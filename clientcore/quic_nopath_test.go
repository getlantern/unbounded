package clientcore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// newGuardedLayer returns a QUICLayer guarded over a single consumer slot, whose reporter it also
// returns. Nothing is listened on: tests hand the layer a connection themselves.
func newGuardedLayer(t *testing.T, failAfter time.Duration) (*QUICLayer, func(bool, string)) {
	t.Helper()
	slots := newConsumerSlots(1, nil)
	g := &noPathGuard{slots: slots, failAfter: failAfter}
	q, err := NewQUICLayer(&BroflakeConn{noPath: g}, &tls.Config{})
	if err != nil {
		t.Fatalf("NewQUICLayer: %v", err)
	}
	t.Cleanup(q.Close)
	return q, slots.reporter(0)
}

// dialWithin dials q, bounded by d.
func dialWithin(q *QUICLayer, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	conn, err := q.DialContext(ctx)
	if conn != nil {
		conn.Close()
	}
	return err
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNoPathGuardFailsDialsAfterGrace(t *testing.T) {
	const grace = 100 * time.Millisecond
	start := time.Now()
	q, report := newGuardedLayer(t, grace)

	// Within the grace a dial waits for a connection, as it always has.
	if err := dialWithin(q, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial within the grace: got %v, want it to wait out its context", err)
	}

	// A dial waiting when the grace runs out is woken with ErrNoConnectedPath.
	if err := dialWithin(q, 5*time.Second); !errors.Is(err, ErrNoConnectedPath) {
		t.Fatalf("waiting dial: got %v, want ErrNoConnectedPath", err)
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Fatalf("dials failed after %v, before the %v grace", elapsed, grace)
	}

	// After it, dials fail at once.
	failedAt := time.Now()
	if err := dialWithin(q, 5*time.Second); !errors.Is(err, ErrNoConnectedPath) {
		t.Fatalf("dial after the grace: got %v, want ErrNoConnectedPath", err)
	}
	if elapsed := time.Since(failedAt); elapsed > grace {
		t.Fatalf("dial after the grace took %v to fail", elapsed)
	}

	// A slot connecting lets dials wait for a connection again.
	report(true, "")
	eventually(t, "dials to wait again", func() bool {
		return errors.Is(dialWithin(q, 10*time.Millisecond), context.DeadlineExceeded)
	})

	// And when it drops, they get the grace again before failing.
	report(false, "")
	droppedAt := time.Now()
	if err := dialWithin(q, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial within the grace after a drop: got %v, want it to wait out its context", err)
	}
	if err := dialWithin(q, 5*time.Second); !errors.Is(err, ErrNoConnectedPath) {
		t.Fatalf("dial after a drop: got %v, want ErrNoConnectedPath", err)
	}
	if elapsed := time.Since(droppedAt); elapsed < grace-10*time.Millisecond {
		t.Fatalf("dials failed %v after the drop, before the %v grace", elapsed, grace)
	}
}

func TestNoPathGuardBriefDropDoesNotFail(t *testing.T) {
	const grace = 150 * time.Millisecond
	q, report := newGuardedLayer(t, grace)
	report(true, "")
	time.Sleep(grace) // past the startup grace, but connected

	// A drop shorter than the grace never fails a dial.
	report(false, "")
	time.Sleep(grace / 3)
	report(true, "")
	time.Sleep(grace)
	if err := dialWithin(q, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial after a brief drop: got %v, want it to wait out its context", err)
	}
}

func TestUnguardedLayerDialsWait(t *testing.T) {
	q, err := NewQUICLayer(&BroflakeConn{}, &tls.Config{})
	if err != nil {
		t.Fatalf("NewQUICLayer: %v", err)
	}
	defer q.Close()
	if q.noPath != nil || q.pathDown != nil {
		t.Fatal("a layer without a guard has guard state")
	}
	if err := dialWithin(q, 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unguarded dial: got %v, want it to wait out its context", err)
	}
}

func TestNoPathGuardResetsStreamsKeepsConn(t *testing.T) {
	const grace = 100 * time.Millisecond
	q, report := newGuardedLayer(t, grace)
	report(true, "")

	conn := loopbackQUICConn(t)
	q.mx.Lock()
	q.eventualConn.set(conn)
	q.mx.Unlock()

	stream, err := q.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer stream.Close()
	// Opening a stream sends nothing, so the peer never answers this read:
	// it waits as a read on a dead path does.
	readErr := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 1))
		readErr <- err
	}()

	report(false, "")
	droppedAt := time.Now()
	select {
	case err := <-readErr:
		var se *quic.StreamError
		if !errors.As(err, &se) || se.ErrorCode != noPathQUICCode || se.Remote {
			t.Fatalf("read on a stream with no path: got %v, want a local reset with code %d", err, noPathQUICCode)
		}
		if elapsed := time.Since(droppedAt); elapsed < grace-10*time.Millisecond {
			t.Fatalf("stream reset %v after the drop, before the %v grace", elapsed, grace)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stream with no path was never reset")
	}
	q.smx.Lock()
	tracked := len(q.streams)
	q.smx.Unlock()
	if tracked != 0 {
		t.Fatalf("%d streams still tracked after the reset", tracked)
	}

	// The connection itself stays up, for a returning path to migrate it onto.
	time.Sleep(3 * grace)
	if conn.Context().Err() != nil {
		t.Fatal("the guard closed the QUIC connection")
	}
}

func TestNoPathGuardFailsDialWaitingForStreamCredit(t *testing.T) {
	const grace = 100 * time.Millisecond
	q, report := newGuardedLayer(t, grace)
	report(true, "")

	// The peer grants one stream, so a second dial waits in OpenStreamSync
	// for credit the peer never sends.
	conn := loopbackQUICConnWith(t, &quic.Config{MaxIncomingStreams: 1})
	q.mx.Lock()
	q.eventualConn.set(conn)
	q.mx.Unlock()
	first, err := q.DialContext(context.Background())
	if err != nil {
		t.Fatalf("first DialContext: %v", err)
	}
	defer first.Close()

	dialErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := q.DialContext(ctx)
		if c != nil {
			c.Close()
		}
		dialErr <- err
	}()
	select {
	case err := <-dialErr:
		t.Fatalf("second dial returned %v with the path still up, want it to wait for stream credit", err)
	case <-time.After(2 * grace):
	}

	report(false, "")
	select {
	case err := <-dialErr:
		if !errors.Is(err, ErrNoConnectedPath) {
			t.Fatalf("dial waiting for stream credit on a failed path: got %v, want ErrNoConnectedPath", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a dial waiting for stream credit outlived the path")
	}
}

func TestNoPathGuardFailsSOCKS5Handshake(t *testing.T) {
	const grace = 100 * time.Millisecond
	q, report := newGuardedLayer(t, grace)
	report(true, "")

	// The peer never answers, so the handshake waits on its greeting reply
	// when the path fails.
	conn := loopbackQUICConn(t)
	q.mx.Lock()
	q.eventualConn.set(conn)
	q.mx.Unlock()

	dial := CreateSOCKS5Dialer(q)
	dialErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := dial(ctx, "tcp", "example.com:443")
		if c != nil {
			c.Close()
		}
		dialErr <- err
	}()
	select {
	case err := <-dialErr:
		t.Fatalf("SOCKS5 dial returned %v with the path still up, want it to wait for the egress", err)
	case <-time.After(2 * grace):
	}

	report(false, "")
	select {
	case err := <-dialErr:
		if !errors.Is(err, ErrNoConnectedPath) {
			t.Fatalf("SOCKS5 handshake on a failed path: got %v, want ErrNoConnectedPath", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a SOCKS5 handshake outlived the path")
	}
}

func TestNoPathGuardSlotReturningKeepsConn(t *testing.T) {
	const grace = 50 * time.Millisecond
	q, report := newGuardedLayer(t, grace)
	report(true, "")
	conn := loopbackQUICConn(t)
	q.mx.Lock()
	q.eventualConn.set(conn)
	q.mx.Unlock()

	report(false, "")
	eventually(t, "the path to fail", func() bool {
		return errors.Is(dialWithin(q, time.Second), ErrNoConnectedPath)
	})
	// A slot returning keeps the connection, which the egress migrates onto
	// it, and dials use it again.
	report(true, "")
	time.Sleep(2 * grace)
	if conn.Context().Err() != nil {
		t.Fatal("the QUIC connection closed although a slot reconnected")
	}
	if err := dialWithin(q, time.Second); err != nil {
		t.Fatalf("dial after the slot returned: %v", err)
	}
}

func TestConsumerConnectedSince(t *testing.T) {
	slots := newConsumerSlots(2, nil)
	b := &BroflakeEngine{consumerSlots: slots}
	// Still making its first connection: since is zero.
	if connected, since := b.ConsumerConnected(); connected || !since.IsZero() {
		t.Fatalf("new slots: got (%v, %v), want (false, zero)", connected, since)
	}
	slots.reporter(0)(false, "")
	if connected, since := b.ConsumerConnected(); connected || !since.IsZero() {
		t.Fatalf("a slot failed its first attempt: got (%v, %v), want (false, zero)", connected, since)
	}

	before := time.Now()
	slots.reporter(0)(true, "")
	connected, up := b.ConsumerConnected()
	if !connected || up.Before(before) {
		t.Fatalf("a slot connected: got (%v, %v), want connected since %v or later", connected, up, before)
	}

	// Another slot connecting, or one of two dropping, changes nothing.
	time.Sleep(time.Millisecond)
	slots.reporter(1)(true, "")
	slots.reporter(0)(false, "")
	if connected, since := b.ConsumerConnected(); !connected || !since.Equal(up) {
		t.Fatalf("one of two slots dropped: got (%v, %v), want (true, %v)", connected, since, up)
	}

	time.Sleep(time.Millisecond)
	slots.reporter(1)(false, "")
	if connected, since := b.ConsumerConnected(); connected || !since.After(up) {
		t.Fatalf("the last slot dropped: got (%v, %v), want disconnected since after %v", connected, since, up)
	}

	if connected, since := (&BroflakeEngine{}).ConsumerConnected(); connected || !since.IsZero() {
		t.Fatalf("widget: got (%v, %v), want (false, zero)", connected, since)
	}
}

func TestNewNoPathGuard(t *testing.T) {
	slots := newConsumerSlots(1, nil)
	if g := newNoPathGuard(slots, -1); g != nil {
		t.Fatal("a negative grace must turn the guard off")
	}
	g := newNoPathGuard(slots, 0)
	if g == nil || g.failAfter != defaultConsumerNoPathGrace {
		t.Fatalf("zero grace: got %+v, want the default", g)
	}
	if g := newNoPathGuard(slots, time.Second); g == nil || g.failAfter != time.Second {
		t.Fatalf("1s grace: got %+v", g)
	}
}

// TestAcceptConnsReplacesOldConn: the egress dials a new connection only after dropping the old
// one, so a layer that accepts one while its old one still looks alive switches to it at once and
// closes the old one, rather than leave it queued while dials hang on the dead one.
func TestAcceptConnsReplacesOldConn(t *testing.T) {
	// An unguarded (pool) layer: this applies to every consumer.
	q, err := NewQUICLayer(&BroflakeConn{}, &tls.Config{})
	if err != nil {
		t.Fatalf("NewQUICLayer: %v", err)
	}
	defer q.Close()
	serverTLS, clientTLS := testTLSConfigs(t)
	ln, err := quic.ListenAddr("127.0.0.1:0", serverTLS, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go q.acceptConns(ln)

	current := func() *quic.Conn {
		q.mx.RLock()
		defer q.mx.RUnlock()
		return q.eventualConn.current()
	}
	dial := func() *quic.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := quic.DialAddr(ctx, ln.Addr().String(), clientTLS, &quic.Config{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseWithError(0, "") })
		return conn
	}

	// The "egress" dials a first connection, which the layer adopts.
	dial()
	eventually(t, "the first connection", func() bool { return current() != nil })
	old := current()

	// It drops it on its side without our hearing, and dials a second.
	peer := dial()
	eventually(t, "the second connection to replace the first", func() bool {
		c := current()
		return c != nil && c != old
	})
	replacement := current()
	select {
	case <-old.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced connection was never closed")
	}
	// Its ending must not unset its replacement.
	time.Sleep(50 * time.Millisecond)
	if current() != replacement {
		t.Fatal("the replaced connection's ending unset the current connection")
	}

	// Dials use the new connection.
	conn, err := q.DialContext(context.Background())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := peer.AcceptStream(ctx); err != nil {
		t.Fatalf("the new connection's peer never saw the dial's stream: %v", err)
	}

	// When the current connection ends with nothing to replace it, dials wait for the next.
	peer.CloseWithError(0, "")
	eventually(t, "the ended connection to be unset", func() bool { return current() == nil })
}

// testTLSConfigs returns a server TLS config with a throwaway self-signed certificate, and a
// client config that accepts it.
func testTLSConfigs(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server = &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"test"},
	}
	// Test-only: the client trusts the throwaway certificate above.
	client = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test"}}
	return server, client
}

// loopbackQUICConn returns the client end of a QUIC connection over loopback UDP, whose server
// end accepts it and nothing more.
func loopbackQUICConn(t *testing.T) *quic.Conn {
	t.Helper()
	return loopbackQUICConnWith(t, &quic.Config{})
}

// loopbackQUICConnWith is loopbackQUICConn with the server end configured by serverCfg.
func loopbackQUICConnWith(t *testing.T, serverCfg *quic.Config) *quic.Conn {
	t.Helper()
	serverTLS, clientTLS := testTLSConfigs(t)
	ln, err := quic.ListenAddr("127.0.0.1:0", serverTLS, serverCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			if _, err := ln.Accept(context.Background()); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, ln.Addr().String(), clientTLS, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseWithError(0, "") })
	return conn
}
