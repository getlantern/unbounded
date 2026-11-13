package freddie

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/getlantern/broflake/common"
)

// recordProducerOutcome counts how a producer's previous connection attempt ended, which it
// reports on its next genesis message. Every value is normalized onto a fixed set, so a report
// can't mint label values. A producer that reports nothing is not counted. volunteer_version comes
// from the reporting genesis's VersionHeader, since the process that made the attempt reports it.
func (f *Freddie) recordProducerOutcome(ctx context.Context, span trace.Span, r *http.Request) {
	outcome := common.NormalizeProducerOutcome(r.Form.Get(common.PrevOutcomeField))
	if outcome == "" {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
		attribute.String("ice_state", common.NormalizeICEState(r.Form.Get(common.PrevICEStateField))),
		attribute.String("pc_state", common.NormalizePCState(r.Form.Get(common.PrevPCStateField))),
		attribute.String("nat_timeout_s", common.NormalizeNATTimeout(r.Form.Get(common.PrevNATTimeoutField))),
		attribute.String("volunteer_version", common.NormalizeConsumerVersion(r.Header.Get(common.VersionHeader), common.Version)),
		attribute.String("path", common.OutcomePath(r.Form.Get(common.PrevPathField))),
		attribute.String("vpn", common.NormalizeVPNState(r.Form.Get(common.PrevVPNField))),
	}
	attrs = append(attrs, volunteerKindAttrs(r)...)
	consumer := f.attemptConsumerAttrs(r.Form.Get(common.PrevOfferIDField))
	attrs = append(attrs, consumer...)
	f.producerOutcomes.Add(ctx, 1, metric.WithAttributes(attrs...))
	span.SetAttributes(attribute.String("producer.prev_outcome", outcome))
	if took, ok := common.ParseAttemptDuration(r.Form.Get(common.PrevAttemptMsField)); ok {
		f.handshakeTimes.Record(ctx, took.Seconds(), metric.WithAttributes(
			attribute.String("phase", handshakePhaseAttempt),
			attribute.String("delivery", attrValue(consumer, "delivery")),
			attribute.String("outcome", outcome),
		))
	}
}

// Phases of freddie.handshake.duration. Freddie times the first two as it relays the messages
// that end them. The last is the producer's own report, and the only one that includes ICE.
const (
	// handshakePhaseAnswer runs from Freddie relaying an offer to relaying the volunteer's answer:
	// the volunteer's ICE gathering, plus its round trip to Freddie.
	handshakePhaseAnswer = "volunteer_answer"
	// handshakePhaseConsumerICE runs from the answer to the consumer's first ICE message: the
	// consumer's ICE gathering, which starts once it has the answer.
	handshakePhaseConsumerICE = "consumer_ice"
	// handshakePhaseAttempt is the whole attempt as the volunteer saw it, from taking the offer to
	// its outcome, labeled with that outcome.
	handshakePhaseAttempt = "attempt"
)

// handshakeBuckets, in seconds, resolve the sub-second relays as well as gathering bounded by
// turnGatherWait (8s) and attempts bounded by NATFailTimeout (up to a minute).
var handshakeBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 10, 15, 20, 30, 45, 60, 90}

// recordHandshakePhase records how long a relayed phase of an attempt took.
func (f *Freddie) recordHandshakePhase(ctx context.Context, phase string, took time.Duration, delivery string) {
	f.handshakeTimes.Record(ctx, took.Seconds(), metric.WithAttributes(
		attribute.String("phase", phase),
		attribute.String("delivery", delivery),
	))
}

// deliveryOf labels an attempt whose offer answered genesisID as outcomeDelivery does: room when a
// consumer in one of the producer's rooms took the genesis, pool otherwise.
func (f *Freddie) deliveryOf(genesisID string) string {
	if f.roomGeneses.has(genesisID) {
		return "room"
	}
	return "pool"
}

// attrValue is the string value of attrs' key, or "" without one.
func attrValue(attrs []attribute.KeyValue, key attribute.Key) string {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value.AsString()
		}
	}
	return ""
}

// attemptConsumerAttrs are the consumer_country, consumer_version, consumer_nat, volunteer_nat and
// delivery labels for an attempt that answered offerID.
func (f *Freddie) attemptConsumerAttrs(offerID string) []attribute.KeyValue {
	reported := offerID != ""
	var oc offerConsumer
	if reported {
		oc = f.offerConsumers.take(offerID)
	}
	return []attribute.KeyValue{
		attribute.String("consumer_country", common.OutcomeConsumerCountry(reported, oc.country)),
		attribute.String("consumer_version", common.OutcomeConsumerVersion(reported, oc.version)),
		attribute.String("consumer_nat", outcomeNAT(reported, oc.consumerNAT)),
		attribute.String("volunteer_nat", outcomeNAT(reported, oc.volunteerNAT)),
		// Looked up now rather than when the offer arrived: an offer can overtake the genesis
		// handler's add, but the producer reports only after answering that offer.
		attribute.String("delivery", outcomeDelivery(reported, oc, f.roomGeneses.has(oc.genesisID))),
	}
}

// outcomeDelivery labels how the attempt's genesis was delivered: room when a consumer in one of
// the producer's rooms took it, pool for any other remembered offer, and unreported or unknown as
// outcomeNAT has them. A remembered offer is never the zero offerConsumer: its NATs start out as
// outcomeNATNone.
func outcomeDelivery(offerIDReported bool, oc offerConsumer, room bool) string {
	switch {
	case !offerIDReported:
		return "unreported"
	case oc == offerConsumer{}:
		return "unknown"
	case room:
		return "room"
	default:
		return "pool"
	}
}

// outcomeNAT labels a peer's NAT like common.OutcomeConsumerVersion labels the version: unreported
// when the producer sent no offer ID, and unknown when Freddie doesn't remember the offer.
func outcomeNAT(offerIDReported bool, nat string) string {
	switch {
	case !offerIDReported:
		return "unreported"
	case nat == "":
		return "unknown"
	default:
		return nat
	}
}

// recordSTUNPreflights counts the STUN preflights a producer reports on its genesis: the pass that
// preceded it, and any failures since its last report. A producer that ran no preflight since its
// last genesis, or predates them, reports nothing.
func (f *Freddie) recordSTUNPreflights(ctx context.Context, r *http.Request) {
	passVPN, ok := r.Form[common.PreflightVPNField]
	if !ok {
		return
	}
	version := attribute.String("volunteer_version", common.NormalizeConsumerVersion(r.Header.Get(common.VersionHeader), common.Version))
	kind := volunteerKindAttrs(r)
	f.stunPreflights.Add(ctx, 1, metric.WithAttributes(append([]attribute.KeyValue{
		attribute.String("result", common.PreflightOK),
		attribute.String("vpn", common.NormalizeVPNState(passVPN[0])),
		attribute.String("cause", "none"),
		version,
	}, kind...)...))
	n, ok := common.ParsePreflightFailures(r.Form.Get(common.PrevPreflightFailuresField))
	if !ok {
		return
	}
	result := common.NormalizePreflightFailure(r.Form.Get(common.PrevPreflightOutcomeField))
	// Only a failed socket has a cause.
	cause := "none"
	if result == common.PreflightNoSocket {
		cause = common.NormalizePreflightCause(r.Form.Get(common.PrevPreflightCauseField))
	}
	f.stunPreflights.Add(ctx, int64(n), metric.WithAttributes(append([]attribute.KeyValue{
		attribute.String("result", result),
		attribute.String("vpn", common.NormalizeVPNState(r.Form.Get(common.PrevPreflightVPNField))),
		attribute.String("cause", cause),
		version,
	}, kind...)...))
}

// volunteerKindAttrs are the volunteer_kind and volunteer_mapped labels from the producer's
// genesis.
func volunteerKindAttrs(r *http.Request) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("volunteer_kind", common.NormalizeVolunteerKind(r.Form.Get(common.VolunteerKindField))),
		attribute.String("volunteer_mapped", common.NormalizeVolunteerMapped(r.Form.Get(common.VolunteerMappedField))),
	}
}
