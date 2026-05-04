package egress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

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
	// Long enough for the test cleanup; failed migrations show up as
	// quic-go errors well before this fires.
	t.Parallel()

	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })

	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
	// closeAllRecords is registered LAST below so it runs FIRST in
	// cleanup (t.Cleanup is LIFO). The order matters: we want QUIC
	// connections closed before their underlying PacketConns disappear,
	// otherwise quic-go's read goroutines observe a vanished transport
	// before they observe the connection close, which produces noisy
	// error logs and occasionally races on the test's assertion path.

	csid := "test-csid-happy"

	// Path A: brand-new CSID → should take the dial branch.
	pconnA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnA: %v", err)
	}
	t.Cleanup(func() { _ = pconnA.Close() })

	connA, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnA, dst: consumer.LocalAddr()})
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
	// Registered after both PacketConns so it runs first (LIFO) and
	// closes the QUIC conns while their transports are still alive.
	t.Cleanup(func() { closeAllRecords(cm) })

	connB, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnB, dst: consumer.LocalAddr()})
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
	t.Parallel()

	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })

	probeTimeout := 1500 * time.Millisecond // short so the test runs fast
	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    probeTimeout,
	}
	// closeAllRecords is registered LAST below so it runs FIRST in
	// cleanup (LIFO); see comment in the happy-path test for why.

	csid := "test-csid-probe-timeout"

	pconnA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket pconnA: %v", err)
	}
	t.Cleanup(func() { _ = pconnA.Close() })

	if _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconnA, dst: consumer.LocalAddr()}); err != nil {
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
	t.Cleanup(func() { closeAllRecords(cm) })

	start := time.Now()
	_, err = cm.createOrMigrate(csid, dialedPconn{PacketConn: dropping, dst: consumer.LocalAddr()})
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
func closeAllRecords(cm *connectionManager) {
	cm.mx.Lock()
	defer cm.mx.Unlock()
	for csid, r := range cm.connections {
		_ = r.connection.CloseWithError(0, "test cleanup")
		delete(cm.connections, csid)
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

