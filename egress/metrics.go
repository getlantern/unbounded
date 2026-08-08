package egress

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/getlantern/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OTel setup for the egress, scoped to the process rather than to a listener.
//
// It used to live inline in NewListener, which meant every listener built its own
// copy of everything. That was raised in review three separate times before this
// fixed it, and by then the pattern had accreted seven instruments — so what began
// as a tidiness point had become the shape of a real outage.
//
// Nothing it touches is per-listener. The tallies the callback reads (refusals,
// teardowns, freezeReports, statsByCC) are package globals, the instrument handles
// are package variables, and telemetry.EnableOTELMetrics installs a *global* meter
// provider via otel.SetMeterProvider. Calling it twice therefore did not produce two
// independent setups; it produced one broken one, in three distinct ways:
//
//   - Duplicate series. Both callbacks observe the same process-global tallies, so
//     every attribute set is reported twice per collection cycle. Exactly the
//     double-counting #375 fixed for donor_country, reintroduced by a second listener.
//   - Silently dropped observations. The callback closure captures nothing — it reads
//     nClientsCounter and friends as globals when it runs. A second NewListener
//     overwrites those variables with instruments belonging to the *new* provider,
//     so the first provider's callback then observes instruments it never registered,
//     and the SDK discards them. The #399 failure mode, arrived at from the other end.
//   - A live orphan. SetMeterProvider replaces the global but does not stop the old
//     provider, whose periodic reader keeps exporting on its own schedule. The first
//     provider is unreachable and still talking.
//
// Shutdown had the mirror-image problem. closeMetrics was per-listener while the
// provider was not, so the first listener to close shut down telemetry for every
// listener still serving — and, because it held a closure over its *own* provider,
// it might shut down the orphan while the global one kept running. Reference
// counting is the honest model: the last listener out turns off the lights, and a
// listener created afterwards gets a fresh setup rather than a dead one.
//
// None of this is reachable from cmd/, which builds exactly one listener. It is
// reachable from tests, and NewListener was deliberately made safe to call twice
// (see the per-listener ServeMux in egresslib.go) — which is precisely the
// invitation that makes leaving it broken a bad trade.

// The instrument handles. Package-scoped because the otel callback is registered
// once and reads them when it runs; see the callback for why that matters.
var nClientsCounter metric.Int64ObservableUpDownCounter
var nQUICStreamsCounter metric.Int64ObservableUpDownCounter
var nQUICConnectionsCounter metric.Int64ObservableUpDownCounter
var nIngressBytesCounter metric.Int64ObservableUpDownCounter

// refusedCounter tallies connections turned away before a session span exists.
// A monotonic Counter, not an UpDownCounter: it is a cumulative tally meant to be
// rate()'d, unlike the concurrency gauges above.
var refusedCounter metric.Int64ObservableCounter

// teardownCounter tallies ended sessions by reason. Monotonic, and deliberately a
// metric rather than only a span attribute: session spans are sampled at 1%, which
// is fine for inspecting one session and useless for counting rare ones.
var teardownCounter metric.Int64ObservableCounter

// freezeReportCounter tallies freeze reports beaconed by the page-side watchdog,
// labelled by diagnosis. The counterpart to session-teardowns: that one says a donor
// stopped answering, this one says why.
var freezeReportCounter metric.Int64ObservableCounter

var (
	// metricsMu guards both fields below. A plain mutex rather than sync.Once
	// because the setup has to be repeatable: refcount reaching zero shuts the
	// provider down, and a listener created after that must get a working one
	// rather than inherit the corpse. sync.Once cannot express that.
	metricsMu       sync.Mutex
	metricsRefs     int
	metricsShutdown func(context.Context) error
)

// startMetrics installs the process-wide metric and tracing setup if it is not
// already running, and returns this caller's release function.
//
// The returned function is idempotent — proxyListener.Close is not guaranteed to be
// called exactly once — and only performs the real shutdown when it drops the last
// reference.
func startMetrics(ctx context.Context) (func(context.Context) error, error) {
	metricsMu.Lock()
	defer metricsMu.Unlock()

	if metricsRefs == 0 {
		shutdown, err := initMetricsFn(ctx)
		if err != nil {
			// Deliberately before the increment. Counting a failed setup as a live
			// reference would make the next caller skip initialization and run with
			// no instruments at all — a silent version of the failure that just
			// announced itself.
			return nil, err
		}
		metricsShutdown = shutdown
	}
	metricsRefs++

	var once sync.Once
	return func(ctx context.Context) error {
		var err error
		once.Do(func() {
			metricsMu.Lock()
			defer metricsMu.Unlock()
			metricsRefs--
			if metricsRefs == 0 && metricsShutdown != nil {
				err = metricsShutdown(ctx)
				metricsShutdown = nil
			}
		})
		return err
	}, nil
}

// initMetricsFn is the setup entry point, indirected so the lifecycle tests can
// exercise refcounting without standing up an OTLP exporter and a real meter
// provider. Production always runs initMetrics.
var initMetricsFn = initMetrics

// initMetrics creates the exporters, instruments and callback. Callers must hold
// metricsMu.
func initMetrics(ctx context.Context) (func(context.Context) error, error) {
	closeFuncMetrics := telemetry.EnableOTELMetrics(ctx)

	// Tracing powers the per-session spans in handleWebsocket. Enabled alongside
	// metrics rather than instead of them: the counters answer "is the fleet
	// carrying traffic", the spans answer "did this particular consumer session
	// get served", and neither substitutes for the other.
	closeFuncTracing := telemetry.EnableOTELTracing(ctx)

	// Shut both down together. Dropping the tracing shutdown would leak the
	// provider and discard whatever spans were still buffered, which on a
	// low-traffic egress could be most of them.
	shutdown := func(ctx context.Context) error {
		return errors.Join(closeFuncMetrics(ctx), closeFuncTracing(ctx))
	}

	m := otel.Meter("github.com/getlantern/broflake/egress")

	var err error
	if nClientsCounter, err = m.Int64ObservableUpDownCounter("concurrent-websockets"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if nQUICConnectionsCounter, err = m.Int64ObservableUpDownCounter("concurrent-quic-connections"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if nQUICStreamsCounter, err = m.Int64ObservableUpDownCounter("concurrent-quic-streams"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if nIngressBytesCounter, err = m.Int64ObservableUpDownCounter("ingress-bytes"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if refusedCounter, err = m.Int64ObservableCounter("refused-websockets"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if teardownCounter, err = m.Int64ObservableCounter("session-teardowns"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}
	if freezeReportCounter, err = m.Int64ObservableCounter("freeze-reports"); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}

	if _, err = m.RegisterCallback(
		observeMetrics,
		nClientsCounter,
		nQUICConnectionsCounter,
		nQUICStreamsCounter,
		nIngressBytesCounter,
		refusedCounter,
		// Every instrument the callback observes must be declared here. The SDK
		// ignores observations for anything absent from this list, so omitting one
		// produces a metric that is registered, incremented, observed — and never
		// exported. Silent, and indistinguishable from "the event never happened".
		teardownCounter,
		freezeReportCounter,
	); err != nil {
		return nil, shutdownAfter(ctx, shutdown, err)
	}

	return shutdown, nil
}

// shutdownAfter tears down the half-built provider and returns the original error,
// so a failure partway through setup does not leave an exporter running with no
// instruments attached to it.
func shutdownAfter(ctx context.Context, shutdown func(context.Context) error, err error) error {
	return errors.Join(err, shutdown(ctx))
}

// observeMetrics is the single otel callback. Registered once per process, so it
// must read only process-global state — which is all it does.
func observeMetrics(ctx context.Context, o metric.Observer) error {
	o.ObserveInt64(nQUICConnectionsCounter, int64(atomic.LoadUint64(&nQUICConnections)))
	o.ObserveInt64(nQUICStreamsCounter, int64(atomic.LoadUint64(&nQUICStreams)))

	// concurrent-websockets and ingress-bytes are reported per donor country
	// rather than as a single unlabelled series. Totals are preserved: summing
	// across the country dimension gives the same number the unlabelled series
	// carried, so queries that don't group by country are unaffected. Anything
	// that reduced with max/latest instead of sum needs a spaceAggregation of sum
	// to stay correct.
	//
	// CAUTION when summing: these datapoints also carry a `via` resource attribute
	// identifying the telemetry collector that forwarded them (ops-0/1/2), and the
	// same datapoint arrives once per collector. The egress is a single instance —
	// instance.id has exactly one value, unbounded-us-linode-nj.iantem.io — so
	// summing across `via` triples the real figure. Sum across donor_country, but
	// filter or average across `via`.
	//
	// Note these counts come from per-session counters incremented and decremented
	// exactly once around the handler, whereas the legacy global nClients decrements
	// in the conn's Close(). nClients is now only used for the log lines.
	eachRefusal(func(reason refusalReason, count int64) {
		o.ObserveInt64(refusedCounter, count,
			metric.WithAttributes(attribute.String("reason", string(reason))))
	})

	eachTeardown(func(reason teardownReason, count int64) {
		o.ObserveInt64(teardownCounter, count,
			metric.WithAttributes(attribute.String("reason", string(reason))))
	})

	// kind only. url, userAgent and longestTaskName are peer-chosen and unbounded;
	// any of them here would be unbounded cardinality controlled by a stranger.
	eachFreezeReport(func(kind freezeKind, count int64) {
		o.ObserveInt64(freezeReportCounter, count,
			metric.WithAttributes(attribute.String("kind", string(kind))))
	})

	eachCountryStats(func(cc string, clients, ingressBytes int64) {
		attrs := metric.WithAttributes(attribute.String(attrDonorCountry, cc))
		o.ObserveInt64(nClientsCounter, clients, attrs)
		o.ObserveInt64(nIngressBytesCounter, ingressBytes, attrs)
	})
	return nil
}
