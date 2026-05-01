package common

import (
	"fmt"
	"log"
	"log/slog"
	"sync"
)

// debugFn handles each formatted debug message broflake produces. It's
// swappable so embedders can route output anywhere they like.
//
// The default routes through slog.Default(), which makes broflake's
// internals visible in any host that has configured a slog handler — most
// notably the Lantern client, where radiance calls slog.SetDefault() at
// startup. Before this default existed, broflake's debug output went to
// stderr via a private *log.Logger, which the structured-log pipeline never
// captured (so "Consumer state 0", "Sending genesis offer", and similar
// breadcrumbs were silently dropped on production clients).
var (
	debugFn = defaultDebugFn
	logMx   sync.RWMutex
)

func defaultDebugFn(msg string) {
	slog.Default().Debug(msg, "pkg", "broflake")
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
