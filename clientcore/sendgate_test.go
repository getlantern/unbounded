package clientcore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// simChannel models a data channel's send buffer a millisecond at a time: it transmits up to
// perMs bytes of what is queued, and the far end acknowledges each byte rtt after it went out.
// Its buffered amount, like pion's, is queued plus unacknowledged bytes.
type simChannel struct {
	perMs    uint64
	rtt      time.Duration
	queued   uint64
	inFlight []simFlight
	acked    uint64
	now      time.Time
}

type simFlight struct {
	bytes uint64
	ackAt time.Time
}

func newSimChannel(perMs uint64, rtt time.Duration) *simChannel {
	return &simChannel{perMs: perMs, rtt: rtt, now: time.Unix(1_000_000, 0)}
}

func (c *simChannel) buffered() uint64 {
	b := c.queued
	for _, f := range c.inFlight {
		b += f.bytes
	}
	return b
}

// offer passes a packet of n bytes through g, queuing it if g allows it.
func (c *simChannel) offer(g *sendGate, n int) bool {
	ok := g.allow(n, c.buffered(), c.now)
	if ok {
		c.queued += uint64(n)
	}
	return ok
}

// tick advances a millisecond: it transmits and takes acknowledgements.
func (c *simChannel) tick() {
	c.now = c.now.Add(time.Millisecond)
	if tx := min(c.queued, c.perMs); tx > 0 {
		c.queued -= tx
		c.inFlight = append(c.inFlight, simFlight{tx, c.now.Add(c.rtt)})
	}
	for len(c.inFlight) > 0 && !c.inFlight[0].ackAt.After(c.now) {
		c.acked += c.inFlight[0].bytes
		c.inFlight = c.inFlight[1:]
	}
}

func (c *simChannel) run(ms int) {
	for range ms {
		c.tick()
	}
}

func TestSendGate_NeverDropsBelowTheFloor(t *testing.T) {
	g := newSendGate(0)
	c := newSimChannel(1, 50*time.Millisecond)
	for range 13 {
		assert.True(t, c.offer(g, 1200))
		c.run(1000)
	}
	assert.Zero(t, g.dropped)
}

// On an idle, fast path with a long round trip, steadily paced traffic keeps a round trip's worth
// of bytes awaiting acknowledgement. That isn't a queue: neither it nor a burst after it is cut.
func TestSendGate_PacedTrafficAndABurstOnAnIdleLongPath(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	c := newSimChannel(10_000, 40*time.Millisecond) // 10MB/s
	for range 200 {
		assert.True(t, c.offer(g, 1200))
		c.run(10)
	}
	for range 20 {
		assert.True(t, c.offer(g, 1200), "a burst on an uncongested path")
	}
	assert.Zero(t, g.dropped)
}

// Bursts with idle gaps between them never queue for long, so none is cut, including bursts that
// straddle any boundary.
func TestSendGate_BurstsWithIdleGaps(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	c := newSimChannel(10_000, 40*time.Millisecond)
	for i := range 100 {
		for range 20 {
			c.offer(g, 1200)
		}
		c.run(97 + i%7)
	}
	assert.Zero(t, g.dropped)
}

// aimdSender offers packets as a congestion-controlled sender such as QUIC does: it halves its
// rate on a drop, at most once a round trip, and otherwise adds about a packet a round trip.
type aimdSender struct {
	perMs    float64
	backoff  time.Time
	credit   float64
	maxPerMs float64
}

// send offers this millisecond's packets of 1200 bytes to c through g.
func (s *aimdSender) send(c *simChannel, g *sendGate) {
	s.credit += s.perMs
	for ; s.credit >= 1200; s.credit -= 1200 {
		if !c.offer(g, 1200) && !c.now.Before(s.backoff) {
			s.perMs = max(s.perMs/2, 100)
			s.backoff = c.now.Add(c.rtt)
		}
	}
	s.perMs = min(s.perMs+1200/float64(c.rtt.Milliseconds()), s.maxPerMs)
}

// simulate runs a congestion-controlled sender into c through g for ms milliseconds, and returns
// the longest a packet joining the queue would have waited to go out, after the first second.
func simulate(c *simChannel, g *sendGate, s *aimdSender, ms int) time.Duration {
	var worst time.Duration
	for i := range ms {
		s.send(c, g)
		c.tick()
		if i > 1000 {
			worst = max(worst, time.Duration(c.queued/c.perMs)*time.Millisecond)
		}
	}
	return worst
}

// A congestion-controlled sender that wants more than the channel carries is held to a queue near
// the target, where without the gate the queue would grow without bound.
func TestSendGate_HoldsTheQueueNearTheTarget(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(10 * time.Millisecond)
	c := newSimChannel(1000, 10*time.Millisecond) // 1MB/s
	worst := simulate(c, g, &aimdSender{perMs: 1000, maxPerMs: 4000}, 5000)
	assert.Positive(t, g.dropped)
	assert.LessOrEqual(t, worst, 40*time.Millisecond)
	assert.GreaterOrEqual(t, float64(c.acked), 0.85*1000*(5000-10), "the channel stays busy")
}

// On a long round trip the gate keeps the channel busy: a round trip's worth in flight is not
// taken for a queue, which a fixed threshold would cap.
func TestSendGate_KeepsALongPathBusy(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(80 * time.Millisecond)
	c := newSimChannel(5000, 80*time.Millisecond) // 5MB/s, 400KB in flight
	simulate(c, g, &aimdSender{perMs: 5000, maxPerMs: 10_000}, 8000)
	assert.GreaterOrEqual(t, float64(c.acked), 0.85*5000*(8000-80))
}

// Traffic that doesn't back off is still held near the limit, since the gate predicts each
// packet's wait rather than waiting for it to show.
func TestSendGate_BoundsTrafficThatDoesNotBackOff(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(10 * time.Millisecond)
	c := newSimChannel(1000, 10*time.Millisecond)
	var worst time.Duration
	for i := range 3000 {
		for range 4 {
			c.offer(g, 1000)
		}
		c.tick()
		if i > 1000 {
			worst = max(worst, time.Duration(c.queued/c.perMs)*time.Millisecond)
		}
	}
	assert.LessOrEqual(t, worst, 40*time.Millisecond)
}

// Once the oldest buffered byte has waited past the round trip plus the target, the gate drops.
func TestSendGate_DropsOnceTheOldestByteIsTooOld(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	start := time.Now()
	for range 20 {
		g.allow(1200, 0, start)
	}
	stuck := uint64(20 * 1200) // nothing acknowledged
	assert.True(t, g.allow(1200, stuck, start.Add(60*time.Millisecond)))
	assert.False(t, g.allow(1200, stuck+1200, start.Add(61*time.Millisecond)))
	assert.Equal(t, uint64(1), g.dropped)
}

// Until the round trip is measured, the gate can't tell a queue from data in flight, so it stays
// open. In the browser it is never measured.
func TestSendGate_StaysOpenUntilTheRoundTripIsKnown(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	start := time.Now()
	for range 20 {
		g.allow(1200, 0, start)
	}
	stuck := uint64(20 * 1200)
	assert.True(t, g.allow(1200, stuck, start.Add(10*time.Second)))
	g.setRTT(0)
	assert.Zero(t, g.rtt, "an unmeasured round trip doesn't open a closed gate's estimate")
	g.setRTT(40 * time.Millisecond)
	assert.False(t, g.allow(1200, stuck+1200, start.Add(10*time.Second)))
}

// Bursts between which the send queue empties, while a long round trip leaves bytes awaiting
// acknowledgement across the gaps, must not teach the gate the bursts' average rate.
func TestSendGate_DoesNotLearnTheRateAcrossBurstGaps(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(80 * time.Millisecond)
	c := newSimChannel(1000, 80*time.Millisecond) // 1MB/s
	for range 200 {
		for range 10 {
			c.offer(g, 1200)
		}
		c.run(43)
	}
	assert.Zero(t, g.dropped)
}

// A smoothed round trip that trails the path's must not turn the path's own delay into a queue.
func TestSendGate_ToleratesAnUnderestimatedRoundTrip(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	c := newSimChannel(10_000, 50*time.Millisecond) // the path is 50ms
	for range 200 {
		c.offer(g, 1200)
		c.run(1)
	}
	for range 20 {
		assert.True(t, c.offer(g, 1200))
	}
	assert.Zero(t, g.dropped)
}

// The gate keeps a mark per buffered send, not per send ever made.
func TestSendGate_ForgetsReleasedSends(t *testing.T) {
	g := newSendGate(0)
	c := newSimChannel(10_000, 10*time.Millisecond)
	for range 100_000 {
		c.offer(g, 1200)
		c.tick()
	}
	assert.Less(t, len(g.marks), 1000)
}
