package clientcore

import "time"

// sendGate keeps a data channel's send buffer from growing into a queue. A data channel carries
// QUIC packets, and pion's SCTP stream buffers whatever is sent faster than it can drain. Nothing
// is lost, so QUIC's congestion control never backs off and the buffer stays full: every packet,
// including a page's small requests, then waits behind a bulk download's backlog. Measured
// through a producer, one download put about 300ms of queue in front of every request.
//
// So the gate drops a packet once the buffered data would take more than queueTarget beyond the
// path's round trip to drain. QUIC treats the drop as loss and slows down, which holds the queue
// near queueTarget. Buffered data includes data in flight awaiting acknowledgement, which takes
// a round trip to clear even with no queue, hence the round trip in the allowance. The drain rate
// is measured from what the buffer releases.
type sendGate struct {
	// queueTarget is how much queuing delay, above the round trip, the gate allows.
	queueTarget time.Duration
	// rtt is the data channel's round trip, or zero while unknown.
	rtt time.Duration

	sent       uint64
	lastSent   uint64
	lastBuf    uint64
	lastSample time.Time
	rate       float64 // drained bytes per second, smoothed
	// starved is whether the buffer ran empty at any point of the current interval, which makes
	// the interval say nothing about the channel's capacity. A buffer that only runs low still
	// has a backlog, so the channel was busy throughout.
	starved bool
	dropped uint64
}

const (
	defaultQueueTarget = 20 * time.Millisecond
	// gateFloor is buffered data the gate never drops below, so a slow trickle is never cut.
	gateFloor = 16 << 10
	// gateStartCap bounds the buffer until a drain rate is measured.
	gateStartCap = 256 << 10
	// gateFallbackRTT stands in for the round trip where it can't be measured: a browser's
	// WebRTC stack doesn't report SCTP's. Long enough not to cut a distant peer's throughput.
	gateFallbackRTT = 100 * time.Millisecond
	// gateRTTEvery is how often a data channel's send loop refreshes its gate's round trip.
	gateRTTEvery = 250 * time.Millisecond
	// gateSampleEvery is how often the drain rate is measured.
	gateSampleEvery   = 50 * time.Millisecond
	gateRateSmoothing = 0.25
)

func newSendGate(queueTarget time.Duration) *sendGate {
	if queueTarget <= 0 {
		queueTarget = defaultQueueTarget
	}
	return &sendGate{queueTarget: queueTarget}
}

// setRTT records the data channel's measured round trip.
func (g *sendGate) setRTT(rtt time.Duration) {
	if rtt > 0 {
		g.rtt = rtt
	}
}

// allow reports whether a packet of n bytes may be sent, given the data channel's buffered
// amount now. It records the packet as sent when it allows it.
func (g *sendGate) allow(n int, buffered uint64, now time.Time) bool {
	g.sample(buffered, now)
	ok := g.admits(buffered)
	if ok {
		g.sent += uint64(n)
	} else {
		g.dropped++
	}
	return ok
}

func (g *sendGate) admits(buffered uint64) bool {
	if buffered <= gateFloor {
		return true
	}
	if g.rate <= 0 {
		return buffered <= gateStartCap
	}
	rtt := g.rtt
	if rtt <= 0 {
		rtt = gateFallbackRTT
	}
	drain := time.Duration(float64(buffered) / g.rate * float64(time.Second))
	return drain <= rtt+g.queueTarget
}

// sample updates the drain rate. The buffer drains by what was sent since the last sample less
// what it grew by. That is the channel's capacity only while it had a backlog throughout: with
// nothing buffered at any point, it drains whatever the application offered, and
// learning that as the rate would make the gate drop the next burst on an idle, fast path. Every
// send reports the buffered amount, so an interval is judged on all of them, not its ends.
func (g *sendGate) sample(buffered uint64, now time.Time) {
	if buffered == 0 {
		g.starved = true
	}
	if g.lastSample.IsZero() {
		g.lastSample, g.lastBuf, g.lastSent = now, buffered, g.sent
		return
	}
	elapsed := now.Sub(g.lastSample)
	if elapsed < gateSampleEvery {
		return
	}
	drained := int64(g.sent-g.lastSent) - (int64(buffered) - int64(g.lastBuf))
	if drained > 0 && !g.starved {
		r := float64(drained) / elapsed.Seconds()
		if g.rate <= 0 {
			g.rate = r
		} else {
			g.rate += gateRateSmoothing * (r - g.rate)
		}
	}
	g.lastSample, g.lastBuf, g.lastSent = now, buffered, g.sent
	g.starved = buffered == 0
}
