package egress

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// The egress exported metrics and traces but not logs, so everything the log
// lines actually say reached one host's journal and nowhere else. That is the
// half of the freeze-report work that never paid off: the ingest exists so a
// freeze stops dying in the browser that diagnosed it, and instead it died in
// a journal. Same for the refusal diagnostics — a counter says 5.4M
// connections were refused in a week, and the page_url and user_agent that
// would identify them are unreachable.
//
// Only Info and above is exported, which is the whole design constraint rather
// than a detail. The stderr handler runs at Debug and logs per connection, per
// QUIC stream, and per keepalive ping ("PING"), so exporting Debug would ship
// millions of lines a day and cost accordingly. Info and above is bounded by
// construction: the two diagnostic families that matter are rate-limited
// already — one line per refusal reason per minute (6 reasons) and one per
// freeze kind per five minutes (5 kinds) — so the ceiling is roughly 10k lines
// a day no matter what the fleet does.
//
// stderr keeps everything. This adds a second destination rather than moving
// the logs, so deep debugging on a host is unaffected.

// otelLogLevel is the floor for what gets exported. See the note above on why
// this is not Debug.
const otelLogLevel = slog.LevelInfo

// enableOTELLogs installs an exporting handler alongside the current default,
// mirroring how telemetry.EnableOTELMetrics and EnableOTELTracing configure
// themselves purely from OTEL_* environment variables.
//
// Degrades to a no-op on exporter construction failure, matching the telemetry
// package's behaviour: an egress that cannot reach the collector must still
// carry traffic, and it still has stderr.
func enableOTELLogs(ctx context.Context) func(context.Context) error {
	// No configured collector, no exporter. Without this the SDK happily
	// builds one pointed at the default localhost:4318, queues every Info
	// record into a batch processor, and then blocks on shutdown trying to
	// flush them to something that was never listening — which hung
	// NewListener's shutdown for the full test timeout and would do the same
	// to a production egress on a host with no collector.
	//
	// Same shape as telemetry.EnableOTELTracing returning a no-op when its
	// sampler variables are absent: absent configuration means the feature is
	// off, not misconfigured.
	if !otlpLogsConfigured() {
		return func(context.Context) error { return nil }
	}

	exp, err := otlploghttp.New(ctx)
	if err != nil {
		slog.Warn("Log export disabled; could not build the OTLP log exporter", "err", err)
		return func(context.Context) error { return nil }
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	otellogglobal.SetLoggerProvider(lp)

	// Wrap whatever the binary installed rather than replacing it — each
	// egress/cmd main sets a stderr TextHandler at Debug, and that is still
	// the only place the high-volume lines are readable.
	local := slog.Default().Handler()
	remote := otelslog.NewHandler("github.com/getlantern/broflake/egress",
		otelslog.WithLoggerProvider(lp))
	slog.SetDefault(slog.New(&teeHandler{local: local, remote: remote}))

	return func(ctx context.Context) error {
		// Restore the plain local handler before tearing the provider down, so
		// a log line emitted during shutdown does not reach a closed exporter.
		slog.SetDefault(slog.New(local))

		// Bounded, because Shutdown flushes and the collector may be gone by
		// the time we are shutting down — a common case, since both often go
		// away together. Losing a final batch of throttled samples is a much
		// better outcome than refusing to exit.
		ctx, cancel := context.WithTimeout(ctx, logShutdownTimeout)
		defer cancel()
		return lp.Shutdown(ctx)
	}
}

// logShutdownTimeout bounds the final flush. Generous enough for a reachable
// collector on the same host, short enough that an unreachable one does not
// hold up process exit.
const logShutdownTimeout = 5 * time.Second

// otlpLogsConfigured reports whether an OTLP endpoint is configured for logs.
// Checks the signal-specific variable first, matching OTEL's own precedence,
// then the shared one.
func otlpLogsConfigured() bool {
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// teeHandler writes each record to both destinations. Not a general-purpose
// fanout: remote is deliberately gated at otelLogLevel while local sees
// everything, which is the only reason this type exists rather than a slog
// multi-handler dependency.
type teeHandler struct {
	local  slog.Handler
	remote slog.Handler
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.local.Enabled(ctx, level) || h.remoteEnabled(ctx, level)
}

func (h *teeHandler) remoteEnabled(ctx context.Context, level slog.Level) bool {
	return level >= otelLogLevel && h.remote.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	if h.local.Enabled(ctx, r.Level) {
		firstErr = h.local.Handle(ctx, r)
	}
	if h.remoteEnabled(ctx, r.Level) {
		// Clone because a Handler is allowed to retain or mutate the record's
		// attrs, and the local handler has already been handed this one.
		if err := h.remote.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{local: h.local.WithAttrs(attrs), remote: h.remote.WithAttrs(attrs)}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{local: h.local.WithGroup(name), remote: h.remote.WithGroup(name)}
}
