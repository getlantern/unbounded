package egress

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
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

// installTestActivationCounter swaps the process-global
// proxy.activations counter for one backed by a ManualReader, so a test
// can Collect and assert exactly what a datapoint would carry. Restored
// on cleanup, the same discipline installTestProxyIOCounter follows.
func installTestActivationCounter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	c, err := mp.Meter("test").Int64Counter("proxy.activations", metric.WithUnit("activation"))
	if err != nil {
		t.Fatalf("creating test counter: %v", err)
	}
	prev := proxyActivationCounter.Swap(&proxyActivationHandle{c})
	t.Cleanup(func() {
		proxyActivationCounter.Store(prev)
		_ = mp.Shutdown(context.Background())
	})
	return reader
}

// activationDatapoints collects every proxy.activations datapoint.
func activationDatapoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	var dps []metricdata.DataPoint[int64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "proxy.activations" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("proxy.activations data is %T, want Sum[int64]", m.Data)
			}
			dps = append(dps, sum.DataPoints...)
		}
	}
	return dps
}

// activationCount sums the datapoints recorded for one donor country.
func activationCount(t *testing.T, reader *sdkmetric.ManualReader, donorCC string) int64 {
	t.Helper()
	var total int64
	for _, dp := range activationDatapoints(t, reader) {
		if v, ok := dp.Attributes.Value(semconv.GeoCountryISOCodeKey); ok && v.AsString() == donorCC {
			total += dp.Value
		}
	}
	return total
}

// testActivationSet returns a set whose clock the test advances by hand.
func testActivationSet() (*activationSet, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	s := newActivationSet()
	s.now = func() time.Time { return now }
	return s, &now
}

// The attribute contract is what downstream storage and reporting key
// on. A wrong spelling doesn't error anywhere — it files the count
// under NULL forever — so the exact keys, values and the absence of
// anything else, the activation ID above all, are pinned here.
func TestRecordActivation_AttributeContract(t *testing.T) {
	reader := installTestActivationCounter(t)

	recordActivation("IR")

	dps := activationDatapoints(t, reader)
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
		t.Errorf("value = %d, want 1", dps[0].Value)
	}
}

func TestRecordActivation_NoCounterInstalledIsANoOp(t *testing.T) {
	prev := proxyActivationCounter.Swap(nil)
	t.Cleanup(func() { proxyActivationCounter.Store(prev) })

	recordActivation("IR")
}

func TestActivationSet_CountsOncePerID(t *testing.T) {
	reader := installTestActivationCounter(t)
	s, _ := testActivationSet()
	a, b := common.NewActivationID(), common.NewActivationID()

	for range 5 {
		s.record(a, "SE")
	}
	s.record(b, "SE")

	if got := activationCount(t, reader, "SE"); got != 2 {
		t.Fatalf("count = %d for two IDs seen six times, want 2", got)
	}
}

func TestActivationSet_IgnoresMissingIDAndNilSet(t *testing.T) {
	reader := installTestActivationCounter(t)
	s, _ := testActivationSet()

	s.record("", "SE")
	var none *activationSet
	none.record(common.NewActivationID(), "SE")

	if got := activationCount(t, reader, "SE"); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// Each sighting refreshes the entry, so a widget that stays on and
// keeps opening WebSockets is never re-counted, however long it runs.
// Only an ID that goes quiet for longer than the TTL counts again.
func TestActivationSet_IdleExpiry(t *testing.T) {
	reader := installTestActivationCounter(t)
	s, now := testActivationSet()
	id := common.NewActivationID()

	s.record(id, "SE")
	for range 10 {
		*now = now.Add(activationIdleTTL / 2)
		s.record(id, "SE")
	}
	if got := activationCount(t, reader, "SE"); got != 1 {
		t.Fatalf("count = %d for an ID active across five TTLs, want 1", got)
	}

	*now = now.Add(activationIdleTTL + time.Second)
	s.record(id, "SE")
	if got := activationCount(t, reader, "SE"); got != 2 {
		t.Fatalf("count = %d after the ID was idle past the TTL, want 2", got)
	}
}

func TestActivationSet_SweepsExpiredEntries(t *testing.T) {
	s, now := testActivationSet()
	for range 100 {
		s.first(common.NewActivationID())
	}

	*now = now.Add(activationIdleTTL + activationSweepInterval)
	s.first(common.NewActivationID())

	if n := len(s.lastSeen); n != 1 {
		t.Fatalf("%d entries after the sweep, want 1", n)
	}
}

// A flood of IDs must not evict real ones, or it could re-count every
// live widget. A full set declines new IDs and keeps the old ones.
func TestActivationSet_FullSetDeclinesNewIDs(t *testing.T) {
	s, _ := testActivationSet()
	live := common.NewActivationID()
	s.first(live)
	for len(s.lastSeen) < maxActivations {
		s.lastSeen[common.NewActivationID()] = s.now()
	}

	if s.first(common.NewActivationID()) {
		t.Error("a new ID counted into a full set")
	}
	if s.first(live) {
		t.Error("an ID already in the full set counted again")
	}
	if n := len(s.lastSeen); n != maxActivations {
		t.Errorf("set grew to %d past maxActivations", n)
	}
}

// A donor whose path probe fails has proxied nothing for the consumer,
// so a WebSocket whose createOrMigrate fails counts nothing even when
// it carries an ID.
func TestHandleWebsocket_FailedMigrationCountsNothing(t *testing.T) {
	reader := installTestActivationCounter(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()
	l.connectionManager.probeTimeout = time.Second

	const csid = "activation-failed-migration"
	consumer := startStreamOpeningConsumer(t)
	startTestDonor(t, srv.URL, csid, "203.0.113.7", "", consumer.addr())
	consumer.awaitConn(t)
	awaitEstablished(t, l.connectionManager, csid)

	// Nothing listens here, so the probe through this donor never
	// gets an answer.
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	addr := dead.LocalAddr()
	_ = dead.Close()

	before := migrationSnapshot()
	startTestDonor(t, srv.URL, csid, "203.0.113.7", common.NewActivationID(), addr)
	probeErr := slices.Index(migrationOutcomes[:], migrationProbeError)
	for deadline := time.Now().Add(15 * time.Second); migrationSnapshot()[probeErr] == before[probeErr]; {
		if time.Now().After(deadline) {
			t.Fatal("no failed probe was recorded; the test never reached the path it is about")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got := len(activationDatapoints(t, reader)); got != 0 {
		t.Fatalf("%d datapoints for a donor whose migration failed, want 0", got)
	}
}

// The case the metric exists for: one widget, one activation, several
// WebSockets — two consumers, one of which migrates to a new donor
// WebSocket. Exactly one activation, labelled with the donor's country.
func TestHandleWebsocket_CountsOneActivationAcrossWebSockets(t *testing.T) {
	origGeo := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(origGeo) })
	setDonorGeo(fakeCountryLookup{"203.0.113.7": "SE"})

	reader := installTestActivationCounter(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()
	id := common.NewActivationID()

	first := startStreamOpeningConsumer(t)
	startTestDonor(t, srv.URL, "activation-consumer-1", "203.0.113.7", id, first.addr())
	first.awaitConn(t)
	awaitEstablished(t, l.connectionManager, "activation-consumer-1")
	awaitActivationCount(t, reader, "SE", 1)

	second := startStreamOpeningConsumer(t)
	startTestDonor(t, srv.URL, "activation-consumer-2", "203.0.113.7", id, second.addr())
	second.awaitConn(t)
	awaitEstablished(t, l.connectionManager, "activation-consumer-2")

	// Same CSID as the first: createOrMigrate takes the migrate branch.
	before := migrationSnapshot()
	startTestDonor(t, srv.URL, "activation-consumer-1", "203.0.113.7", id, first.addr())
	awaitMigration(t, before)

	// awaitMigration returns while createOrMigrate still holds the
	// record, and the handler records just after releasing it. Wait out
	// the lock and then briefly, so a per-WebSocket count would have
	// landed before the assertion.
	awaitEstablished(t, l.connectionManager, "activation-consumer-1")
	time.Sleep(50 * time.Millisecond)
	if got := activationCount(t, reader, "SE"); got != 1 {
		t.Errorf("count = %d for one activation across three WebSockets, want 1", got)
	}
	if got := l.activations.first(id); got {
		t.Error("the activation was not remembered")
	}
}

// Two widgets, or one widget turned off and on again, are two
// activations even when they serve the same consumer.
func TestHandleWebsocket_CountsEachActivation(t *testing.T) {
	origGeo := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(origGeo) })
	setDonorGeo(fakeCountryLookup{"203.0.113.7": "SE", "198.51.100.9": "DE"})

	reader := installTestActivationCounter(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()

	consumer := startStreamOpeningConsumer(t)
	startTestDonor(t, srv.URL, "activation-shared", "203.0.113.7", common.NewActivationID(), consumer.addr())
	consumer.awaitConn(t)
	awaitEstablished(t, l.connectionManager, "activation-shared")

	before := migrationSnapshot()
	startTestDonor(t, srv.URL, "activation-shared", "198.51.100.9", common.NewActivationID(), consumer.addr())
	awaitMigration(t, before)

	awaitActivationCount(t, reader, "SE", 1)
	awaitActivationCount(t, reader, "DE", 1)
}

// A widget that predates activation IDs still proxies; it just isn't
// counted.
func TestHandleWebsocket_NoIDCountsNothing(t *testing.T) {
	reader := installTestActivationCounter(t)
	l, srv, cleanup := startTestEgress(t)
	defer cleanup()

	consumer := startStreamOpeningConsumer(t)
	startTestDonor(t, srv.URL, "activation-legacy", "203.0.113.7", "", consumer.addr())
	consumer.awaitConn(t)
	awaitEstablished(t, l.connectionManager, "activation-legacy")

	if got := len(activationDatapoints(t, reader)); got != 0 {
		t.Fatalf("%d datapoints for a donor that sent no ID, want 0", got)
	}
}

// awaitActivationCount polls, because the donor's view of the QUIC
// handshake completing races the handler returning from createOrMigrate.
func awaitActivationCount(t *testing.T, reader *sdkmetric.ManualReader, donorCC string, want int64) {
	t.Helper()
	var got int64
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if got = activationCount(t, reader, donorCC); got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("count for %s = %d, want %d", donorCC, got, want)
}

// awaitEstablished waits for createOrMigrate to finish storing csid's
// connection. The consumer sees the handshake complete before the
// egress handler returns, and the handler records its activation right
// after, so a test that asserts or tears down before this races it.
// record.mx is held for all of createOrMigrate, which is what makes
// this wait rather than peek.
func awaitEstablished(t *testing.T, cm *connectionManager, csid string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		cm.mx.Lock()
		record := cm.connections[csid]
		cm.mx.Unlock()
		if record != nil {
			record.mx.Lock()
			ok := record.connection != nil
			record.mx.Unlock()
			if ok {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("createOrMigrate never stored a connection for %s", csid)
}

// awaitMigration waits for a successful migration to be recorded, so a
// test knows it reached the migrate branch it is about.
func awaitMigration(t *testing.T, before [len(migrationOutcomes)]int64) {
	t.Helper()
	success := slices.Index(migrationOutcomes[:], migrationSuccess)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if migrationSnapshot()[success] > before[success] {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no successful migration was recorded; the test never reached the path it is about")
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
		addr:              common.DebugAddr("activation-test-egress"),
		activations:       newActivationSet(),
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
func startTestDonor(t *testing.T, serverURL, csid, donorIP, activationID string, consumer net.Addr) {
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
		Subprotocols: common.NewSubprotocolsRequestWithActivation(csid, common.Version, "", activationID),
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
