package clientcore

import (
	"errors"
	"log/slog"
	"time"

	"github.com/quic-go/quic-go"
)

// ErrNoConnectedPath is returned by a rooms-only consumer's QUICLayer.DialContext, and so by
// its SOCKS5Dialer, while it has gone WebRTCOptions.ConsumerNoPathGrace without a connected
// WebRTC slot: with no path to the egress, a dial would only wait for one.
var ErrNoConnectedPath = errors.New("unbounded: no connected WebRTC path")

// defaultConsumerNoPathGrace is WebRTCOptions.ConsumerNoPathGrace's default. It covers one room
// attempt's worst case, so that a slot that drops and reconnects over the room's TURN servers
// isn't given up on while that attempt is still on track: relay-early ICE gathering, which
// usually takes about 1 to 1.5s but is capped at 8s, plus the 10s a room attempt waits for its
// data channel (roomNATFailTimeout). It is still short enough that a user isn't left watching a
// stalled page for long.
const defaultConsumerNoPathGrace = 20 * time.Second

// noPathQUICCode is the QUIC application error code with which a no-path guard resets streams,
// so the egress, should it ever hear of them, can tell them apart.
const noPathQUICCode = 42070

// noPathGuard is a rooms-only consumer's guard against a QUIC connection outliving its path.
//
// A rooms-only consumer reaches one helper, through one WebRTC slot. When that path dies, the
// QUIC connection over it lives on until it idles out (common.QUICCfg's MaxIdleTimeout), and
// every stream sent into it until then hangs, while nothing else re-establishes the path until
// the helper sends a new genesis, which can take a minute or never come. A pool consumer has
// none of this: the pool soon offers it another volunteer, so it has no guard.
//
// Once the consumer has gone failAfter without a connected slot, the guarded QUICLayer resets
// every stream it opened and fails its dials with ErrNoConnectedPath. When a slot connects,
// dials wait for a QUIC connection again as before: the egress either migrates the old
// connection onto the new path or, past its migration window, dials a new one, which replaces
// the old one at once (see QUICLayer.acceptConns).
//
// The guard leaves the QUIC connection itself open. The egress keeps it for migrationWindow (30s,
// egress/egresslib.go) after the volunteer's WebSocket drops, which may be well after our slot
// did, for a returning path to migrate it onto. Were we to close it, quic-go would answer the
// egress's path probe with a CONNECTION_CLOSE for only 3 PTOs; past that our transport drops
// packets for the connection unanswered, so the probe would wait out the egress's probeTimeout
// (35s) instead, an outage longer than the one it ends. Closing it also gains nothing once
// acceptConns replaces a dead connection with the egress's next one as soon as it arrives:
// until then, with the path failed, no dial uses it.
type noPathGuard struct {
	slots     *consumerSlots
	failAfter time.Duration
}

// newNoPathGuard returns a guard over slots that fails after grace (see
// WebRTCOptions.ConsumerNoPathGrace), or nil if grace turns it off.
func newNoPathGuard(slots *consumerSlots, grace time.Duration) *noPathGuard {
	if grace < 0 || slots == nil {
		return nil
	}
	if grace == 0 {
		grace = defaultConsumerNoPathGrace
	}
	return &noPathGuard{slots: slots, failAfter: grace}
}

// guardPaths runs c's no-path guard until c's context ends.
func (c *QUICLayer) guardPaths() {
	g := c.noPath
	started := time.Now()
	changed, stop := g.slots.watch()
	defer stop()

	var failTimer *time.Timer
	var fail <-chan time.Time
	disarm := func() {
		if failTimer != nil {
			failTimer.Stop()
		}
		fail = nil
	}
	defer disarm()

	// The state last acted on. Comparing since as well catches a slot that
	// connected and dropped again between two signals.
	var lastConnected bool
	var lastSince time.Time
	evaluate := func() {
		connected, since := g.slots.connected()
		if connected == lastConnected && since.Equal(lastSince) {
			return
		}
		lastConnected, lastSince = connected, since
		disarm()
		if connected {
			c.restorePath()
			return
		}
		// Measured from when the slot dropped, not from when we heard of it,
		// or, before any slot has connected, from when the guard started.
		if since.IsZero() {
			since = started
		}
		failTimer = time.NewTimer(g.failAfter - time.Since(since))
		fail = failTimer.C
	}
	// Force the first evaluation: a consumer starts with no slot connected,
	// and it gets failAfter to connect one.
	lastConnected = true
	evaluate()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-changed:
			evaluate()
		case <-fail:
			fail = nil
			// A timer armed for a state that has since changed (a slot that
			// connected, or dropped again, before we heard) is stale: act on
			// the current state instead.
			if connected, since := g.slots.connected(); connected != lastConnected || !since.Equal(lastSince) {
				evaluate()
				continue
			}
			c.failPath()
		}
	}
}

// failPath fails c's dials with ErrNoConnectedPath, waking those waiting for a connection, and
// resets every stream c has opened.
func (c *QUICLayer) failPath() {
	c.mx.Lock()
	if !isClosed(c.pathDown) {
		close(c.pathDown)
	}
	c.mx.Unlock()

	// After pathDown closes, so a stream trackStream lets through is reset here.
	c.smx.Lock()
	streams := c.streams
	c.streams = make(map[*quic.Stream]struct{})
	c.smx.Unlock()
	for s := range streams {
		s.CancelRead(noPathQUICCode)
		s.CancelWrite(noPathQUICCode)
	}
	slog.Debug("No connected WebRTC path, failing QUIC streams and dials",
		"grace_s", c.noPath.failAfter.Seconds(),
		"streams_reset", len(streams),
	)
}

// restorePath lets c's dials wait for a QUIC connection again.
func (c *QUICLayer) restorePath() {
	c.mx.Lock()
	defer c.mx.Unlock()
	if isClosed(c.pathDown) {
		c.pathDown = make(chan struct{})
		slog.Debug("WebRTC path connected again, QUIC dials resumed")
	}
}

// trackStream records s for failPath to reset, reporting false, without recording it, if
// the path has already failed.
func (c *QUICLayer) trackStream(s *quic.Stream) bool {
	c.smx.Lock()
	defer c.smx.Unlock()
	c.mx.RLock()
	down := isClosed(c.pathDown)
	c.mx.RUnlock()
	if down {
		return false
	}
	c.streams[s] = struct{}{}
	return true
}

func (c *QUICLayer) untrackStream(s *quic.Stream) {
	c.smx.Lock()
	delete(c.streams, s)
	c.smx.Unlock()
}

// isNoPathReset reports whether err is a no-path guard's local reset of a stream (see failPath).
func isNoPathReset(err error) bool {
	var se *quic.StreamError
	return errors.As(err, &se) && !se.Remote && se.ErrorCode == noPathQUICCode
}

// isClosed reports whether ch is closed. A nil ch never is.
func isClosed(ch chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
