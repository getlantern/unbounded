package egress

import (
	"context"
	"testing"

	"github.com/getlantern/semconv"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installTestProxySessionCounter swaps the process-global
// proxy.sessions counter for one backed by a ManualReader, so a test
// can Collect and assert exactly what a datapoint would carry. Restored
// on cleanup, the same discipline installTestProxyIOCounter follows.
func installTestProxySessionCounter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	c, err := mp.Meter("test").Int64Counter("proxy.sessions", metric.WithUnit("session"))
	if err != nil {
		t.Fatalf("creating test counter: %v", err)
	}
	prev := proxySessionCounter.Swap(&proxySessionHandle{c})
	t.Cleanup(func() {
		proxySessionCounter.Store(prev)
		_ = mp.Shutdown(context.Background())
	})
	return reader
}

// proxySessionDatapoints collects and returns every proxy.sessions
// datapoint, so a test can assert on both the values and the attributes
// carried alongside them.
func proxySessionDatapoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	var dps []metricdata.DataPoint[int64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "proxy.sessions" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("proxy.sessions data is %T, want Sum[int64]", m.Data)
			}
			dps = append(dps, sum.DataPoints...)
		}
	}
	return dps
}

// proxySessionCount sums the datapoints recorded for one donor country.
func proxySessionCount(t *testing.T, reader *sdkmetric.ManualReader, donorCC string) int64 {
	t.Helper()
	var total int64
	for _, dp := range proxySessionDatapoints(t, reader) {
		if v, ok := dp.Attributes.Value(semconv.GeoCountryISOCodeKey); ok && v.AsString() == donorCC {
			total += dp.Value
		}
	}
	return total
}

// The attribute contract is what downstream storage and reporting key
// on to classify these counts. A wrong spelling doesn't error anywhere
// — it files the count under NULL forever — so the exact keys, values
// and the absence of anything else are pinned here.
func TestRecordProxySession_AttributeContract(t *testing.T) {
	reader := installTestProxySessionCounter(t)

	recordProxySession("IR")

	dps := proxySessionDatapoints(t, reader)
	if len(dps) != 1 {
		t.Fatalf("%d datapoints, want 1", len(dps))
	}
	attrs := dps[0].Attributes
	if v, ok := attrs.Value(semconv.ProxyProtocolKey); !ok || v.AsString() != "unbounded" {
		t.Errorf("proxy.protocol = %q (present=%v), want \"unbounded\"", v.AsString(), ok)
	}
	if v, ok := attrs.Value(semconv.GeoCountryISOCodeKey); !ok || v.AsString() != "IR" {
		t.Errorf("geo.country.iso_code = %q (present=%v), want \"IR\"", v.AsString(), ok)
	}
	if attrs.Len() != 2 {
		t.Errorf("%d attributes, want exactly 2 — an extra attribute is an extra datastore column and a cardinality multiplier", attrs.Len())
	}
	if dps[0].Value != 1 {
		t.Errorf("value = %d, want 1 — one session, counted once", dps[0].Value)
	}
}

// The tally is what keeps proxy.sessions counting sessions rather than
// streams: a busy donor accepts many streams on one WebSocket and must
// still contribute exactly one activation. Without this, the metric
// silently becomes a stream counter and the number inflates by whatever
// the average streams-per-session happens to be.
func TestProxySessionTally_CountsOncePerSession(t *testing.T) {
	reader := installTestProxySessionCounter(t)

	tally := proxySessionTally{donorCC: "RU"}
	for range 5 {
		tally.streamAccepted()
	}

	if got := proxySessionCount(t, reader, "RU"); got != 1 {
		t.Errorf("count = %d after 5 streams on one session, want 1", got)
	}
}

// Each session gets its own tally, so two donors counted separately
// must both land — with their own country labels.
func TestProxySessionTally_CountsEachSession(t *testing.T) {
	reader := installTestProxySessionCounter(t)

	for _, cc := range []string{"RU", "RU", "CN"} {
		tally := proxySessionTally{donorCC: cc}
		tally.streamAccepted()
		tally.streamAccepted()
	}

	if got := proxySessionCount(t, reader, "RU"); got != 2 {
		t.Errorf("RU count = %d, want 2", got)
	}
	if got := proxySessionCount(t, reader, "CN"); got != 1 {
		t.Errorf("CN count = %d, want 1", got)
	}
}

// A session that connects, handshakes and never carries a consumer
// stream is not an activation. This is the whole reason the count hangs
// off AcceptStream rather than off session setup or the first byte
// read, so it gets a tripwire of its own.
func TestProxySessionTally_NoStreamsCountsNothing(t *testing.T) {
	reader := installTestProxySessionCounter(t)

	tally := proxySessionTally{donorCC: "US"}
	_ = tally

	if dps := proxySessionDatapoints(t, reader); len(dps) != 0 {
		t.Errorf("%d datapoints for a session that carried no streams, want 0", len(dps))
	}
}

// Before initMetrics runs — and in every test that drives sessions
// without a provider — the counter is not installed. Measurements must
// be dropped, not panic: telemetry is observability, never a gate.
func TestRecordProxySession_NoCounterInstalledIsANoOp(t *testing.T) {
	prev := proxySessionCounter.Swap(nil)
	t.Cleanup(func() { proxySessionCounter.Store(prev) })

	tally := proxySessionTally{donorCC: "US"}
	tally.streamAccepted() // must not panic
}
