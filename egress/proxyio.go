package egress

import (
	"context"
	"sync/atomic"

	"github.com/getlantern/semconv"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// proxy.io measures throughput: every proxy in the fleet emits it, the
// telemetry pipeline forwards it to a datastore, and periodic reporting
// aggregates it into per-protocol totals there. Emitting it here is what
// makes Browsers Unbounded traffic exist in that pipeline at all.
//
// It deliberately coexists with ingress-bytes rather than replacing it:
// ingress-bytes keeps per-host identity (proxy.io has it stripped by the
// pipeline) and existing dashboards read it. proxy.io adds the transmit
// direction — the "bandwidth shared" number — which ingress-bytes never
// carried.
//
// Unlike every other instrument in this package, proxy.io is a
// synchronous counter, because the pipeline requires it to arrive as
// DELTA (see counterTemporality in metrics.go) and switching the
// Observable* instruments' temporality would silently change what their
// saved queries mean.

// proxyProtocol is the value downstream reporting surfaces as this
// traffic's protocol, on proxy.io and on proxy.sessions alike.
// "unbounded" matches the service name and repo; change it only in
// concert with whoever reads those reports.
const proxyProtocol = "unbounded"

// proxyIOHandle wraps the counter interface so atomic.Pointer has a
// single concrete type to hold regardless of which implementation is
// installed.
type proxyIOHandle struct{ c metric.Int64Counter }

// proxyIOCounter is read on every WebSocket read and write, concurrent
// with the provider lifecycle in metrics.go tearing instruments down and
// rebuilding them. The other instrument globals get away with plain
// package variables because only the otel callback reads them; this one
// is read from the packet path, so it is an atomic.Pointer. nil until
// initMetrics installs it; addProxyIO drops measurements while nil.
// After the last listener closes, the pointer still holds the old
// provider's counter — Adds against a shut-down provider are discarded
// by the SDK, which is the behavior we want anyway.
var proxyIOCounter atomic.Pointer[proxyIOHandle]

// proxyIOSets carries one connection's pre-built attribute sets, one per
// direction. Built once per session: attribute.NewSet sorts and hashes,
// which is too much work per packet, and every attribute is fixed for
// the life of the connection.
type proxyIOSets struct {
	rx attribute.Set
	tx attribute.Set
}

// proxyIOSetsFor builds the attribute sets for a session with the given
// donor country. The three attributes here are the whole contract —
// spellings are load-bearing (downstream storage and reporting match on
// them) and anything added multiplies series cardinality, so additions
// need the same scrutiny the freeze-report attributes got in metrics.go.
func proxyIOSetsFor(donorCC string) *proxyIOSets {
	set := func(direction string) attribute.Set {
		return attribute.NewSet(
			semconv.ProxyProtocolKey.String(proxyProtocol),
			semconv.GeoCountryISOCodeKey.String(donorCC),
			semconv.NetworkIODirectionKey.String(direction),
		)
	}
	return &proxyIOSets{rx: set("receive"), tx: set("transmit")}
}

// addProxyIO records n bytes against the installed proxy.io counter, or
// drops them if none is installed (tests constructing conns directly,
// and the window before initMetrics runs).
func addProxyIO(n int64, set attribute.Set) {
	h := proxyIOCounter.Load()
	if h == nil {
		return
	}
	h.c.Add(context.Background(), n, metric.WithAttributeSet(set))
}
