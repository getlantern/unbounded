package common

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestDebug_DefaultRoutesToSlog locks in the load-bearing behavior of the
// slog migration: with no explicit setter call, Debugf must reach
// slog.Default(). Lantern's structured logger is installed via
// slog.SetDefault() in radiance, and that's the path that gets broflake's
// internals into lantern.log. If this test starts failing, every
// production client's broflake debug output is silently disappearing.
func TestDebug_DefaultRoutesToSlog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Reset to default debugFn in case a prior test mutated package state.
	SetSlogLogger(nil)

	Debugf("hello %s %d", "world", 42)
	out := buf.String()
	if !strings.Contains(out, `msg="hello world 42"`) {
		t.Fatalf("Debugf output not in slog.Default() text handler buffer: %q", out)
	}
	if !strings.Contains(out, `pkg=broflake`) {
		t.Fatalf("expected pkg=broflake attribute in default slog routing, got: %q", out)
	}

	buf.Reset()
	Debug("plain message")
	if !strings.Contains(buf.String(), `msg="plain message"`) {
		t.Fatalf("Debug() not visible in slog.Default(): %q", buf.String())
	}
}

// TestSetSlogLogger_RoutesToProvidedLogger verifies that callers can pin
// broflake's output to a specific slog.Logger (e.g. a child logger with
// added attributes) instead of relying on the package default.
func TestSetSlogLogger_RoutesToProvidedLogger(t *testing.T) {
	t.Cleanup(func() { SetSlogLogger(nil) })

	var buf bytes.Buffer
	custom := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).
		With("source", "custom-test")
	SetSlogLogger(custom)

	Debugf("scoped message %v", "x")
	if !strings.Contains(buf.String(), `source=custom-test`) {
		t.Fatalf("custom slog.Logger not used: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `msg="scoped message x"`) {
		t.Fatalf("formatted message missing: %q", buf.String())
	}
}

// TestSetDebugLogger_BackCompat exercises the legacy *log.Logger setter,
// which flashlight (and any other pre-slog caller) still uses. We need it
// to keep working unchanged after the slog migration.
func TestSetDebugLogger_BackCompat(t *testing.T) {
	t.Cleanup(func() { SetDebugLogger(nil) })

	var buf bytes.Buffer
	SetDebugLogger(log.New(&buf, "legacy: ", 0))

	Debugf("hello %s", "legacy")
	if !strings.Contains(buf.String(), "legacy: hello legacy\n") {
		t.Fatalf("*log.Logger not used: %q", buf.String())
	}
}

// TestDebug_FallsBackToStderrWhenSlogDebugDisabled pins down the second
// half of the default contract: when the active slog handler drops Debug
// (true for the stdlib default at LevelInfo, and any host that hasn't
// opted in to debug), broflake messages must fall back to stderr instead
// of disappearing. The standalone broflake binaries under cmd/ rely on
// this — they call common.Debugf for all visibility and never configure
// slog. Without this fallback they'd go completely silent.
func TestDebug_FallsBackToStderrWhenSlogDebugDisabled(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	// Reset to default debugFn in case a prior test mutated package state.
	SetSlogLogger(nil)

	// Slog handler at LevelInfo — Debug records are dropped.
	var slogBuf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&slogBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Redirect the package-level legacyStderr to a buffer so we can assert
	// fallback delivery without spamming the test runner's stderr.
	var stderrBuf syncBuffer
	prevStderr := legacyStderr
	legacyStderr = log.New(&stderrBuf, "", 0)
	t.Cleanup(func() { legacyStderr = prevStderr })

	Debugf("fallback %s", "ok")

	if slogBuf.Len() != 0 {
		t.Fatalf("slog handler at Info should have dropped Debug record; got %q", slogBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "fallback ok") {
		t.Fatalf("expected stderr fallback to capture message; got %q", stderrBuf.String())
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer for tests. The fallback test
// only writes synchronously so the mutex is overkill, but using it
// pre-empts a flake if a later test exercises a goroutine path.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestSetDebugLogger_NilResetsToDefault makes sure passing nil restores
// slog.Default() routing. This is the path test fixtures use to clean up
// after themselves.
func TestSetDebugLogger_NilResetsToDefault(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Mute first, then restore.
	SetDebugLogger(log.New(io.Discard, "", 0))
	Debugf("muted")
	if buf.Len() != 0 {
		t.Fatalf("expected discarded output, got %q", buf.String())
	}

	SetDebugLogger(nil)
	Debugf("after reset")
	if !strings.Contains(buf.String(), `msg="after reset"`) {
		t.Fatalf("expected slog.Default() to receive output after nil reset, got %q", buf.String())
	}
}
