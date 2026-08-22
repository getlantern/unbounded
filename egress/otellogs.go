package egress

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
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
	endpointVar, endpoint := otlpLogsEndpoint()
	if endpointVar == "" {
		// Warn rather than returning quietly. Whether export is on is not
		// otherwise observable: the answer lives in the absence of logs in the
		// collector, which is indistinguishable from a healthy egress that
		// simply had nothing to say. Someone deploying this needs to be able
		// to confirm it from the journal.
		slog.Warn("Log export disabled: no OTLP logs endpoint configured",
			"checked", strings.Join(otlpLogsEndpointVars, ", "))
		return func(context.Context) error { return nil }
	}

	// Validated before otlploghttp.New, because New reads the same environment
	// variable and its own error handler prints the offending value — with its
	// credentials — straight to the log. It also does not return an error for a
	// malformed endpoint: it logs, falls back, and leaves us announcing an
	// export that will never work. Refusing here means the SDK never sees the
	// value, so it never prints it.
	safeEndpoint, ok := redactEndpoint(endpoint)
	if !ok {
		// The value is deliberately absent: it did not parse, so there is no
		// way to tell which part of it was a secret.
		slog.Warn("Log export disabled: the configured OTLP logs endpoint is not an absolute URL",
			"from", endpointVar)
		return func(context.Context) error { return nil }
	}

	exp, err := newLogExporter(ctx)
	if err != nil {
		// The exporter reports what it could not parse, which for an endpoint
		// problem is the endpoint — credentials included. Substituted rather
		// than dropped, so the diagnostic survives without the secret.
		slog.Warn("Log export disabled; could not build the OTLP log exporter",
			"err", strings.ReplaceAll(err.Error(), endpoint, safeEndpoint),
			"endpoint", safeEndpoint, "from", endpointVar)
		return func(context.Context) error { return nil }
	}

	// Resource is left to the SDK, which defaults to resource.Default() and so
	// picks up OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES. That is the same
	// path sdkmetric.NewMeterProvider takes in telemetry.EnableOTELMetrics, so
	// logs land under the same service.name as the metrics already do.
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)

	// Deliberately not registered as the process-global provider. The bridge
	// below is wired to lp explicitly, so nothing needs the global — and
	// setting it would leave it pointing at a stopped provider after shutdown.
	// The SDK delegates pre-registration loggers exactly once, so that state is
	// not recoverable: anything reaching for the global logger afterwards
	// silently no-ops.

	// Wrap whatever the binary installed rather than replacing it — each
	// egress/cmd main sets a stderr TextHandler at Debug, and that is still
	// the only place the high-volume lines are readable.
	// Emitted before the handler swap so this line is stderr-only, never queued
	// for export. It is the line an operator reads to find out whether export
	// works, so routing it through the exporter it describes would be circular.
	// (stderr would receive it either way — the tee's local leg is stderr — so
	// the ordering is about not exporting it, not about reaching the journal.)
	slog.Info("Log export enabled",
		"endpoint", safeEndpoint, "from", endpointVar, "min_level", otelLogLevel)

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

// newLogExporter is indirected so the failure branch below is reachable from a
// test. otlploghttp.New declines to fail for most bad input — it logs through
// the SDK's error handler and falls back — so there is no environment value
// that exercises the sanitizing path. Same shape as initMetricsFn in metrics.go.
var newLogExporter = func(ctx context.Context) (sdklog.Exporter, error) {
	return otlploghttp.New(ctx)
}

// otlpLogsEndpointVars are the variables that can supply a logs endpoint, in
// OTEL's own precedence order: signal-specific first, then shared.
//
// Deliberately not the metrics or traces variables. A host that sets only
// OTEL_EXPORTER_OTLP_METRICS_ENDPOINT has a collector, but says nothing about
// where logs should go — otlploghttp would fall back to localhost:4318 and
// queue records for something that is not there.
var otlpLogsEndpointVars = []string{
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
}

// otlpLogsEndpoint returns the variable that supplied a logs endpoint and its
// value, or two empty strings when none is configured.
func otlpLogsEndpoint() (name, value string) {
	for _, k := range otlpLogsEndpointVars {
		if v := os.Getenv(k); v != "" {
			return k, v
		}
	}
	return "", ""
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
		// Clone because a Handler may retain or mutate the record it is given,
		// and the local leg has already been handed this one.
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

// redactEndpoint strips anything an OTLP endpoint could legally carry as a
// credential before it reaches a log. These URLs are configuration rather than
// user input, but "https://user:token@collector/v1/logs" and
// "https://collector/v1/logs?api-key=..." are both valid values, and the journal
// is read by more people than the config is. Scheme, host and path are what make
// the line useful.
//
// Anything that is not an absolute http/https URL is refused outright rather
// than returned. url.Parse accepts opaque strings ("secret", "http:token") and
// network-path references ("//s3cr3t", which parses with that as the Host and
// no scheme) without error, and clearing User does nothing to any of them, so
// returning the parsed form would echo the whole value. Requiring both a scheme
// and a host is what makes the redaction meaningful; with no structure to rely
// on there is no way to tell which part was secret.
func redactEndpoint(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	// http/https only: this exporter is OTLP over HTTP, so "ftp://collector"
	// parses fine and would be announced as enabled while being unsendable.
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), true
}
