package freddie

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/getlantern/broflake/common"
)

// recordProducerOutcome counts how a producer's previous connection attempt ended, which it
// reports on its next genesis message. Every value is normalized onto a fixed set, so a report
// can't mint label values. A producer that reports nothing is not counted.
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
	}
	attrs = append(attrs, f.attemptConsumerAttrs(r.Form.Get(common.PrevOfferIDField))...)
	f.producerOutcomes.Add(ctx, 1, metric.WithAttributes(attrs...))
	span.SetAttributes(attribute.String("producer.prev_outcome", outcome))
}

// attemptConsumerAttrs are the consumer_country and consumer_version labels for an attempt that
// answered offerID.
func (f *Freddie) attemptConsumerAttrs(offerID string) []attribute.KeyValue {
	reported := offerID != ""
	var oc offerConsumer
	if reported {
		oc = f.offerConsumers.take(offerID)
	}
	return []attribute.KeyValue{
		attribute.String("consumer_country", common.OutcomeConsumerCountry(reported, oc.country)),
		attribute.String("consumer_version", common.OutcomeConsumerVersion(reported, oc.version)),
	}
}
