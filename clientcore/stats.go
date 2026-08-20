package clientcore

import (
	"net"
	"sync"
	"sync/atomic"
)

// Aggregate, always-on lifetime counters for the local Broflake engine. These
// are the numbers a headless widget wants to report periodically: how much it
// has relayed and for whom. They are deliberately cheap — a handful of atomic
// adds on the data-plane chunk path, in the same spirit as the per-second
// bytesPerSec counter DownstreamUIHandler already maintains unconditionally.
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
	statIncomingConns  atomic.Uint64 // consumer/peer connections established (cumulative)
	statOutgoingConns  atomic.Uint64 // egress websocket dials established (cumulative)
	statPeersConnected atomic.Int64  // currently-connected peers (gauge)

	peersSeenMu sync.Mutex
	peersSeen   = make(map[string]struct{}) // distinct peer addresses observed
)

// recordPeerConnect accounts for a newly-connected consumer/peer.
func recordPeerConnect(addr net.IP) {
	statIncomingConns.Add(1)
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

// StatsSnapshot is an atomic-free copy of the engine's lifetime counters.
type StatsSnapshot struct {
	BytesToPeers   uint64 // download relayed toward peers (egress -> peer)
	BytesFromPeers uint64 // upload relayed toward egress (peer -> egress)
	IncomingConns  uint64 // peer connections established (cumulative)
	OutgoingConns  uint64 // egress connections dialed (cumulative)
	PeersConnected int64  // peers currently connected (gauge, never negative)
	PeersSeen      int    // distinct peer addresses observed
}

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

	return StatsSnapshot{
		BytesToPeers:   statBytesToPeers.Load(),
		BytesFromPeers: statBytesFromPeers.Load(),
		IncomingConns:  statIncomingConns.Load(),
		OutgoingConns:  statOutgoingConns.Load(),
		PeersConnected: connected,
		PeersSeen:      seen,
	}
}
