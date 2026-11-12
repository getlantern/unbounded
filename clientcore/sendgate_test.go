package clientcore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// drainAt feeds g a channel that sends perTick bytes and drains drainPerTick of them every
// gateSampleEvery, for ticks samples, and returns the buffered amount and time it ends at.
func drainAt(g *sendGate, start time.Time, buffered uint64, perTick, drainPerTick uint64, ticks int) (uint64, time.Time) {
	now := start
	for range ticks {
		g.allow(int(perTick), buffered, now)
		buffered = buffered + perTick - min(drainPerTick, buffered+perTick)
		now = now.Add(gateSampleEvery)
	}
	return buffered, now
}

func TestSendGate_NeverDropsBelowTheFloor(t *testing.T) {
	g := newSendGate(0)
	now := time.Now()
	for i := range 100 {
		assert.True(t, g.allow(1200, gateFloor, now.Add(time.Duration(i)*time.Millisecond)))
	}
	assert.Zero(t, g.dropped)
}

// Until a drain rate is measured, the buffer may grow to gateStartCap and no further.
func TestSendGate_CapsTheBufferBeforeItKnowsTheRate(t *testing.T) {
	g := newSendGate(0)
	now := time.Now()
	assert.True(t, g.allow(1200, gateStartCap, now))
	assert.False(t, g.allow(1200, gateStartCap+1, now))
}

// With the drain rate known, the gate allows a round trip plus the target's worth of buffered
// data and drops beyond it.
func TestSendGate_DropsBeyondTheRoundTripPlusTarget(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	// A backlog draining at 1MB/s: 60KB buffered is 60ms, the most the gate allows.
	_, now := drainAt(g, time.Now(), 100_000, 50_000, 50_000, 20)
	assert.InDelta(t, 1e6, g.rate, 1e5)

	assert.True(t, g.admits(55_000), "55ms of drain is within 40ms + 20ms")
	assert.False(t, g.admits(70_000), "70ms of drain is past it")
	before := g.dropped
	assert.False(t, g.allow(1200, 70_000, now.Add(time.Millisecond)))
	assert.Equal(t, before+1, g.dropped)
}

// Where the round trip can't be measured, the gate assumes gateFallbackRTT rather than none, so a
// distant browser peer's data in flight isn't taken for a queue.
func TestSendGate_FallsBackToADefaultRoundTrip(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	drainAt(g, time.Now(), 100_000, 50_000, 50_000, 20)
	assert.True(t, g.admits(110_000), "110ms of drain is within 100ms + 20ms")
	assert.False(t, g.admits(130_000))
	g.setRTT(0)
	assert.Zero(t, g.rtt, "an unmeasured round trip doesn't replace the fallback")
}

// A channel that drains slower than it is fed stops being fed once its queue passes the target,
// and its buffer stays bounded instead of growing with every send.
func TestSendGate_HoldsTheQueueNearTheTarget(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(10 * time.Millisecond)
	now := time.Now()
	buffered := uint64(0)
	// Offered 4MB/s into a 1MB/s channel, in 1200-byte packets.
	for range 2000 {
		for range 10 {
			if g.allow(1200, buffered, now) {
				buffered += 1200
			}
		}
		buffered -= min(buffered, 1000) // 1MB/s at one tick per millisecond
		now = now.Add(time.Millisecond)
	}
	assert.Positive(t, g.dropped)
	// 10ms + 20ms at 1MB/s is 30KB; allow the rate estimate some slack.
	assert.Less(t, buffered, uint64(45_000))
}

// Traffic that comes in bursts with idle gaps between them never builds a backlog, so it says
// nothing about the channel's capacity: the gate must not learn the offered rate as the drain rate
// and then drop the next burst on a fast path.
func TestSendGate_DoesNotLearnTheRateFromBursts(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	now := time.Now()
	for range 100 {
		// Twenty 1200-byte packets at once, into a channel that drains them within a millisecond.
		buffered := uint64(0)
		for range 20 {
			assert.True(t, g.allow(1200, buffered, now))
			buffered += 1200
		}
		now = now.Add(100 * time.Millisecond)
		g.allow(0, 0, now)
	}
	assert.Zero(t, g.dropped)
	assert.Zero(t, g.rate, "no interval had a backlog to measure")
}

// A backlog measured after an idle period still teaches the gate the channel's rate.
func TestSendGate_LearnsTheRateFromABacklogAfterIdle(t *testing.T) {
	g := newSendGate(0)
	now := time.Now()
	g.allow(0, 0, now)
	now = now.Add(time.Second)
	// 100KB buffered, draining at 1MB/s while 50KB more is sent per 50ms sample.
	drainAt(g, now, 100_000, 50_000, 50_000, 20)
	assert.InDelta(t, 1e6, g.rate, 1e5)
}

// A burst that straddles a sample boundary leaves data buffered at both ends of the interval
// though the buffer ran dry within it; that interval must not teach the gate a rate either.
func TestSendGate_DoesNotLearnFromAnIntervalThatRanDry(t *testing.T) {
	g := newSendGate(20 * time.Millisecond)
	g.setRTT(40 * time.Millisecond)
	start := time.Now()
	g.allow(0, 0, start)
	for i := range 50 {
		base := start.Add(time.Duration(i) * gateSampleEvery)
		// Twenty packets just before the boundary into an empty buffer, then one on it.
		buffered := uint64(0)
		for range 20 {
			g.allow(1200, buffered, base.Add(gateSampleEvery-time.Millisecond))
			buffered += 1200
		}
		g.allow(1200, buffered, base.Add(gateSampleEvery))
	}
	assert.Zero(t, g.rate)
	assert.Zero(t, g.dropped)
}

// A busy channel whose backlog dips below the floor without ever emptying is still saturated: the
// gate must learn its rate rather than hold it at the start cap.
func TestSendGate_LearnsTheRateFromALowButNonemptyBacklog(t *testing.T) {
	g := newSendGate(0)
	now := time.Now()
	// Starts below the floor, but not empty.
	buffered := uint64(8_000)
	// Drains 1KB/ms; fed 36KB every 40ms plus 100 bytes every millisecond, so it swings between
	// about 8KB and 44KB and never empties.
	for ms := range 2000 {
		if ms%40 == 0 {
			g.allow(36_000, buffered, now)
			buffered += 36_000
		}
		g.allow(100, buffered, now)
		buffered += 100
		buffered -= min(buffered, 1000)
		now = now.Add(time.Millisecond)
	}
	assert.Positive(t, buffered)
	assert.InDelta(t, 1e6, g.rate, 2e5)
}
