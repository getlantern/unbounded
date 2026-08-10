package clientcore

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Teardown tests for the worker/engine stop path. A worker that cannot be
// cancelled while blocked on a control-plane send strands its wg.Done(), which
// blocks BroflakeEngine.stop() and retains the bus, both routers and every other
// worker for the process lifetime — so each re-create of an outbound built on
// broflake accumulates a whole stack.
//
// These use synthetic FSM states rather than a real NewBroflake so nothing touches
// STUN, freddie or an egress server.

// sendCtxState models a control-plane send written the way the states must write
// it: abandonable via ctx.
func sendCtxState(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
	if !sendCtx(ctx, com.tx, IPCMsg{IpcType: PathAssertionIPC}) {
		return 0, input
	}
	return 0, nil
}

// wedgedState blocks unabandonably, standing in for any future unguarded blocking
// operation. Used only to prove stop() stays bounded regardless.
func wedgedState(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
	com.tx <- IPCMsg{IpcType: PathAssertionIPC}
	return 0, nil
}

// fillTx saturates the worker's tx buffer so the next send on it blocks — the
// state a worker is left in when its drain is torn down before the engine stops.
func fillTx(t *testing.T, fsm *WorkerFSM) {
	t.Helper()
	for i := 0; i < cap(fsm.com.tx); i++ {
		select {
		case fsm.com.tx <- IPCMsg{IpcType: ChunkIPC}:
		default:
			t.Fatalf("tx buffer full after %d of %d sends", i, cap(fsm.com.tx))
		}
	}
}

func waitWg(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestNewWorkerFSMInitializesCtx is the WorkerFSM counterpart of
// TestQUICLayerCloseBeforeListen: ctx/cancel must exist before Start()'s goroutine
// runs, so Stop() cannot race the write or read a nil cancel.
func TestNewWorkerFSMInitializesCtx(t *testing.T) {
	fsm := NewWorkerFSM(&sync.WaitGroup{}, []FSMstate{sendCtxState})
	if fsm.ctx == nil || fsm.cancel == nil {
		t.Fatal("NewWorkerFSM must initialize ctx/cancel before Start()'s goroutine can run")
	}
}

// TestWorkerFSMStopBeforeStart covers Stop() arriving before Start(), e.g. a
// connect torn down immediately. Previously this called a nil CancelFunc.
func TestWorkerFSMStopBeforeStart(t *testing.T) {
	fsm := NewWorkerFSM(&sync.WaitGroup{}, []FSMstate{sendCtxState})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop() before Start() panicked: %v", r)
		}
	}()
	fsm.Stop()

	if fsm.ctx.Err() == nil {
		t.Fatal("Stop() before Start() did not cancel the context")
	}
}

// TestSendCtxAbandonsOnCancel is the unit-level contract: a send into a full
// channel must give up once ctx is cancelled.
func TestSendCtxAbandonsOnCancel(t *testing.T) {
	ch := make(chan IPCMsg, 1)
	ch <- IPCMsg{} // full

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan bool, 1)
	go func() { done <- sendCtx(ctx, ch, IPCMsg{}) }()
	select {
	case sent := <-done:
		if sent {
			t.Fatal("sendCtx reported a send into a full channel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sendCtx did not abandon the send after ctx was cancelled")
	}
}

// TestWorkerFSMStopReleasesWorkerOnUndrainedSend is the regression test for the
// leak: a worker parked in a control-plane send with nothing draining tx must
// still exit when Stop() cancels it.
func TestWorkerFSMStopReleasesWorkerOnUndrainedSend(t *testing.T) {
	var wg sync.WaitGroup
	fsm := NewWorkerFSM(&wg, []FSMstate{sendCtxState})
	fillTx(t, fsm)

	fsm.Start()
	time.Sleep(100 * time.Millisecond) // let the goroutine reach the blocked send
	fsm.Stop()

	if !waitWg(&wg, 3*time.Second) {
		t.Fatal("worker did not exit after Stop() while blocked on a control-plane send; " +
			"its wg.Done() never runs, so BroflakeEngine.stop()'s wait cannot complete")
	}
}

// TestBroflakeEngineStopCancelsCtxDespiteWedgedWorker is the backstop: even a
// worker that cannot be cancelled must not hold the engine ctx, because that ctx
// keeps the bus and both routers alive.
func TestBroflakeEngineStopCancelsCtxDespiteWedgedWorker(t *testing.T) {
	var wg sync.WaitGroup
	wedged := NewWorkerFSM(&wg, []FSMstate{wedgedState})
	fillTx(t, wedged)

	ui := &UIImpl{}
	engine := NewBroflakeEngine(
		NewWorkerTable([]WorkerFSM{*wedged}),
		NewWorkerTable([]WorkerFSM{}),
		ui, &wg, "", "test",
	)
	engine.stopGrace = 200 * time.Millisecond
	ui.Init(engine)

	engine.start()
	time.Sleep(100 * time.Millisecond)
	engine.stop()

	select {
	case <-engine.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("stop() never cancelled the engine ctx: an unbounded wait on a wedged worker " +
			"retains the bus, routers and workers for the process lifetime")
	}
}

// TestBroflakeEngineStopDoesNotLeakGoroutines is the end-to-end cost check. A
// client re-creating an outbound every few minutes accumulates linearly on
// anything retained per cycle, which presents as CPU rising with uptime while disk
// and network stay flat.
func TestBroflakeEngineStopDoesNotLeakGoroutines(t *testing.T) {
	const cycles = 10
	base := runtime.NumGoroutine()

	for i := 0; i < cycles; i++ {
		var wg sync.WaitGroup
		fsm := NewWorkerFSM(&wg, []FSMstate{sendCtxState})
		fillTx(t, fsm)

		engine := NewBroflakeEngine(
			NewWorkerTable([]WorkerFSM{*fsm}),
			NewWorkerTable([]WorkerFSM{}),
			&UIImpl{}, &wg, "", "test",
		)
		engine.stopGrace = 200 * time.Millisecond
		engine.start()
		time.Sleep(20 * time.Millisecond)
		engine.stop()
	}

	time.Sleep(500 * time.Millisecond)
	leaked := runtime.NumGoroutine() - base
	t.Logf("goroutines: base=%d after %d create/stop cycles=%d (leaked=%d)",
		base, cycles, runtime.NumGoroutine(), leaked)
	if leaked >= cycles {
		t.Fatalf("leaked %d goroutines across %d create/stop cycles: each re-create retains a stack",
			leaked, cycles)
	}
}

// TestNoBareControlPlaneSends stops the pattern from being reintroduced across the
// package. A bare `com.tx <- msg` statement is unabandonable; control-plane sends
// must go through sendCtx. Data-plane sends use a non-blocking select with a
// default, which appears as `case com.tx <- ...` and so does not match here.
func TestNoBareControlPlaneSends(t *testing.T) {
	bare := regexp.MustCompile(`^\s*com\.tx <- `)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d: bare `com.tx <-` send; use sendCtx so Stop() can cancel it:\n\t%s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
