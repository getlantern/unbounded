package common

import (
	"strconv"
	"strings"
)

// A producer reports how its previous connection attempt ended on its next genesis message, so
// Freddie can count where volunteer handshakes fail without any client telemetry pipeline. The
// fields carry only these enumerations and the Freddie-minted ID of the offer the attempt
// answered: no addresses, consumer session IDs, or timings. A producer that
// predates them sends nothing, and a Freddie that predates them ignores them.
const (
	PrevOutcomeField  = "prev-outcome"
	PrevICEStateField = "prev-ice-state"
	PrevPCStateField  = "prev-pc-state"
	// PrevNATTimeoutField is the NATFailTimeout, in whole seconds, the producer ran the attempt
	// with, so outcomes can be compared across timeout settings while clients roll over.
	PrevNATTimeoutField = "prev-nat-timeout"
	// PrevOfferIDField is the ReplyTo of the offer the attempt answered: the ID Freddie gave the
	// consumer's offer request. Freddie already knows it, so it reveals nothing new, and it lets
	// Freddie label the outcome with the country it derived for that consumer.
	PrevOfferIDField = "prev-offer-id"
)

// Producer attempt outcomes, in the order a handshake can fail.
const (
	// OutcomeNoLocalSrflx: the producer's own STUN servers all failed, so it aborted before
	// answering.
	OutcomeNoLocalSrflx = "no_local_srflx"
	// OutcomePartnerHungUp: the consumer was gone by the time the producer's answer arrived.
	OutcomePartnerHungUp = "partner_hung_up"
	// OutcomeNoConsumerICE: the consumer took the answer but never sent its candidates.
	OutcomeNoConsumerICE = "no_consumer_ice"
	// OutcomeBadConsumerICE: the consumer's candidates couldn't be decoded or added.
	OutcomeBadConsumerICE = "bad_consumer_ice"
	// OutcomeConsumerHostOnly: the consumer sent only host candidates.
	OutcomeConsumerHostOnly = "consumer_host_only"
	// OutcomeNATTimeout: signaling completed but no data channel opened within NATFailTimeout.
	// The ICE and peer-connection states then say whether ICE never connected or DTLS/SCTP stalled.
	OutcomeNATTimeout = "nat_timeout"
	// OutcomeConnected: the data channel opened.
	OutcomeConnected = "connected"
)

var producerOutcomes = map[string]bool{
	OutcomeNoLocalSrflx:     true,
	OutcomePartnerHungUp:    true,
	OutcomeNoConsumerICE:    true,
	OutcomeBadConsumerICE:   true,
	OutcomeConsumerHostOnly: true,
	OutcomeNATTimeout:       true,
	OutcomeConnected:        true,
}

// webrtc.ICEConnectionState and webrtc.PeerConnectionState String() values.
var iceStates = map[string]bool{
	"new": true, "checking": true, "connected": true, "completed": true,
	"disconnected": true, "failed": true, "closed": true,
}

var pcStates = map[string]bool{
	"new": true, "connecting": true, "connected": true,
	"disconnected": true, "failed": true, "closed": true,
}

// NormalizeProducerOutcome maps a reported outcome onto the known set, so a malformed or
// malicious report can't mint new metric label values. Empty stays empty (nothing reported).
func NormalizeProducerOutcome(s string) string {
	return normalizeEnum(s, producerOutcomes)
}

// NormalizeICEState does the same for a reported ICE connection state.
func NormalizeICEState(s string) string {
	return normalizeEnum(s, iceStates)
}

// NormalizePCState does the same for a reported peer connection state.
func NormalizePCState(s string) string {
	return normalizeEnum(s, pcStates)
}

// maxReportedNATTimeoutSeconds bounds the reported timeout, so the label has at most this many
// values plus "invalid" and "unreported".
const maxReportedNATTimeoutSeconds = 60

// NormalizeNATTimeout maps a reported timeout onto "1".."60", "unreported" when the producer
// predates the field, or "invalid".
func NormalizeNATTimeout(s string) string {
	if s == "" {
		return "unreported"
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxReportedNATTimeoutSeconds || strconv.Itoa(n) != s {
		return "invalid"
	}
	return s
}

// outcomeCountries are the consumer countries freddie.producer.outcomes labels individually; any
// other known country is "other", so the label has a handful of values.
var outcomeCountries = map[string]bool{"CN": true, "IR": true, "RU": true, "US": true}

// OutcomeConsumerCountry maps the country Freddie derived for an attempt's consumer onto the
// consumer_country label: one of outcomeCountries, "other", "unknown" when Freddie has no country
// for the offer, or "unreported" when the producer predates PrevOfferIDField.
func OutcomeConsumerCountry(offerIDReported bool, country string) string {
	switch {
	case !offerIDReported:
		return "unreported"
	case country == "":
		return "unknown"
	case outcomeCountries[country]:
		return country
	default:
		return "other"
	}
}

// NormalizeConsumerVersion labels within this many patch versions behind and ahead of Freddie's
// own Version stay exact, so the consumer_version label has at most 42 values however a
// consumer sets its VersionHeader.
const (
	consumerVersionPatchesBehind = 31
	consumerVersionPatchesAhead  = 8
)

// NormalizeConsumerVersion maps the VersionHeader a consumer sent with its offer onto the
// consumer_version label, relative to current, the Version Freddie was built with: the version
// itself when it has current's major and minor and a patch within the window above; "older" or
// "newer" for any other version with current's major; "not_sent" when the header was empty; or
// "invalid". The result never shares memory with s, so caching it doesn't keep the header alive.
func NormalizeConsumerVersion(s, current string) string {
	if s == "" {
		return "not_sent"
	}
	major, minor, patch, ok := parseVersion(s)
	curMajor, curMinor, curPatch, curOK := parseVersion(current)
	if !ok || !curOK || major != curMajor {
		return "invalid"
	}
	switch {
	case minor < curMinor || minor == curMinor && patch < curPatch-consumerVersionPatchesBehind:
		return "older"
	case minor > curMinor || patch > curPatch+consumerVersionPatchesAhead:
		return "newer"
	}
	return "v" + strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
}

// parseVersion parses a canonical "vMAJOR.MINOR.PATCH" with no pre-release or build suffix.
func parseVersion(s string) (major, minor, patch int, ok bool) {
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(rest, ".", 4)
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var n [3]int
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 || strconv.Itoa(v) != part {
			return 0, 0, 0, false
		}
		n[i] = v
	}
	return n[0], n[1], n[2], true
}

// OutcomeConsumerVersion maps the NormalizeConsumerVersion value Freddie cached for an attempt's
// offer onto the consumer_version label, with "unknown" when Freddie has no entry for the offer
// and "unreported" when the producer predates PrevOfferIDField.
func OutcomeConsumerVersion(offerIDReported bool, version string) string {
	switch {
	case !offerIDReported:
		return "unreported"
	case version == "":
		return "unknown"
	default:
		return version
	}
}

func normalizeEnum(s string, known map[string]bool) string {
	if s == "" || known[s] {
		return s
	}
	return "invalid"
}
