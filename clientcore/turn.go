package clientcore

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// turnGatherWait bounds ICE gathering on a peer connection that has a room pairing's TURN servers.
// Gathering completes only once every server has answered or failed, and pion dials a TCP or TLS
// TURN server with no timeout, so a censor that drops its SYNs would hold gathering for the OS
// connect timeout (over a minute), and a blocked UDP TURN server for seconds of allocate
// retransmits. Freddie gives each side remoteICEGatheringTTL (15s) to gather and reply, so the
// attempt would fail rather than fall back to the candidates it has. 8s is past pion's 5s STUN
// gather timeout, so host and server-reflexive candidates are in by then, and leaves 7s of
// Freddie's window for signaling them.
const turnGatherWait = 8 * time.Second

// turnRelayGrace is how long a wait bounded by turnGatherWait goes on once the first relay
// candidate arrives. A room pairing comes with several TURN servers, each of which usually
// allocates a relay within a few hundred milliseconds, but gathering completes only once every one
// has answered, and now and then one allocation is never answered and holds gathering until pion's
// transaction timeout, close to turnGatherWait. One relay is enough to connect through, so rather
// than spend most of Freddie's window on the server that stalled, the wait goes on shortly after
// the first relay, which leaves time for the other relays and server-reflexive candidates arriving
// at about the same time.
const turnRelayGrace = 500 * time.Millisecond

// gatherEnd is how a wait for ICE gathering ended.
type gatherEnd int

const (
	// gatherCompleted: gathering completed, so every candidate is in.
	gatherCompleted gatherEnd = iota
	// gatherAfterRelay: gathering was still running, but a relay candidate had arrived and the
	// grace after it ran out.
	gatherAfterRelay
	// gatherCapped: gathering was still running when the wait reached its limit.
	gatherCapped
)

// awaitGathering waits for gatherComplete. If limit is positive it waits for at most limit, and
// once firstRelay closes it waits for at most grace more, whichever ends first. A nil firstRelay
// never closes. Gathering goes on after a wait that gives up.
func awaitGathering(gatherComplete, firstRelay <-chan struct{}, grace, limit time.Duration) gatherEnd {
	var capped, graced <-chan time.Time
	if limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		capped = t.C
	}
	for {
		select {
		case <-gatherComplete:
			return gatherCompleted
		case <-firstRelay:
			firstRelay = nil
			t := time.NewTimer(grace)
			defer t.Stop()
			graced = t.C
		case <-graced:
			return gatheredBy(gatherComplete, gatherAfterRelay)
		case <-capped:
			return gatheredBy(gatherComplete, gatherCapped)
		}
	}
}

// gatheredBy is gatherCompleted if gatherComplete is closed, and otherwise end. select picks at
// random among ready cases, so gathering that completed just as a timer fired may not have been
// chosen.
func gatheredBy(gatherComplete <-chan struct{}, end gatherEnd) gatherEnd {
	select {
	case <-gatherComplete:
		return gatherCompleted
	default:
		return end
	}
}

// logGatherEnd logs how a wait for ICE gathering ended, after waited.
func logGatherEnd(end gatherEnd, waited time.Duration) {
	waited = waited.Round(time.Millisecond)
	switch end {
	case gatherCompleted:
		slog.Debug("ICE gathering complete!", "waited", waited)
	case gatherAfterRelay:
		slog.Debug("ICE gathering still running, going on shortly after the first relay candidate", "waited", waited)
	default:
		slog.Debug("ICE gathering still running at its limit, going on with the candidates gathered so far", "waited", waited)
	}
}

// roomNATFailTimeout is the least time an attempt with a room pairing's TURN servers waits for its
// data channel to open. NATFailTimeout is sized for pool attempts, whose usual pair is server
// reflexive and connects within a couple of seconds. A room attempt is more likely to connect
// through a relay, which takes longer: pion nominates a relay pair only after
// relayAcceptanceMinWait (2s) of checks, and the checks and DTLS handshake through it each take a
// round trip via the TURN server. Field logs show room attempts giving up right at the default 5s.
const roomNATFailTimeout = 10 * time.Second

// natFailWindow is how long an attempt waits for its data channel to open (see awaitDataChannel):
// natFail, raised to roomNATFailTimeout for an attempt with a room pairing's TURN servers.
func natFailWindow(natFail time.Duration, roomTURN bool) time.Duration {
	if roomTURN {
		return max(natFail, roomNATFailTimeout)
	}
	return natFail
}

// candidateLog collects the local candidates OnICECandidate reports. A bounded wait reads
// them while gathering may still be reporting more, so they are locked. The zero value is ready
// to use.
type candidateLog struct {
	mu    sync.Mutex
	cands []webrtc.ICECandidate
	// relay is closed when the first relay candidate is added, which sawRelay records.
	relay    chan struct{}
	sawRelay bool
}

// add records c, skipping the null candidate (a nil pointer) that marks the end of gathering.
func (g *candidateLog) add(c *webrtc.ICECandidate) {
	if c == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cands = append(g.cands, *c)
	if c.Typ == webrtc.ICECandidateTypeRelay && !g.sawRelay {
		g.sawRelay = true
		close(g.relayLocked())
	}
}

// firstRelay returns a channel that is closed once a relay candidate has been added.
func (g *candidateLog) firstRelay() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.relayLocked()
}

func (g *candidateLog) relayLocked() chan struct{} {
	if g.relay == nil {
		g.relay = make(chan struct{})
	}
	return g.relay
}

// snapshot returns a copy of the candidates gathered so far, which later ones don't change.
func (g *candidateLog) snapshot() []webrtc.ICECandidate {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]webrtc.ICECandidate(nil), g.cands...)
}

// candidateCounts is cands as counts by type, in a fixed order and with no addresses, for logs. Every
// type is listed even at zero, so a log always shows whether a TURN server gave a relay candidate.
func candidateCounts(cands []webrtc.ICECandidate) string {
	var t candidateTally
	for _, c := range cands {
		switch c.Typ {
		case webrtc.ICECandidateTypeHost:
			t.host++
		case webrtc.ICECandidateTypeSrflx:
			t.srflx++
		case webrtc.ICECandidateTypePrflx:
			t.prflx++
		case webrtc.ICECandidateTypeRelay:
			t.relay++
		}
	}
	return t.String()
}

// candidateTally is a set of candidates counted by type.
type candidateTally struct {
	host, srflx, prflx, relay int
}

// String is the tally as candidateCounts writes it.
func (t candidateTally) String() string {
	return fmt.Sprintf("host=%d srflx=%d prflx=%d relay=%d", t.host, t.srflx, t.prflx, t.relay)
}

// selectedPair is pc's selected candidate pair, or nil when no pair is selected or the browser
// doesn't say.
func selectedPair(pc *webrtc.PeerConnection) (pair *webrtc.ICECandidatePair) {
	defer func() {
		if recover() != nil {
			pair = nil
		}
	}()
	sctp := pc.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return nil
	}
	p, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || p == nil || p.Local == nil || p.Remote == nil {
		return nil
	}
	return p
}

// selectedPairTypes is pc's selected candidate pair as common.PairTypes, its local end first, or ""
// when no pair is selected or the browser doesn't say.
func selectedPairTypes(pc *webrtc.PeerConnection) string {
	p := selectedPair(pc)
	if p == nil {
		return ""
	}
	return common.PairTypes(p.Local.Typ.String(), p.Remote.Typ.String())
}

// selectedPath reports whether pc's selected candidate pair goes through a TURN relay:
// common.PathRelay, common.PathDirect, or "" when no pair is selected or the browser doesn't say.
func selectedPath(pc *webrtc.PeerConnection) string {
	return pairPath(selectedPair(pc))
}

// pairPath is selectedPath for the pair p, which may be nil.
func pairPath(p *webrtc.ICECandidatePair) string {
	switch {
	case p == nil:
		return ""
	case p.Local.Typ == webrtc.ICECandidateTypeRelay || p.Remote.Typ == webrtc.ICECandidateTypeRelay:
		return common.PathRelay
	default:
		return common.PathDirect
	}
}

// pairDescription is p's candidate types and transport protocols, its local end first, such as
// "relay/udp - srflx/udp", with no addresses; "" for a nil p.
func pairDescription(p *webrtc.ICECandidatePair) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s - %s/%s", p.Local.Typ, p.Local.Protocol, p.Remote.Typ, p.Remote.Protocol)
}

// pathLogEvery is how often a connected peer connection logs its selected pair's round trip and
// traffic (see logPath).
const pathLogEvery = 30 * time.Second

// logPath logs, at debug level, pc's selected candidate pair as pairDescription does, whether it
// goes through a TURN relay, and, where pion measures them, the pair's latest ICE round trip and
// the bytes sent and received on it. No address is logged. It does nothing when debug logging is
// off, so a connected peer can call it every pathLogEvery at no cost.
func logPath(pc *webrtc.PeerConnection, msg string) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	p := selectedPair(pc)
	attrs := []any{"pair", pairDescription(p), "path", pairPath(p)}
	if s, ok := selectedPairStats(pc); ok {
		attrs = append(attrs, "rtt", s.rtt.Round(time.Millisecond), "bytes_sent", s.sent, "bytes_received", s.received)
	}
	slog.Debug(msg, attrs...)
}

// pairStats is what pion measures on a selected candidate pair: its latest ICE round trip, and the
// bytes sent and received on it.
type pairStats struct {
	rtt            time.Duration
	sent, received uint64
}
