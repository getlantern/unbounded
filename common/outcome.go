package common

// A producer reports how its previous connection attempt ended on its next genesis message, so
// Freddie can count where volunteer handshakes fail without any client telemetry pipeline. The
// fields carry only these enumerations: no addresses, session IDs, or timings. A producer that
// predates them sends nothing, and a Freddie that predates them ignores them.
const (
	PrevOutcomeField  = "prev-outcome"
	PrevICEStateField = "prev-ice-state"
	PrevPCStateField  = "prev-pc-state"
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

func normalizeEnum(s string, known map[string]bool) string {
	if s == "" || known[s] {
		return s
	}
	return "invalid"
}
