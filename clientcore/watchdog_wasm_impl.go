//go:build wasm

// watchdog_wasm_impl.go exposes a Go-side liveness heartbeat to JavaScript so the
// page can tell a wedged Go runtime apart from a wedged browser main thread.
//
// Why this is needed at all: a frozen widget is only observable today as an
// *absence*. The netstate vertex ages out after 5 minutes, and a donor that
// stopped answering looks exactly like a user who closed the tab. Both freezes
// seen on 2026-08-03 were invisible in telemetry for that reason, and they were
// not even the same failure:
//
//   - the first took the netstate vertex and all four of its edges away, i.e. the
//     engine itself stopped
//   - the second left the vertex current and all four edges intact while the page
//     was visually frozen, i.e. the engine was fine and the main thread was not
//
// Those two need different fixes, so the instrumentation has to distinguish them.
// Reading this heartbeat from a JS timer does exactly that, because Go/wasm and JS
// share one thread but fail independently:
//
//	JS timer fires, ticks advancing    -> both healthy
//	JS timer fires, ticks frozen       -> Go scheduler wedged (deadlock, or a
//	                                      goroutine looping without yielding)
//	JS timer does not fire at all      -> main thread blocked; nothing Go-side can
//	                                      report it, which is why the JS side owns
//	                                      the detection and this only supplies the
//	                                      evidence
//
// Note the third case cannot be detected from here by construction: if the thread
// is blocked, this goroutine is not running either. That asymmetry is the whole
// reason the watchdog lives in JS and reads Go, rather than Go reporting on itself.
//
// Deliberately not OpenTelemetry: the otel Go SDK was found to abuse the call
// stack in ways mobile Safari does not tolerate, so widget-side telemetry has to
// stay this primitive.
package clientcore

import (
	"sync/atomic"
	"syscall/js"
	"time"
)

// heartbeatInterval is how often the Go side bumps its tick counter. One second
// is short enough that a JS watchdog polling every few seconds can tell a stalled
// counter from a merely slow one, and long enough to be free: this is one atomic
// store and one Date.now() per second.
const heartbeatInterval = time.Second

// watchdog carries the Go-side liveness counters. Values are read from JS on an
// arbitrary turn of the event loop, so every field is atomic.
type watchdog struct {
	// ticks increments once per heartbeatInterval. JS compares successive reads:
	// a delta of zero across a wall-clock gap much larger than the interval means
	// the Go scheduler is not running.
	ticks atomic.Uint64
	// lastTickMs is the JS-epoch milliseconds of the most recent tick. Exposed so
	// the page can compute staleness directly rather than having to remember a
	// previous sample, which matters for a beacon fired during pagehide when
	// there may be no time left to sample twice.
	lastTickMs atomic.Int64
	// startedMs is when the heartbeat began, so a reader can tell "never ticked"
	// apart from "ticked and then stopped".
	startedMs atomic.Int64
}

// nowMs returns JS-epoch milliseconds. Deliberately Date.now() rather than Go's
// time.Now(): the consumer of these values is JS comparing them against its own
// Date.now(), and mixing clocks would make staleness arithmetic wrong under any
// clock skew the wasm runtime introduces.
func nowMs() int64 {
	return int64(js.Global().Get("Date").Call("now").Float())
}

// start begins the heartbeat. It runs for the lifetime of the page rather than
// being tied to Start/Stop on purpose: the question it answers is "is the Go
// runtime still alive", which is exactly the question you need answered while the
// engine is stopped or wedged. A stopped engine must still report liveness, or a
// deliberate stop would be indistinguishable from a freeze — the same conflation
// this file exists to remove.
func (w *watchdog) start() {
	now := nowMs()
	w.startedMs.Store(now)
	w.lastTickMs.Store(now)

	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for range t.C {
			w.ticks.Add(1)
			w.lastTickMs.Store(nowMs())
		}
	}()
}

// snapshot returns the counters as a JS object. Field names are stable API: the
// page-side watchdog reads them, so renaming one breaks freeze detection silently.
func (w *watchdog) snapshot() map[string]interface{} {
	return map[string]interface{}{
		"goTicks":      float64(w.ticks.Load()),
		"goLastTickMs": float64(w.lastTickMs.Load()),
		"goStartedMs":  float64(w.startedMs.Load()),
		"goIntervalMs": float64(heartbeatInterval.Milliseconds()),
		// Echo the reader's own clock from inside Go. If this disagrees with the
		// caller's Date.now() by more than a moment, the call itself was queued
		// behind a blocked event loop — which is itself a freeze signal, and one
		// no counter delta would reveal.
		"goNowMs": float64(nowMs()),
	}
}

// installWatchdog starts the heartbeat and exposes it as `liveness()` on the
// Broflake JS API object. Returns a snapshot object; see snapshot for the fields.
func installWatchdog(id string) {
	w := &watchdog{}
	w.start()

	js.Global().Get(id).Set(
		"liveness",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			return js.ValueOf(w.snapshot())
		}),
	)
}
