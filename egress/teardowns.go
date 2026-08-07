package egress

import (
	"sync"
	"sync/atomic"
)

// Session teardown reasons existed only as a span attribute, and session spans are
// sampled at 1% (OTEL_TRACES_SAMPLER_ARG=0.01 in the egress unit). That makes them
// unusable for counting: over 24 hours the whole fleet produced six sampled session
// spans, and reading a ratio off six events is how "5 of 6 are keepalive_timeout"
// became an 83% figure that a 7-day window corrected to ~45%.
//
// A detector for wedged donors has to count rare events, so it needs an unsampled
// series. This mirrors refused-websockets: a monotonic tally per reason, observed by
// the otel callback, unaffected by trace sampling.
//
// What it cannot do on its own is separate a frozen page from a sleeping laptop.
// keepalive_timeout means "the donor stopped answering pings", which covers a real
// freeze, a suspended machine, and a tab closed without a clean close. Distinguishing
// them needs the page-side watchdog (ui/src/utils/freezeWatchdog.ts), whose reports
// currently have nowhere to go. This is half of that pair, and the half that is
// cheap.

// teardownReason is why a WebSocket session ended. A distinct type rather than a bare
// string for the same reason refusalReason is: these become metric label content, and
// a label built from request-derived data is unbounded cardinality chosen by a peer.
type teardownReason string

const (
	// teardownWebSocketClosed is the default: the session ended without any more
	// specific cause being recorded. It is not necessarily clean — a donor that
	// vanished without a close handshake also lands here — it only means nothing
	// else claimed the teardown.
	teardownWebSocketClosed teardownReason = "websocket_closed"
	// teardownKeepaliveTimeout: the donor stopped answering keepalive pings. The
	// closest server-side signature of a wedged or frozen donor, and the reason this
	// metric exists. Outranks the default because an unanswered keepalive is the only
	// thing that distinguishes a wedged peer from a merely disconnected one.
	teardownKeepaliveTimeout teardownReason = "keepalive_timeout"
	teardownAcceptFailed     teardownReason = "websocket_accept_failed"
	teardownPeerAddrBad      teardownReason = "peer_addr_unresolvable"
	teardownMigrateFailed    teardownReason = "create_or_migrate_failed"
)

// labeledTally is a monotonic count keyed by a small fixed label set.
//
// Extracted rather than copied: refusals already needed exactly this, teardowns are
// the second, and a third near-identical mutex-plus-map would be the point at which
// someone changes one and not the others. The typed wrappers around it keep the
// call-site guarantee that a label cannot be built from request data by accident.
type labeledTally struct {
	mx sync.Mutex
	// counts is never pruned. The label set is small and fixed, and a reason that
	// stops occurring should keep reporting its running total rather than vanishing
	// from the series — a series that disappears reads as "no data" rather than "no
	// longer happening".
	counts map[string]*int64
}

func newLabeledTally() *labeledTally {
	return &labeledTally{counts: map[string]*int64{}}
}

// add increments the count for one label.
func (t *labeledTally) add(label string) {
	t.mx.Lock()
	c, ok := t.counts[label]
	if !ok {
		c = new(int64)
		t.counts[label] = c
	}
	t.mx.Unlock()
	// Outside the lock: the map entry is stable once created, so only the pointer
	// lookup needs synchronizing.
	atomic.AddInt64(c, 1)
}

// each reports every label seen so far. Snapshots the pointers under the lock so an
// otel callback never holds it while observing.
func (t *labeledTally) each(f func(label string, count int64)) {
	type row struct {
		label string
		c     *int64
	}
	t.mx.Lock()
	rows := make([]row, 0, len(t.counts))
	for label, c := range t.counts {
		rows = append(rows, row{label, c})
	}
	t.mx.Unlock()

	for _, r := range rows {
		f(r.label, atomic.LoadInt64(r.c))
	}
}

var teardowns = newLabeledTally()

// recordTeardown counts one ended session. Monotonic, so consumers rate() it.
func recordTeardown(reason teardownReason) {
	teardowns.add(string(reason))
}

// eachTeardown reports every teardown reason seen so far.
func eachTeardown(f func(reason teardownReason, count int64)) {
	teardowns.each(func(label string, count int64) {
		f(teardownReason(label), count)
	})
}
