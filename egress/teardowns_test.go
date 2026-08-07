package egress

import (
	"sync"
	"testing"
)

func resetTeardowns(t *testing.T) {
	t.Helper()
	teardowns = newLabeledTally()
}

func collectTeardowns() map[teardownReason]int64 {
	out := map[teardownReason]int64{}
	eachTeardown(func(reason teardownReason, count int64) { out[reason] = count })
	return out
}

// The whole reason this metric exists: teardown reasons lived only on spans sampled
// at 1%, so counting rare events off them was statistically blind. A tally must be
// monotonic and must not be drained by observation, or every rate() would be wrong.
func TestRecordTeardown_IsMonotonicAcrossObservations(t *testing.T) {
	resetTeardowns(t)
	for i := 0; i < 3; i++ {
		recordTeardown(teardownKeepaliveTimeout)
	}
	if got := collectTeardowns()[teardownKeepaliveTimeout]; got != 3 {
		t.Fatalf("after 3: got %d, want 3", got)
	}
	if got := collectTeardowns()[teardownKeepaliveTimeout]; got != 3 {
		t.Fatalf("observing drained the tally: got %d, want 3", got)
	}
	recordTeardown(teardownKeepaliveTimeout)
	if got := collectTeardowns()[teardownKeepaliveTimeout]; got != 4 {
		t.Fatalf("after a 4th: got %d, want 4", got)
	}
}

// keepalive_timeout is the reason a detector watches, so it must never be merged
// with the generic default — that distinction is the entire signal.
func TestRecordTeardown_SeparatesReasons(t *testing.T) {
	resetTeardowns(t)
	recordTeardown(teardownWebSocketClosed)
	recordTeardown(teardownKeepaliveTimeout)
	recordTeardown(teardownKeepaliveTimeout)
	recordTeardown(teardownMigrateFailed)

	got := collectTeardowns()
	for reason, want := range map[teardownReason]int64{
		teardownWebSocketClosed:  1,
		teardownKeepaliveTimeout: 2,
		teardownMigrateFailed:    1,
	} {
		if got[reason] != want {
			t.Errorf("%s = %d, want %d", reason, got[reason], want)
		}
	}
	if len(got) != 3 {
		t.Errorf("reported %d reasons, want 3: %v", len(got), got)
	}
}

// recordTeardown takes a teardownReason, not a string, so request-derived data
// cannot become a metric label without an explicit conversion a reviewer would see.
// This compiles only while that holds.
func TestRecordTeardown_TakesTypedReason(t *testing.T) {
	var f func(teardownReason) = recordTeardown
	_ = f
}

// Sessions end concurrently, and the otel callback observes while they do.
func TestRecordTeardown_NoLostCountsUnderConcurrency(t *testing.T) {
	resetTeardowns(t)
	const writers, per = 8, 500

	stop := make(chan struct{})
	var obsWg sync.WaitGroup
	obsWg.Add(1)
	go func() {
		defer obsWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				collectTeardowns()
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				recordTeardown(teardownKeepaliveTimeout)
			}
		}()
	}
	wg.Wait()
	close(stop)
	obsWg.Wait()

	if got := collectTeardowns()[teardownKeepaliveTimeout]; got != writers*per {
		t.Fatalf("counted %d, want %d", got, writers*per)
	}
}

// The tally is now shared by refusals and teardowns, so a bug in it would corrupt
// both. Pins that the two do not see each other's counts.
func TestLabeledTally_IsolatesInstances(t *testing.T) {
	resetTeardowns(t)
	resetRefusals(t)

	recordTeardown(teardownKeepaliveTimeout)
	recordRefusal(refusedLegacyTeamClient)

	if got := collectTeardowns()[teardownKeepaliveTimeout]; got != 1 {
		t.Errorf("teardown count = %d, want 1", got)
	}
	if got := collectRefusals()[refusedLegacyTeamClient]; got != 1 {
		t.Errorf("refusal count = %d, want 1", got)
	}
	// A label used in one tally must not appear in the other.
	if _, crossed := collectTeardowns()[teardownReason(refusedLegacyTeamClient)]; crossed {
		t.Error("a refusal label leaked into the teardown tally")
	}
}
