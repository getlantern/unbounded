package egress

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/getlantern/broflake/common"
	"github.com/getlantern/semconv"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installTestProxyIOCounter swaps the process-global proxy.io counter for
// one backed by a ManualReader, so a test can Collect and assert exactly
// what a datapoint would carry, without an OTLP exporter. Restored on
// cleanup so tests can't leak instruments into each other — the same
// discipline withStubbedMetrics applies to the provider lifecycle.
func installTestProxyIOCounter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	c, err := mp.Meter("test").Int64Counter("proxy.io", metric.WithUnit("bytes"))
	if err != nil {
		t.Fatalf("creating test counter: %v", err)
	}
	prev := proxyIOCounter.Swap(&proxyIOHandle{c})
	t.Cleanup(func() {
		proxyIOCounter.Store(prev)
		_ = mp.Shutdown(context.Background())
	})
	return reader
}

// proxyIOSum collects and returns the summed proxy.io datapoint values
// whose network.io.direction attribute equals direction. Zero when no
// datapoint matches.
func proxyIOSum(t *testing.T, reader *sdkmetric.ManualReader, direction string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "proxy.io" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("proxy.io data is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(semconv.NetworkIODirectionKey); ok &&
					v.AsString() == direction {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// The attribute contract is what makes the datapoints land in the right
// BigQuery columns (keys flatten dots-to-underscores) and the right arm
// of teleport.protocols' COALESCE. Getting a spelling wrong doesn't
// error anywhere — it just files the traffic under NULL forever — so the
// exact keys and values are pinned here.
func TestProxyIOSetsFor_AttributeContract(t *testing.T) {
	sets := proxyIOSetsFor("IR")
	for _, tc := range []struct {
		name      string
		set       attribute.Set
		direction string
	}{
		{"rx", sets.rx, "receive"},
		{"tx", sets.tx, "transmit"},
	} {
		if v, ok := tc.set.Value(semconv.ProxyProtocolKey); !ok || v.AsString() != "unbounded" {
			t.Errorf("%s: proxy.protocol = %q (present=%v), want \"unbounded\"", tc.name, v.AsString(), ok)
		}
		if v, ok := tc.set.Value(semconv.GeoCountryISOCodeKey); !ok || v.AsString() != "IR" {
			t.Errorf("%s: geo.country.iso_code = %q (present=%v), want \"IR\"", tc.name, v.AsString(), ok)
		}
		if v, ok := tc.set.Value(semconv.NetworkIODirectionKey); !ok || v.AsString() != tc.direction {
			t.Errorf("%s: network.io.direction = %q (present=%v), want %q", tc.name, v.AsString(), ok, tc.direction)
		}
		if tc.set.Len() != 3 {
			t.Errorf("%s: %d attributes, want exactly 3 — an extra attribute is an extra BigQuery column and a cardinality multiplier", tc.name, tc.set.Len())
		}
	}
}

func TestAddProxyIO_RecordsToInstalledCounter(t *testing.T) {
	reader := installTestProxyIOCounter(t)
	sets := proxyIOSetsFor("US")

	addProxyIO(42, sets.rx)
	addProxyIO(7, sets.tx)
	addProxyIO(1, sets.tx)

	if got := proxyIOSum(t, reader, "receive"); got != 42 {
		t.Errorf("receive sum = %d, want 42", got)
	}
	if got := proxyIOSum(t, reader, "transmit"); got != 8 {
		t.Errorf("transmit sum = %d, want 8", got)
	}
}

// Before initMetrics runs — and in every test that constructs conns
// directly — the counter is not installed. Measurements must be dropped,
// not panic: telemetry is observability, never a gate (the house rule
// from proxyListener.Close applies on the way in, too).
func TestAddProxyIO_NoCounterInstalledIsANoOp(t *testing.T) {
	prev := proxyIOCounter.Swap(nil)
	t.Cleanup(func() { proxyIOCounter.Store(prev) })
	addProxyIO(99, proxyIOSetsFor("US").rx) // must not panic
}

// newWSPair returns the two ends of a live WebSocket, server side first.
// Real frames, not a fake: the point of these tests is that the counted
// quantity is what actually crossed the wire at this layer.
func newWSPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	serverConns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accepting websocket: %v", err)
			return
		}
		serverConns <- c
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	client, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatalf("dialing websocket: %v", err)
	}
	select {
	case server = <-serverConns:
	case <-ctx.Done():
		t.Fatal("server never accepted the websocket")
	}
	t.Cleanup(func() {
		_ = client.CloseNow()
		_ = server.CloseNow()
	})
	return server, client
}

// The transmit direction is the one #3900 exists for — "bandwidth
// shared" is egress→donor. The counted quantity is the marshaled
// UnboundedPacket envelope (what WriteTo actually puts on the wire),
// not the caller's payload.
func TestWriteTo_CountsTransmitWireBytes(t *testing.T) {
	reader := installTestProxyIOCounter(t)
	serverWS, _ := newWSPair(t)

	q := errorlessWebSocketPacketConn{
		w:      serverWS,
		addr:   common.DebugAddr("proxyio-test-tx"),
		ioSets: proxyIOSetsFor("US"),
	}

	payload := []byte("response bytes headed back through a donor")
	if _, err := q.WriteTo(payload, nil); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	envelope, err := json.Marshal(common.UnboundedPacket{
		SourceAddr: q.addr.String(),
		Payload:    payload,
	})
	if err != nil {
		t.Fatalf("marshaling expected envelope: %v", err)
	}
	if got, want := proxyIOSum(t, reader, "transmit"), int64(len(envelope)); got != want {
		t.Errorf("transmit sum = %d, want %d (the marshaled envelope length)", got, want)
	}
	if got := proxyIOSum(t, reader, "receive"); got != 0 {
		t.Errorf("receive sum = %d after a write, want 0", got)
	}
}

func TestReadFrom_CountsReceiveWireBytes(t *testing.T) {
	reader := installTestProxyIOCounter(t)
	serverWS, clientWS := newWSPair(t)

	q := errorlessWebSocketPacketConn{
		w:         serverWS,
		addr:      common.DebugAddr("proxyio-test-rx"),
		keepalive: time.Minute, // far beyond the test's lifetime
		tcpAddr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		readError: make(chan error, 1),
		ioSets:    proxyIOSetsFor("US"),
	}

	msg := []byte("packet from a donor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientWS.Write(ctx, websocket.MessageBinary, msg); err != nil {
		t.Fatalf("client write: %v", err)
	}

	p := make([]byte, 1500)
	n, _, err := q.ReadFrom(p)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("ReadFrom returned %d bytes, want %d", n, len(msg))
	}
	if got, want := proxyIOSum(t, reader, "receive"), int64(len(msg)); got != want {
		t.Errorf("receive sum = %d, want %d", got, want)
	}
	if got := proxyIOSum(t, reader, "transmit"); got != 0 {
		t.Errorf("transmit sum = %d after a read, want 0", got)
	}
}

// Conns constructed without ioSets (migration_test does this) must keep
// working and simply not report — same nil-check convention as the
// stats and sessionBytes fields.
func TestWriteTo_NilIOSetsIsFine(t *testing.T) {
	installTestProxyIOCounter(t)
	serverWS, _ := newWSPair(t)
	q := errorlessWebSocketPacketConn{
		w:    serverWS,
		addr: common.DebugAddr("proxyio-test-nil"),
	}
	if _, err := q.WriteTo([]byte("x"), nil); err != nil {
		t.Fatalf("WriteTo with nil ioSets: %v", err)
	}
}

// WriteTo hides transport errors from its caller (err = nil), but the
// proxy.io tap must not be fooled by that: a failed write moved nothing
// through a donor, so it counts nothing. This is the tripwire for the
// tap's placement BEFORE the error-hiding line — moving it below would
// count every failed write and no other test would notice.
func TestWriteTo_FailedWriteCountsNothing(t *testing.T) {
	reader := installTestProxyIOCounter(t)
	serverWS, _ := newWSPair(t)

	q := errorlessWebSocketPacketConn{
		w:      serverWS,
		addr:   common.DebugAddr("proxyio-test-txfail"),
		ioSets: proxyIOSetsFor("US"),
	}

	// Kill the transport out from under the write. WriteTo still returns
	// nil — that error-hiding is its documented contract — so the only
	// observable distinction between a counted and uncounted failure is
	// the metric itself.
	_ = serverWS.CloseNow()
	if _, err := q.WriteTo([]byte("never delivered"), nil); err != nil {
		t.Fatalf("WriteTo hides transport errors, got: %v", err)
	}

	if got := proxyIOSum(t, reader, "transmit"); got != 0 {
		t.Errorf("transmit sum = %d after a failed write, want 0", got)
	}
}
