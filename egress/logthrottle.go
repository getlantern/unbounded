package egress

import (
	"sync"
	"time"
)

// logThrottle bounds how often a given key may emit a log line, and counts what it
// suppressed in between.
//
// Extracted from the refusal-specific version because freeze reports need exactly the
// same thing, and a second copy is where the two drift apart. Same reasoning as
// labeledTally: shared storage, typed wrappers at the call sites.
//
// The problem it solves is worth restating, since the shape recurs. A high-rate event
// wants opposite things from a counter and from a log: the counter must see every
// occurrence, and the log must not, or it drowns everything else. Refusal lines were
// 96% of the egress journal — 5,326 of 5,551 entries over ten minutes — which is not
// merely untidy: three separate greps for an unrelated startup line came back empty
// because the signal was buried, and I concluded a deploy had not taken effect when it
// had.
type logThrottle struct {
	interval time.Duration

	mx    sync.Mutex
	state map[string]*throttleState
}

type throttleState struct {
	lastLogged time.Time
	suppressed int64
}

func newLogThrottle(interval time.Duration) *logThrottle {
	return &logThrottle{interval: interval, state: map[string]*throttleState{}}
}

// allow reports whether this key should be logged now, and how many occurrences went
// unlogged since the last one that was.
//
// The first occurrence of a key always logs. A condition nobody has seen before is
// exactly the one nobody is watching a dashboard for, so deferring it by up to an
// interval is the wrong default — the throttle should quiet a known flood, not delay
// news.
//
// now is a parameter rather than read inside so behavior is testable without sleeping.
func (t *logThrottle) allow(key string, now time.Time) (shouldLog bool, suppressed int64) {
	t.mx.Lock()
	defer t.mx.Unlock()

	st, seen := t.state[key]
	if !seen {
		t.state[key] = &throttleState{lastLogged: now}
		return true, 0
	}
	if now.Sub(st.lastLogged) < t.interval {
		st.suppressed++
		return false, 0
	}

	// Report the backlog on the line that breaks the silence, so one entry says how
	// much it stands for. Without it a throttled line understates a flood by three
	// orders of magnitude and reads like an isolated event — actively misleading
	// rather than merely quiet.
	suppressed = st.suppressed
	st.suppressed = 0
	st.lastLogged = now
	return true, suppressed
}
