package common

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync"
)

// debugFn handles each formatted debug message broflake produces. It's
// swappable so embedders can route output anywhere they like.
//
// The default first tries slog.Default() — which makes broflake's
// internals visible in any host that has configured a slog handler at
// LevelDebug, most notably the Lantern client, where radiance calls
// slog.SetDefault() at startup. If the active slog handler has Debug
// disabled (true for the stdlib default, which sits at LevelInfo), the
// message falls back to stderr so the standalone broflake binaries under
// cmd/ keep the visibility they had before this migration. This dual
// behavior matters because cmd/client_default_impl.go and friends never
// explicitly configure slog and would otherwise go silent.
var (
	debugFn      = defaultDebugFn
	legacyStderr = log.New(os.Stderr, "", log.LstdFlags)
	logMx        sync.RWMutex
)

func defaultDebugFn(msg string) {
	h := slog.Default().Handler()
	if h.Enabled(context.Background(), slog.LevelDebug) {
		slog.Default().Debug(msg, "pkg", "broflake")
		return
	}
	legacyStderr.Println(msg)
}

// SetSlogLogger routes broflake's debug output through the given slog
// logger. Pass nil to fall back to slog.Default().
//
// Prefer this over SetDebugLogger for new callers.
func SetSlogLogger(l *slog.Logger) {
	logMx.Lock()
	defer logMx.Unlock()
	if l == nil {
		debugFn = defaultDebugFn
		return
	}
	debugFn = func(msg string) { l.Debug(msg, "pkg", "broflake") }
}

// SetDebugLogger routes broflake's debug output through the given
// *log.Logger. Kept for callers that pre-date slog (e.g. flashlight); new
// callers should use SetSlogLogger or rely on slog.Default(). Pass nil to
// fall back to slog.Default().
func SetDebugLogger(l *log.Logger) {
	logMx.Lock()
	defer logMx.Unlock()
	if l == nil {
		debugFn = defaultDebugFn
		return
	}
	debugFn = func(msg string) { l.Println(msg) }
}

// Debugf logs a formatted debug-level message.
func Debugf(format string, args ...interface{}) {
	logMx.RLock()
	fn := debugFn
	logMx.RUnlock()
	fn(fmt.Sprintf(format, args...))
}

// Debug logs a non-formatted debug-level message.
func Debug(msg interface{}) {
	logMx.RLock()
	fn := debugFn
	logMx.RUnlock()
	fn(fmt.Sprint(msg))
}
