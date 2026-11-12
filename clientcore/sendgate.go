package clientcore

import "time"

// sendGate keeps a data channel's send buffer from growing into a queue. A data channel carries
// QUIC packets, and pion's SCTP stream buffers whatever is sent faster than it can drain. Nothing
// is lost, so QUIC's congestion control never backs off and the buffer stays full: every packet,
// including a page's small requests, then waits behind a bulk download's backlog. Measured
// through a producer, one download put about 300ms of queue in front of every request.
//
// So the gate drops a packet that would wait more than a round trip plus queueTarget before it is
// acknowledged. QUIC treats the drop as loss and slows down, which holds the queue near
// queueTarget.
//
// pion's buffered amount counts bytes until they are acknowledged, so it mixes a queue with data
// in flight. Two signals separate them:
//   - Whether a queue stands at all. pion releases buffered bytes in order, so the oldest is at
//     offset sent-buffered of what the gate let through, and its age is a round trip with no
//     queue. Paced or bursty traffic that never builds a queue never ages past that, whatever
//     its rate, so it is never cut.
//   - How long the queue takes to drain. While a queue stands, bytes are released at the
//     channel's capacity, so only then is the rate measured. A packet sent now would be
//     acknowledged once everything buffered ahead of it is, after buffered/rate.
//
// Until a rate is measured, the oldest byte's age is the measure. It lags the queue, which grew
// while that byte waited, but only for the first window of a queue.
//
// Both signals need the round trip, so the gate stays open until it is measured. A browser's
// WebRTC stack doesn't report SCTP's, so in the widget the gate never closes: a guess would
// misjudge a distant peer's data in flight for a queue.
type sendGate struct {
	// queueTarget is how much queuing delay, above the round trip, the gate allows.
	queueTarget time.Duration
	// rtt is the data channel's round trip, or zero while unknown, which leaves the gate open.
	rtt time.Duration

	// sent is the bytes the gate has let through, and marks when each of them was sent: marks[i]
	// covers the bytes up to its end offset. marks[first:] are those not yet known to be released.
	// head is the offset the channel has released up to.
	sent    uint64
	head    uint64
	marks   []sendMark
	first   int
	dropped uint64

	// rate is the channel's drain rate in bytes per second, measured while a queue stood and
	// smoothed, as of rateAt. A window starting at winStart has released winReleased so far, and
	// lastSeen is when the gate last saw the buffer.
	rate        float64
	rateAt      time.Time
	winStart    time.Time
	winReleased uint64
	lastSeen    time.Time
}

type sendMark struct {
	end uint64
	at  time.Time
}

const (
	defaultQueueTarget = 20 * time.Millisecond
	// gateRTTSlack scales the round trip before an older byte counts as queued: SCTP's smoothed
	// round trip, refreshed every gateRTTEvery, can trail the path's.
	gateRTTSlack = 1.25
	// gateMaxGap is the longest the gate may go without seeing the buffer within a rate window. A
	// saturated channel is offered packets far more often; a longer gap means the queue may have
	// cleared unseen, so the window would measure what the application sent.
	gateMaxGap = 20 * time.Millisecond
	// gateRTTEvery is how often a data channel's send loop refreshes its gate's round trip.
	gateRTTEvery = 250 * time.Millisecond
	// gateRateWindow is how long the drain rate is measured over, and gateRateStale how long a
	// measurement is trusted once queues stop: the channel's capacity can change while idle.
	gateRateWindow    = 50 * time.Millisecond
	gateRateStale     = time.Second
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
	released := g.release(buffered)
	age, queuing := g.queue(buffered, now)
	g.measure(released, queuing, now)
	g.lastSeen = now
	if !g.admits(buffered, age, queuing, now) {
		g.dropped++
		return false
	}
	g.sent += uint64(n)
	g.marks = append(g.marks, sendMark{end: g.sent, at: now})
	return true
}

// queue returns the oldest buffered byte's age, and whether a queue stands: whether that byte
// has waited longer than a round trip would take with none, allowing for the round trip trailing
// the path's, and by more than a quarter of the target so that a burst's own serialization
// doesn't count.
func (g *sendGate) queue(buffered uint64, now time.Time) (time.Duration, bool) {
	if g.rtt <= 0 || buffered == 0 || g.first == len(g.marks) {
		return 0, false
	}
	age := now.Sub(g.marks[g.first].at)
	slack := time.Duration(float64(g.rtt) * gateRTTSlack)
	return age, age > slack+g.queueTarget/4
}

// measure updates the drain rate from the bytes released while a queue stands. A window in which
// the queue cleared measures nothing, since the channel drained only what it was given.
func (g *sendGate) measure(released uint64, queuing bool, now time.Time) {
	if !queuing || (!g.winStart.IsZero() && now.Sub(g.lastSeen) > gateMaxGap) {
		g.winStart = time.Time{}
		if !queuing {
			return
		}
	}
	if g.winStart.IsZero() {
		g.winStart, g.winReleased = now, 0
		return
	}
	g.winReleased += released
	elapsed := now.Sub(g.winStart)
	if elapsed < gateRateWindow {
		return
	}
	if g.winReleased > 0 {
		r := float64(g.winReleased) / elapsed.Seconds()
		if g.rate <= 0 || now.Sub(g.rateAt) > gateRateStale {
			g.rate = r
		} else {
			g.rate += gateRateSmoothing * (r - g.rate)
		}
		g.rateAt = now
	}
	g.winStart, g.winReleased = now, 0
}

func (g *sendGate) admits(buffered uint64, age time.Duration, queuing bool, now time.Time) bool {
	if !queuing {
		return true
	}
	limit := g.rtt + g.queueTarget
	if g.rate > 0 && now.Sub(g.rateAt) <= gateRateStale {
		return time.Duration(float64(buffered)/g.rate*float64(time.Second)) <= limit
	}
	return age <= limit
}

// release forgets the marks of bytes the channel no longer buffers, leaving marks[first] as the
// send that holds the oldest buffered byte, and returns how many bytes were released since the
// last call.
//
// pion also releases bytes that are selectively acknowledged past a lost chunk, so while a hole
// is outstanding sent-buffered runs ahead of the oldest byte, and the gate takes a younger byte
// for the oldest. That errs towards admitting: the gate sees less queue, never more. The hole
// lasts until the lost chunk is abandoned, as this unretransmitted channel does promptly, and a
// QUIC sender is backing off for that loss anyway. The drain prediction, buffered/rate, uses
// pion's exact buffered total and is unaffected.
func (g *sendGate) release(buffered uint64) uint64 {
	head := g.sent - min(buffered, g.sent)
	released := head - min(g.head, head)
	g.head = max(g.head, head)
	for g.first < len(g.marks) && g.marks[g.first].end <= head {
		g.first++
	}
	// Reclaim the released marks' space once they are most of the slice.
	if g.first > 64 && g.first*2 > len(g.marks) {
		n := copy(g.marks, g.marks[g.first:])
		g.marks = g.marks[:n]
		g.first = 0
	}
	return released
}
