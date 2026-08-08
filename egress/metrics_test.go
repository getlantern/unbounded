package egress

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// resetMetricsState puts the package-global refcount back so one test's listeners
// cannot leak into the next. The real initMetrics is never called here — these tests
// exercise the refcounting, and standing up an OTLP exporter would make them depend
// on a collector being reachable.
func withStubbedMetrics(t *testing.T) *stubMetrics {
	t.Helper()
	stub := &stubMetrics{}

	realInit := initMetricsFn
	initMetricsFn = stub.init

	metricsMu.Lock()
	prevRefs, prevShutdown := metricsRefs, metricsShutdown
	metricsRefs, metricsShutdown = 0, nil
	metricsMu.Unlock()

	t.Cleanup(func() {
		initMetricsFn = realInit
		metricsMu.Lock()
		metricsRefs, metricsShutdown = prevRefs, prevShutdown
		metricsMu.Unlock()
	})
	return stub
}

type stubMetrics struct {
	mu        sync.Mutex
	inits     int
	shutdowns int
	initErr   error
}

func (s *stubMetrics) init(context.Context) (func(context.Context) error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initErr != nil {
		return nil, s.initErr
	}
	s.inits++
	return func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.shutdowns++
		return nil
	}, nil
}

func (s *stubMetrics) counts() (inits, shutdowns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inits, s.shutdowns
}

// The bug this whole change exists for. Registering per listener meant a second
// NewListener built a second meter provider, overwrote the shared instrument
// variables, and left the first provider exporting duplicates of every series it
// could still resolve.
func TestStartMetrics_InitializesOncePerProcess(t *testing.T) {
	stub := withStubbedMetrics(t)

	stop1, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stop2, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if inits, _ := stub.counts(); inits != 1 {
		t.Errorf("initialized %d times for 2 listeners, want 1", inits)
	}

	// And the first listener closing must not take telemetry away from the second,
	// which is still serving. That was the mirror-image half of the same bug.
	if err := stop1(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, shutdowns := stub.counts(); shutdowns != 0 {
		t.Errorf("shut down while a listener was still open (%d times)", shutdowns)
	}

	if err := stop2(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, shutdowns := stub.counts(); shutdowns != 1 {
		t.Errorf("shut down %d times after the last listener closed, want 1", shutdowns)
	}
}

// proxyListener.Close is not guaranteed to be called exactly once, and a double
// close must not drive the refcount negative — which would make the *next*
// startMetrics skip initialization and hand out a working-looking handle to a
// provider that was never created.
func TestStartMetrics_ReleaseIsIdempotent(t *testing.T) {
	stub := withStubbedMetrics(t)

	stop, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := stop(context.Background()); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	if _, shutdowns := stub.counts(); shutdowns != 1 {
		t.Errorf("shut down %d times for 3 closes, want 1", shutdowns)
	}

	metricsMu.Lock()
	refs := metricsRefs
	metricsMu.Unlock()
	if refs != 0 {
		t.Errorf("refcount = %d after repeated closes, want 0", refs)
	}
}

// Dropping to zero and starting again is the create/close/create cycle every test
// binary performs. It must produce a live provider, not reuse the shut-down one —
// this is why the state is a mutex and a counter rather than a sync.Once.
func TestStartMetrics_ReinitializesAfterFullRelease(t *testing.T) {
	stub := withStubbedMetrics(t)

	stop, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	stop2, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop2(context.Background())

	if inits, _ := stub.counts(); inits != 2 {
		t.Errorf("initialized %d times across a full release cycle, want 2", inits)
	}
}

// A failed setup must not be recorded as a live reference. If it were, the next
// caller would see a non-zero refcount, skip initialization entirely, and run with
// no instruments at all.
func TestStartMetrics_FailedInitLeavesNoReference(t *testing.T) {
	stub := withStubbedMetrics(t)
	sentinel := errors.New("exporter unavailable")
	stub.initErr = sentinel

	if _, err := startMetrics(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}

	metricsMu.Lock()
	refs := metricsRefs
	metricsMu.Unlock()
	if refs != 0 {
		t.Fatalf("refcount = %d after a failed init, want 0", refs)
	}

	// A later attempt, once the exporter is reachable, must actually initialize.
	stub.initErr = nil
	stop, err := startMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	if inits, _ := stub.counts(); inits != 1 {
		t.Errorf("initialized %d times after recovering, want 1", inits)
	}
}

// Listeners are created and closed from independent goroutines in tests and in any
// host embedding more than one egress. The refcount is the only thing standing
// between that and a torn-down provider under a live listener.
func TestStartMetrics_ConcurrentStartStop(t *testing.T) {
	stub := withStubbedMetrics(t)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop, err := startMetrics(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			stop(context.Background())
		}()
	}
	wg.Wait()

	metricsMu.Lock()
	refs := metricsRefs
	metricsMu.Unlock()
	if refs != 0 {
		t.Errorf("refcount = %d after %d balanced start/stop pairs, want 0", refs, n)
	}

	// Every init must be matched by exactly one shutdown. Interleaving decides how
	// many cycles happen, so the counts are compared to each other rather than to a
	// fixed number.
	inits, shutdowns := stub.counts()
	if inits != shutdowns {
		t.Errorf("%d inits vs %d shutdowns — a provider was leaked or double-closed", inits, shutdowns)
	}
	if inits == 0 {
		t.Error("never initialized")
	}
}
