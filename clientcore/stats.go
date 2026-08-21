package clientcore

import (
	"net"
	"sync"
	"sync/atomic"
)

// Aggregate, always-on lifetime counters for the local Broflake engine. These
// are the numbers a headless widget wants to report periodically: how much it
// has relayed, for whom, and how its WebRTC session attempts turned out. They are
// deliberately cheap — a handful of atomic adds on the data-plane chunk path, in
// the same spirit as the per-second bytesPerSec counter DownstreamUIHandler
// already maintains unconditionally.
//
// Like the BROFLAKE_STATS counters in user.go, these are process-global rather
// than per-engine: broflake runs exactly one engine per process, so global
// aggregation matches reality. The data-plane direction labels are from the
// widget's point of view as a relay:
//
//	"to peers"   = chunks the engine delivered toward the consumer/peer side
//	               (download: egress -> widget -> peer), seen on the observer's
//	               Upstream.rx -> Downstream.rx forwarder (onRx / DownstreamUIHandler).
//	"from peers" = chunks the engine relayed out toward egress
//	               (upload: peer -> widget -> egress), seen on the observer's
//	               Downstream.tx -> Upstream.tx forwarder (onTx / UpstreamUIHandler).
var (
	statBytesToPeers   atomic.Uint64
	statBytesFromPeers atomic.Uint64
	statEgressDials    atomic.Uint64 // egress websocket dials established (cumulative)
	statPeersConnected atomic.Int64  // currently-connected peers (gauge)

	peersSeenMu sync.Mutex
	peersSeen   = make(map[string]struct{}) // distinct peer addresses observed

	// outcomeCounts tallies the terminal outcome of every WebRTC session attempt,
	// keyed by "success" or a failure reason (the same reason string carried on the
	// "connection attempt failed" log line). Attempts still in flight are not yet
	// counted, so the tallies always sum to concluded attempts.
	outcomeMu     sync.Mutex
	outcomeCounts = make(map[string]uint64)
)

// OutcomeSuccess is the outcome key recorded when a session fully establishes.
const OutcomeSuccess = "success"

// recordPeerConnect accounts for a newly-connected consumer/peer.
func recordPeerConnect(addr net.IP) {
	statPeersConnected.Add(1)
	if addr != nil {
		peersSeenMu.Lock()
		peersSeen[addr.String()] = struct{}{}
		peersSeenMu.Unlock()
	}
}

// recordPeerDisconnect accounts for a consumer/peer disconnecting.
func recordPeerDisconnect() {
	statPeersConnected.Add(-1)
}

// recordOutcome tallies the terminal outcome of one WebRTC session attempt:
// OutcomeSuccess for a fully-established datachannel, otherwise the failure reason.
func recordOutcome(outcome string) {
	outcomeMu.Lock()
	outcomeCounts[outcome]++
	outcomeMu.Unlock()
}

// StatsSnapshot is an atomic-free copy of the engine's lifetime counters.
type StatsSnapshot struct {
	BytesToPeers   uint64            // download relayed toward peers (egress -> peer)
	BytesFromPeers uint64            // upload relayed toward egress (peer -> egress)
	EgressDials    uint64            // egress connections dialed (cumulative)
	ActivePeers    int64             // peers currently connected (gauge, never negative)
	DistinctPeers  int               // distinct peer addresses served (lifetime)
	Outcomes       map[string]uint64 // session-attempt outcomes by "success"/reason
}

// Attempts is the number of concluded WebRTC session attempts (successes + failures).
func (s StatsSnapshot) Attempts() uint64 {
	var n uint64
	for _, c := range s.Outcomes {
		n += c
	}
	return n
}

// Succeeded is the number of attempts that fully established a datachannel.
func (s StatsSnapshot) Succeeded() uint64 { return s.Outcomes[OutcomeSuccess] }

// Failed is the number of attempts that ended in some failure reason.
func (s StatsSnapshot) Failed() uint64 { return s.Attempts() - s.Succeeded() }

// Stats returns a consistent-enough snapshot of the engine's lifetime counters.
// The reads are individually atomic but not mutually atomic; for periodic
// human-facing logging that is fine.
func Stats() StatsSnapshot {
	connected := statPeersConnected.Load()
	if connected < 0 {
		// A disconnect can be observed without a matching connect during startup
		// churn; don't surface a negative gauge.
		connected = 0
	}

	peersSeenMu.Lock()
	seen := len(peersSeen)
	peersSeenMu.Unlock()

	outcomeMu.Lock()
	outcomes := make(map[string]uint64, len(outcomeCounts))
	for k, v := range outcomeCounts {
		outcomes[k] = v
	}
	outcomeMu.Unlock()

	return StatsSnapshot{
		BytesToPeers:   statBytesToPeers.Load(),
		BytesFromPeers: statBytesFromPeers.Load(),
		EgressDials:    statEgressDials.Load(),
		ActivePeers:    connected,
		DistinctPeers:  seen,
		Outcomes:       outcomes,
	}
}
