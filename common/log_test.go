package common

import (
	"bytes"
	"io"
	"log"
	"log/slog"
	"strings"
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
