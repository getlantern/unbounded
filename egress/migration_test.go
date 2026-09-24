package egress

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

// TestConnectionManager_Migration_HappyPath exercises the egress's
// createOrMigrate happy path end-to-end: a QUIC connection is established
// over path A, then a second call with the same CSID and a different
// underlying transport must migrate the existing connection (AddPath →
// Probe → Switch) instead of dialing a new one.
//
// The test uses real UDP loopback as the transport so that quic-go's
// AddPath / Probe / Switch APIs are exercised over actual packets — if a
// future quic-go bump regresses the migration API contract, this test
// fails with a specific error (AddPath / probe / switch) and we know
// which step regressed. Production runs `errorlessWebSocketPacketConn`
// underneath, but for the migration verb specifically the only thing
// that matters is that path A is a working PacketConn, not that it's
// specifically a WebSocket — the createOrMigrate signature was relaxed
// to net.PacketConn precisely to make this test possible.
//
// Originally written 2026-05-04 in response to nelson's report of
// intermittent migration failures (~13% in prod) over the past week.
// Two suspect dep bumps landed mid-April:
//
//   - 3f2b8cb: quic-go v0.51 → v0.59 (with rebased fork)
//   - ca7cd88: pion/webrtc v3 → v4.2.11
//
// If this test passes after either bump, the API contract still holds at
// the quic-go layer and the regression is elsewhere (e.g. consumer-side
// idle-timeout during the re-pair gap, or producer-side WS bring-up
// delays exceeding the migration window).
func TestConnectionManager_Migration_HappyPath(t *testing.T) {
	// These tests run serially because their counter snapshots are process-wide.
	before := migrationSnapshot()

	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })

	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
	// Deferred connection shutdown runs before transport t.Cleanup callbacks,
	// so QUIC connections close while their PacketConns are still alive.

	csid := "test-csid-happy"

	// Path A: brand-new CSID → should take the dial branch.
	pconnA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnA: %v", err)
	}
	t.Cleanup(func() { _ = pconnA.Close() })

	connA, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnA, dst: consumer.LocalAddr()})
	if err != nil {
		t.Fatalf("createOrMigrate path A: %v", err)
	}

	// Sanity: a stream can be opened over the new connection. If quic-go
	// rejects this call, the connection setup itself regressed (not
	// migration), but we want to fail noisily here before the real test
	// assertion runs.
	streamA, err := openWritableStream(t, connA)
	if err != nil {
		t.Fatalf("OpenStreamSync over path A: %v", err)
	}
	_ = streamA.Close()

	// Path B: same CSID, fresh PacketConn → should take the migrate
	// branch (AddPath / Probe / Switch) instead of dialing again.
	//
	// Note that we deliberately do NOT close path A first. Production's
	// errorlessWebSocketPacketConn hides read errors via runtime.Goexit;
	// a plain UDP PacketConn doesn't, so closing path A here would
	// surface the close to quic-go's read goroutine on transport A in a
	// way that production never sees. Leaving path A alive matches the
	// real "two WebSockets concurrently visible to the egress for the
	// same CSID" scenario — which is the actual migration trigger when
	// the producer re-pairs faster than path A's read loop notices.
	pconnB, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnB: %v", err)
	}
	t.Cleanup(func() { _ = pconnB.Close() })
	// Deferred calls run before every t.Cleanup, regardless of registration order.
	defer closeAllRecords(cm)

	connB, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnB, dst: consumer.LocalAddr()})
	if err != nil {
		// This is the diagnostic line nelson would search for if
		// migration regressed. The error string itself names the
		// failing step (AddPath / path probe / path switch), so the
		// test failure message tells you exactly which sub-API broke.
		t.Fatalf("createOrMigrate path B (expected migration, got error): %v", err)
	}
	if connB != connA {
		t.Fatalf("expected migration to return the same *quic.Conn, got a different connection — migrate branch was skipped")
	}

	// Final invariant: a fresh stream opens over the migrated
	// connection. If migration "succeeded" but the connection state is
	// dead, OpenStreamSync would error here. Pairs with the egress's
	// production behavior where the egress opens streams against the
	// (now-migrated) connection in handleWebsocket's accept loop.
	streamB, err := openWritableStream(t, connB)
	if err != nil {
		t.Fatalf("OpenStreamSync over migrated connection: %v", err)
	}
	_ = streamB.Close()
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{1, 1, 0, 0, 0})
}

// TestConnectionManager_Migration_ProbeTimeout pins down what happens
// when the new path can't carry packets back to the egress (matches the
// exact failure mode seen in prod: "createOrMigrate error: path probe
// error: context deadline exceeded"). We simulate the broken path with a
// PacketConn whose WriteTo silently drops everything, so the consumer
// never sees the probe and the egress times out waiting for a response.
//
// This isn't a regression test in the failure sense (we want it to
// "succeed" by detecting the timeout) — it's a contract test that the
// egress correctly surfaces the timeout as an error rather than hanging
// or panicking, and that the connection state is preserved for a
// subsequent retry.
func TestConnectionManager_Migration_ProbeTimeout(t *testing.T) {
	before := migrationSnapshot()

	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })

	probeTimeout := 1500 * time.Millisecond // short so the test runs fast
	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    probeTimeout,
	}
	// Deferred connection shutdown runs before transport t.Cleanup callbacks.

	csid := "test-csid-probe-timeout"

	pconnA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnA: %v", err)
	}
	t.Cleanup(func() { _ = pconnA.Close() })

	if _, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnA, dst: consumer.LocalAddr()}); err != nil {
		t.Fatalf("createOrMigrate path A: %v", err)
	}

	// Path B: a working PacketConn whose writes are silently dropped, so
	// PATH_CHALLENGE never reaches the consumer.
	rawB, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnB: %v", err)
	}
	t.Cleanup(func() { _ = rawB.Close() })
	dropping := &droppingPacketConn{PacketConn: rawB}
	defer closeAllRecords(cm)

	start := time.Now()
	_, _, _, err = cm.createOrMigrate(csid, dialedPconn{PacketConn: dropping, dst: consumer.LocalAddr()})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("createOrMigrate succeeded over a dropping path (expected probe timeout)")
	}
	// Bound timing relative to probeTimeout, with slack on both sides
	// so future bumps to probeTimeout don't require touching this test
	// and CI scheduler jitter doesn't cause flakes:
	//
	//   - lowerSlack covers small early-return paths (e.g. quic-go
	//     short-circuits before the full probeTimeout elapses on a
	//     known-broken path). 100ms is generous enough for the
	//     known-good case while still catching "probe returned
	//     instantly" regressions.
	//   - upperSlack covers ctx setup overhead, scheduler drift, and
	//     the AddPath call that runs before Probe. 2s is a heuristic
	//     that's been stable across local + CI runs.
	const lowerSlack, upperSlack = 100 * time.Millisecond, 2 * time.Second
	if elapsed < probeTimeout-lowerSlack {
		t.Errorf("probe returned in %v, well before probeTimeout=%v (slack=%v)", elapsed, probeTimeout, lowerSlack)
	}
	if elapsed > probeTimeout+upperSlack {
		t.Errorf("probe took %v to return, past probeTimeout=%v+%v slack", elapsed, probeTimeout, upperSlack)
	}

	// And critically: the connection record must still be in the table
	// after a probe failure, so the next migration attempt for the same
	// CSID can try again. Production's egress takes the
	// "outside-in tunnel collapse" path on AcceptStream error, then
	// deletes the record after the migration window — but a probe
	// timeout alone does not delete the record.
	cm.mx.Lock()
	_, present := cm.connections[csid]
	cm.mx.Unlock()
	if !present {
		t.Errorf("connection record gone from cm.connections after probe failure; cannot retry migration")
	}
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{1, 0, 0, 1, 0})
}

// The production adapter must hide a dead donor's errors long enough to move
// the existing stream onto a replacement. A successful Probe alone cannot prove
// this: require delivery of the entire payload after a period with no donor.
func TestConnectionManager_Migration_DonorLossResumesStream(t *testing.T) {
	t.Run("upload", func(t *testing.T) { testDonorLossResumesStream(t, false, 1) })
	t.Run("download", func(t *testing.T) { testDonorLossResumesStream(t, true, 1) })
}

func TestConnectionManager_Migration_RepeatedDonorLoss(t *testing.T) {
	t.Run("upload", func(t *testing.T) { testDonorLossResumesStream(t, false, 8) })
	t.Run("download", func(t *testing.T) { testDonorLossResumesStream(t, true, 8) })
}

func testDonorLossResumesStream(t *testing.T, download bool, hops int) {
	before := migrationSnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(15*hops)*time.Second)
	defer cancel()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	transport := &quic.Transport{Conn: pc}
	listener, err := transport.Listen(testServerTLS(), &common.QUICCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	t.Cleanup(func() { _ = listener.Close() })
	cm := &connectionManager{
		connections: map[string]*connectionRecord{}, tlsConfig: testClientTLS(),
		migrationWindow: 5 * time.Second, probeTimeout: 5 * time.Second,
	}
	pathA, disconnectA := migrationDonor(t, pc.LocalAddr())
	conn, _, _, err := cm.createOrMigrate("donor-loss", pathA)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAllRecords(cm)
	consumer, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.CloseWithError(0, "test cleanup") })
	stream, err := consumer.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	prefix := []byte("transfer started on donor A")
	if _, err := stream.Write(prefix); err != nil {
		t.Fatal(err)
	}
	received, err := conn.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := received.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	gotPrefix := make([]byte, len(prefix))
	if _, err := io.ReadFull(received, gotPrefix); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prefix, gotPrefix) {
		t.Fatal("prefix corrupted")
	}
	sender, receiver := stream, received
	if download {
		sender, receiver = received, stream
		if _, err := sender.Write(prefix); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(receiver, gotPrefix); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(prefix, gotPrefix) {
			t.Fatal("download prefix corrupted")
		}
	}
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{})

	for hop := 0; hop < hops; hop++ {
		t.Logf("replacing donor %d/%d", hop+1, hops)
		for _, s := range []*quic.Stream{sender, receiver} {
			if err := s.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
				t.Fatalf("hop %d deadline: %v", hop+1, err)
			}
		}
		disconnectA()
		select {
		case <-pathA.readError:
		case <-ctx.Done():
			t.Fatal("egress did not observe donor loss")
		}
		// Exceed stream buffering so this transfer cannot finish on local Write
		// success alone. The receiver must acknowledge and verify every byte.
		payload := bytes.Repeat([]byte("payload through replacement donor\n"), 65536)
		written := make(chan error, 1)
		go func() {
			_, err := sender.Write(payload)
			written <- err
		}()
		type readResult struct {
			data []byte
			err  error
		}
		read := make(chan readResult, 1)
		go func() {
			data := make([]byte, len(payload))
			_, err := io.ReadFull(receiver, data)
			read <- readResult{data, err}
		}()
		select {
		case result := <-read:
			t.Fatalf("transfer ended without replacement: %v (%d bytes)", result.err, len(result.data))
		case <-time.After(200 * time.Millisecond):
		}
		pathB, disconnectB := migrationDonor(t, pc.LocalAddr())
		migrated, _, _, err := cm.createOrMigrate("donor-loss", pathB)
		if err != nil {
			t.Fatal(err)
		}
		if migrated != conn {
			t.Fatal("replacement dialed a new QUIC connection")
		}
		select {
		case result := <-read:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if !bytes.Equal(result.data, payload) {
				t.Fatalf("resumed transfer corrupted: got %d bytes, want %d", len(result.data), len(payload))
			}
		case <-ctx.Done():
			t.Fatal("original stream did not resume after migration")
		}
		select {
		case err := <-written:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("sender did not finish")
		}
		pathA, disconnectA = pathB, disconnectB
	}
	if err := receiver.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	extra, err := io.ReadAll(receiver)
	if err != nil || len(extra) != 0 {
		t.Fatalf("unexpected trailing data: %d bytes, %v", len(extra), err)
	}
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{int64(hops), int64(hops), 0, 0, 0})
}

// Bridge the production WebSocket framing to a loopback QUIC consumer. Each
// invocation has a distinct UDP address, just as each donor supplies a new path.
func migrationDonor(t *testing.T, consumer net.Addr) (*errorlessWebSocketPacketConn, func()) {
	t.Helper()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err == nil {
			accepted <- ws
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.CloseNow() })
	var ws *websocket.Conn
	select {
	case ws = <-accepted:
	case <-ctx.Done():
		t.Fatal("WebSocket accept timed out")
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	peer.SetReadLimit(1 << 20)
	ws.SetReadLimit(1 << 20)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			_, b, err := peer.Read(context.Background())
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
	go func() {
		defer workers.Done()
		buf := make([]byte, 65536)
		for {
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if peer.Write(context.Background(), websocket.MessageBinary, buf[:n]) != nil {
				return
			}
		}
	}()
	var once sync.Once
	disconnect := func() {
		once.Do(func() {
			_ = peer.CloseNow()
			_ = udp.Close()
			workers.Wait()
		})
	}
	t.Cleanup(disconnect)
	return &errorlessWebSocketPacketConn{
		w: ws, addr: common.DebugAddr(udp.LocalAddr().String()),
		tcpAddr:   &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: udp.LocalAddr().(*net.UDPAddr).Port},
		keepalive: time.Minute, readError: make(chan error, 1),
	}, disconnect
}

var migrationOutcomes = [...]migrationOutcome{migrationAttempt, migrationSuccess, migrationAddPathError, migrationProbeError, migrationSwitchError}

func migrationSnapshot() (counts [len(migrationOutcomes)]int64) {
	eachMigration(func(outcome migrationOutcome, count int64) {
		for i, label := range migrationOutcomes {
			if label == outcome {
				counts[i] = count
			}
		}
	})
	return
}

func assertMigrationDelta(t *testing.T, before, want [len(migrationOutcomes)]int64) {
	t.Helper()
	for i, count := range migrationSnapshot() {
		if got := count - before[i]; got != want[i] {
			t.Errorf("migration %s: got %d, want %d", migrationOutcomes[i], got, want[i])
		}
	}
}

// dialedPconn wraps a net.PacketConn so every write is redirected to a
// fixed remote (the consumer's UDP loopback address) regardless of the
// addr argument. Required because the egress's transport.Dial passes
// `common.DebugAddr("NELSON WUZ HERE")` as the destination — production's
// errorlessWebSocketPacketConn ignores that argument because the WS only
// has one peer, but a real UDP PacketConn would try to resolve "NELSON
// WUZ HERE" as an address and fail. The wrapper ports the
// "single-destination, ignore the addr arg" semantic into the test.
type dialedPconn struct {
	net.PacketConn
	dst net.Addr
}

func (d dialedPconn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return d.PacketConn.WriteTo(p, d.dst)
}

// droppingPacketConn forwards reads but silently swallows all writes,
// simulating a path that's accepted at the IP layer but doesn't carry
// PATH_CHALLENGE to the far end.
type droppingPacketConn struct {
	net.PacketConn
}

func (d *droppingPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return len(p), nil
}

// startTestConsumer runs a quic.Listen on a UDP loopback socket with a
// minimal accept loop that just consumes incoming streams without
// echoing. It exists so that the egress's transport.Dial can complete
// the handshake — without an active server, the dial itself would hang.
func startTestConsumer(t *testing.T) (net.PacketConn, *tls.Config) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket consumer: %v", err)
	}
	serverTLS := testServerTLS()
	tr := &quic.Transport{Conn: pc}
	listener, err := tr.Listen(serverTLS, &common.QUICCfg)
	if err != nil {
		_ = pc.Close()
		t.Fatalf("Transport.Listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go func(c *quic.Conn) {
				for {
					s, err := c.AcceptStream(context.Background())
					if err != nil {
						return
					}
					// Drain in the background; we don't echo because
					// the migration test never reads.
					go func(stream *quic.Stream) {
						_ = stream.SetReadDeadline(time.Now().Add(30 * time.Second))
						buf := make([]byte, 4096)
						for {
							_, err := stream.Read(buf)
							if err != nil {
								return
							}
						}
					}(s)
				}
			}(conn)
		}
	}()

	return pc, serverTLS
}

// openWritableStream opens a unidirectional or bidirectional stream and
// writes a small payload to confirm the connection is operational.
// Returns the stream so the caller can decide when to close it.
func openWritableStream(t *testing.T, conn *quic.Conn) (*quic.Stream, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Write([]byte("ping")); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// closeAllRecords closes any QUIC connections still tracked in cm so
// they don't outlive the test goroutine.
//
// The records are detached under cm.mx and only then locked
// individually, which is the order deleteConnection uses; holding cm.mx
// across record.mx would invert it. connection is read under record.mx
// because createOrMigrate writes it there — a test whose handler is
// still mid-dial at cleanup races otherwise — and may still be nil when
// that dial has not returned.
func closeAllRecords(cm *connectionManager) {
	cm.mx.Lock()
	records := make([]*connectionRecord, 0, len(cm.connections))
	for csid, r := range cm.connections {
		records = append(records, r)
		delete(cm.connections, csid)
	}
	cm.mx.Unlock()

	for _, r := range records {
		r.mx.Lock()
		if r.connection != nil {
			_ = r.connection.CloseWithError(0, "test cleanup")
		}
		r.mx.Unlock()
	}
}

// --- minimal self-signed TLS helpers, scoped to this test file ----------

var (
	testTLSOnce  sync.Once
	testTLSCert  tls.Certificate
	testTLSError error
)

func testServerTLS() *tls.Config {
	testTLSOnce.Do(generateTestCert)
	if testTLSError != nil {
		panic(testTLSError)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{testTLSCert},
		NextProtos:   []string{"broflake"},
	}
}

func testClientTLS() *tls.Config {
	testTLSOnce.Do(generateTestCert)
	if testTLSError != nil {
		panic(testTLSError)
	}
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"broflake"},
	}
}

func generateTestCert() {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		testTLSError = err
		return
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "egress-migration-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		testTLSError = err
		return
	}
	testTLSCert = tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
}

func TestConnectionManagerSessionLocksAreIndependent(t *testing.T) {
	cm := &connectionManager{connections: map[string]*connectionRecord{}}
	busy := cm.lockRecord("busy")
	defer busy.mx.Unlock()
	waiting := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(waiting)
		cm.deleteOnQUICFailure("busy", nil)
		close(done)
	}()
	<-waiting
	other := make(chan *connectionRecord, 1)
	go func() { other <- cm.lockRecord("unrelated") }()
	select {
	case record := <-other:
		record.mx.Unlock()
	case <-time.After(time.Second):
		t.Fatal("busy session blocked an unrelated session")
	}
	// Releasing busy lets expiry finish without deleting an uninitialized record.
	busy.mx.Unlock()
	<-done
	busy.mx.Lock()
}

func TestConnectionManager_Migration_FailedProbesThenRecovery(t *testing.T) {
	before := migrationSnapshot()
	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })
	cm := &connectionManager{
		connections: map[string]*connectionRecord{}, tlsConfig: testClientTLS(),
		migrationWindow: 5 * time.Second, probeTimeout: 250 * time.Millisecond,
	}
	defer closeAllRecords(cm)
	newSocket := func() net.PacketConn {
		t.Helper()
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		return pc
	}
	const csid = "failed-probes-then-recovery"
	original, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: newSocket(), dst: consumer.LocalAddr()})
	if err != nil {
		t.Fatal(err)
	}
	// Assumes the pinned fork's unexported MaxActiveConnectionIDs is 4.
	// Exceed that pool before trying a healthy replacement; recheck on fork upgrades.
	const failures = 6
	for attempt := 0; attempt < failures; attempt++ {
		dropping := &droppingPacketConn{PacketConn: newSocket()}
		_, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: dropping, dst: consumer.LocalAddr()})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed probe %d: got %v, want probe timeout", attempt+1, err)
		}
	}
	cm.probeTimeout = 3 * time.Second
	recovered, _, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: newSocket(), dst: consumer.LocalAddr()})
	if err != nil {
		t.Fatalf("healthy replacement after %d failed probes: %v", failures, err)
	}
	if recovered != original {
		t.Fatal("recovery replaced the QUIC connection")
	}
	stream, err := openWritableStream(t, recovered)
	if err != nil {
		t.Fatalf("open stream after recovery: %v", err)
	}
	_ = stream.Close()
	assertMigrationDelta(t, before, [len(migrationOutcomes)]int64{failures + 1, 1, 0, failures, 0})
}

func TestConnectionManager_Migration_StaleDonorCleanup(t *testing.T) {
	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })
	cm := &connectionManager{
		connections: map[string]*connectionRecord{}, tlsConfig: testClientTLS(),
		migrationWindow: 5 * time.Second, probeTimeout: 3 * time.Second,
	}
	defer closeAllRecords(cm)
	const csid = "stale-donor-cleanup"
	pathA, disconnectA := migrationDonor(t, consumer.LocalAddr())
	conn, donorA, _, err := cm.createOrMigrate(csid, pathA)
	if err != nil {
		t.Fatal(err)
	}
	pathB, _ := migrationDonor(t, consumer.LocalAddr())
	migrated, donorB, _, err := cm.createOrMigrate(csid, pathB)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != conn || donorB == donorA {
		t.Fatal("migration must preserve the connection and replace the donor identity")
	}
	// A reports its read error only AFTER B has successfully migrated.
	disconnectA()
	select {
	case <-pathA.readError:
	case <-time.After(3 * time.Second):
		t.Fatal("old donor did not report its late read error")
	}
	cm.deleteIfCurrent(csid, conn, donorA)
	assertCurrent := func(expected *quic.Conn, donor *quic.Transport) {
		t.Helper()
		cm.mx.Lock()
		record := cm.connections[csid]
		cm.mx.Unlock()
		if record == nil {
			t.Fatal("stale cleanup removed the current connection")
		}
		record.mx.Lock()
		matches := record.connection == expected && record.transport.Load() == donor
		record.mx.Unlock()
		if !matches {
			t.Fatal("stale cleanup removed or replaced the current connection")
		}
		if err := expected.Context().Err(); err != nil {
			t.Fatalf("stale cleanup closed the current connection: %v", err)
		}
	}
	assertCurrent(conn, donorB)
	cm.deleteIfCurrent(csid, conn, nil)
	cm.deleteIfCurrent(csid, nil, donorB)
	cm.deleteOnQUICFailure(csid, nil)
	assertCurrent(conn, donorB)

	// The current donor must still be able to expire its own connection.
	cm.deleteIfCurrent(csid, conn, donorB)
	cm.mx.Lock()
	remaining := cm.connections[csid]
	cm.mx.Unlock()
	if remaining != nil {
		t.Fatal("current donor cleanup did not remove its connection")
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("current donor cleanup did not close its connection")
	}

	// The same session ID can be reused before old handlers finish teardown.
	pathC, _ := migrationDonor(t, consumer.LocalAddr())
	replacement, donorC, _, err := cm.createOrMigrate(csid, pathC)
	if err != nil {
		t.Fatal(err)
	}
	cm.deleteIfCurrent(csid, conn, donorB)
	cm.deleteOnQUICFailure(csid, conn)
	assertCurrent(replacement, donorC)
	cm.deleteOnQUICFailure(csid, replacement)
	cm.mx.Lock()
	remaining = cm.connections[csid]
	cm.mx.Unlock()
	if remaining != nil {
		t.Fatal("QUIC failure cleanup did not remove its own connection")
	}
}
