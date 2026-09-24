package egress

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/getlantern/semconv"
	"github.com/quic-go/quic-go"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/getlantern/broflake/common"
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

	tally := &proxySessionTally{donorCC: "RU"}
	for range 5 {
		tally.count()
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
		tally := &proxySessionTally{donorCC: cc}
		tally.count()
		tally.count()
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

	tally := &proxySessionTally{donorCC: "US"}
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

	tally := &proxySessionTally{donorCC: "US"}
	tally.count() // must not panic
}

// The tests above drive proxySessionTally directly, which leaves the
// production wiring untested: the tally could be dropped from the accept
// loop, or donorCC could stop reaching it, and every one of them would
// still pass while proxy.sessions went empty or mislabelled in real
// traffic. This drives the real handleWebsocket instead — a donor
// WebSocket carrying a real QUIC session, with the country resolved the
// way production resolves it — and asserts the datapoint that session
// produces.
func TestHandleWebsocket_CountsOneSessionWithDonorCountry(t *testing.T) {
	origGeo := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(origGeo) })
	setDonorGeo(fakeCountryLookup{"203.0.113.7": "SE"})

	reader := installTestProxySessionCounter(t)
	consumer := startStreamOpeningConsumer(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()

	startTestDonor(t, srv.URL, "proxysession-one-donor", "203.0.113.7", consumer.addr())

	// The egress dials the consumer through the donor, so the consumer
	// side is where a stream can be opened toward the accept loop.
	quicConn := consumer.awaitConn(t)
	stream, err := openWritableStream(t, quicConn)
	if err != nil {
		t.Fatalf("consumer opening first stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	awaitAcceptedStream(t, l.connections)

	if got := proxySessionCount(t, reader, "SE"); got != 1 {
		t.Fatalf("count = %d after one session carried a stream, want 1 — the accept loop is not counting, or donorCC is not reaching it", got)
	}
	for _, dp := range proxySessionDatapoints(t, reader) {
		if v, _ := dp.Attributes.Value(semconv.ProxyProtocolKey); v.AsString() != "unbounded" {
			t.Errorf("proxy.protocol = %q, want \"unbounded\"", v.AsString())
		}
	}

	// A second stream on the same session must not count again. The unit
	// test pins this against the tally; this pins it against the loop the
	// tally actually lives in.
	second, err := openWritableStream(t, quicConn)
	if err != nil {
		t.Fatalf("consumer opening second stream: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	awaitAcceptedStream(t, l.connections)

	if got := proxySessionCount(t, reader, "SE"); got != 1 {
		t.Errorf("count = %d after a second stream on the same session, want 1", got)
	}
}

// A donor that takes over an existing consumer session by migration is
// proxying for somebody just as surely as one that accepts a fresh
// stream, but it inherits the streams already open, so AcceptStream
// never fires for it. Counting only in the accept loop dropped that
// donor entirely. Migration runs at roughly 4% of donor session churn,
// concentrated in exactly the sessions that carry traffic.
func TestHandleWebsocket_CountsAMigratedSession(t *testing.T) {
	origGeo := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(origGeo) })
	setDonorGeo(fakeCountryLookup{"203.0.113.7": "SE"})

	reader := installTestProxySessionCounter(t)
	consumer := startStreamOpeningConsumer(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()

	const csid = "proxysession-migrating-session"

	startTestDonor(t, srv.URL, csid, "203.0.113.7", consumer.addr())
	quicConn := consumer.awaitConn(t)
	stream, err := openWritableStream(t, quicConn)
	if err != nil {
		t.Fatalf("consumer opening stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	awaitAcceptedStream(t, l.connections)

	if got := proxySessionCount(t, reader, "SE"); got != 1 {
		t.Fatalf("count = %d after the first donor, want 1", got)
	}

	// Second donor, same CSID: createOrMigrate takes the migrate branch
	// and the consumer opens nothing new.
	before := migrationSnapshot()
	startTestDonor(t, srv.URL, csid, "203.0.113.7", consumer.addr())
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if migrationSnapshot()[1] > before[1] {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := migrationSnapshot()[1]; got <= before[1] {
		t.Fatalf("no successful migration was recorded (%d then %d); the test never reached the path it is about", before[1], got)
	}

	if got := proxySessionCount(t, reader, "SE"); got != 2 {
		t.Errorf("count = %d after a second donor took over the session by migration, want 2", got)
	}
}

// startTestEgress stands up a proxyListener serving the real
// handleWebsocket. The connections channel is buffered so the accept
// loop never blocks handing a stream over, and drained by the caller as
// its synchronization point.
func startTestEgress(t *testing.T) (proxyListener, *httptest.Server, func()) {
	t.Helper()
	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
	l := proxyListener{
		connectionManager: cm,
		connections:       make(chan net.Conn, 8),
		addr:              common.DebugAddr("proxysession-test-egress"),
	}
	srv := httptest.NewServer(http.HandlerFunc(l.handleWebsocket))
	t.Cleanup(srv.Close)
	// Returned rather than registered: QUIC connections must close while
	// their PacketConns are still alive, and deferred calls run before
	// every t.Cleanup.
	return l, srv, func() { closeAllRecords(cm) }
}

// streamOpeningConsumer is the censored end of a session: it QUIC-listens
// so the egress's dial completes, and hands the accepted connection back
// so the test can open streams toward the egress. startTestConsumer only
// drains streams, which is the opposite direction from what the accept
// loop needs.
type streamOpeningConsumer struct {
	pc    net.PacketConn
	conns chan *quic.Conn
}

func (c *streamOpeningConsumer) addr() net.Addr { return c.pc.LocalAddr() }

func (c *streamOpeningConsumer) awaitConn(t *testing.T) *quic.Conn {
	t.Helper()
	select {
	case conn := <-c.conns:
		return conn
	case <-time.After(15 * time.Second):
		t.Fatal("egress never completed a QUIC dial to the consumer")
		return nil
	}
}

func startStreamOpeningConsumer(t *testing.T) *streamOpeningConsumer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket consumer: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	tr := &quic.Transport{Conn: pc}
	listener, err := tr.Listen(testServerTLS(), &common.QUICCfg)
	if err != nil {
		t.Fatalf("Transport.Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	c := &streamOpeningConsumer{pc: pc, conns: make(chan *quic.Conn, 1)}
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			select {
			case c.conns <- conn:
			default:
			}
		}
	}()
	return c
}

// startTestDonor plays the volunteer: it dials the egress's /ws with the
// subprotocols and forwarded address a real donor sends, then relays
// between the WebSocket and the consumer's UDP socket. The relay is
// migrationDonor's, minus the parts that build the egress side by hand —
// here the egress side is whatever handleWebsocket builds.
func startTestDonor(t *testing.T, serverURL, csid, donorIP string, consumer net.Addr) {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket donor: %v", err)
	}
	t.Cleanup(func() { _ = udp.Close() })

	header := http.Header{}
	header.Set("X-Forwarded-For", donorIP)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http"), &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: common.NewSubprotocolsRequest(csid, common.Version),
	})
	if err != nil {
		t.Fatalf("donor dialing /ws: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	ws.SetReadLimit(1 << 20)

	// Egress to consumer: unwrap the envelope WriteTo put on the wire.
	go func() {
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var packet common.UnboundedPacket
			if json.Unmarshal(b, &packet) != nil {
				return
			}
			if _, err := udp.WriteTo(packet.Payload, consumer); err != nil {
				return
			}
		}
	}()

	// Consumer to egress: raw datagrams, which is what ReadFrom expects.
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if ws.Write(context.Background(), websocket.MessageBinary, buf[:n]) != nil {
				return
			}
		}
	}()
}

// awaitAcceptedStream waits for the accept loop to hand a stream to the
// listener, which is the point after which the count is observable.
func awaitAcceptedStream(t *testing.T, conns chan net.Conn) {
	t.Helper()
	select {
	case <-conns:
	case <-time.After(15 * time.Second):
		t.Fatal("handleWebsocket never delivered an accepted stream")
	}
}
