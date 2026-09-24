package egress

import (
	"context"
	"sync/atomic"

	"github.com/getlantern/semconv"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// proxy.sessions counts donor sessions that actually proxied for
// somebody: one increment per WebSocket session, on the first consumer
// stream that session carries, labelled by donor country.
//
// It is the companion to proxy.io, not a duplicate of it. proxy.io
// answers "how much bandwidth did donors share"; nothing in its
// attribute set can answer "how many donors shared it", because it
// deliberately carries no per-peer identity. Reporting needs both
// numbers, so the count gets its own counter rather than a
// cardinality-multiplying identity attribute on the throughput one.
//
// Counted on the first accepted QUIC stream — not at teardown, and not
// on the first byte read. Teardown defers the count for the whole life
// of the session, which loses it across a reporting boundary; first
// byte fires for a QUIC handshake that never goes on to carry consumer
// traffic. An accepted stream is a consumer connection being served,
// which is what "proxied traffic for others" has to mean here.
//
// It counts sessions, not people. A donor who reconnects, or whose
// consumer migrates onto a different WebSocket, starts a new session
// and counts again; there is no stable donor identity at this layer to
// deduplicate on. Reporting must describe this as session activations,
// which is an overcount of humans — in the opposite direction from the
// telemetry-opt-in undercount it sits beside.

// proxySessionHandle wraps the counter interface so atomic.Pointer has
// a single concrete type to hold, for the reasons proxyIOHandle gives.
type proxySessionHandle struct{ c metric.Int64Counter }

// proxySessionCounter is installed by initMetrics and read from each
// session's stream-accept goroutine. nil until then, and again for
// tests that drive sessions without standing up a provider;
// recordProxySession drops measurements while nil, the same contract
// addProxyIO follows.
var proxySessionCounter atomic.Pointer[proxySessionHandle]

// proxySessionTally records one session's activation on the first
// stream it carries and never again. One per session, owned by the
// single goroutine that accepts that session's streams, so the flag
// needs no synchronization.
type proxySessionTally struct {
	donorCC string
	counted bool
}

// streamAccepted notes that this session accepted a consumer stream.
func (t *proxySessionTally) streamAccepted() {
	if t.counted {
		return
	}
	t.counted = true
	recordProxySession(t.donorCC)
}

// recordProxySession increments proxy.sessions for a donor in donorCC,
// or drops the measurement when no counter is installed.
//
// The attribute set is built here rather than once per session because
// it is needed at most once per session, and only for the sessions that
// proxied anything. The spellings are load-bearing the same way
// proxyIOSetsFor's are: downstream storage files these under columns
// named after the keys, so a wrong one files the count under NULL
// instead of failing anywhere visible.
func recordProxySession(donorCC string) {
	h := proxySessionCounter.Load()
	if h == nil {
		return
	}
	h.c.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(
		semconv.ProxyProtocolKey.String(proxyProtocol),
		semconv.GeoCountryISOCodeKey.String(donorCC),
	)))
}
