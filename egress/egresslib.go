package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/telemetry"
)

// TODO: rate limiters and fancy settings and such:
// https://github.com/nhooyr/websocket/blob/master/examples/echo/server.go

const (
	websocketKeepalive = 15 * time.Second
)

// Multi-writer values used for logging and otel metrics
// nClients is the number of open WebSocket connections
var nClients uint64

// nQUICStreams is the number of open QUIC streams (not to be confused with QUIC connections)
var nQUICStreams uint64

// nIngressBytes is the number of bytes received over all WebSocket connections since the last otel measurement callback
var nIngressBytes uint64

// Otel instruments
var nClientsCounter metric.Int64UpDownCounter

// TODO: weirdly, we report the number of open QUIC conections to otel but we don't maintain an atomic value to log it?
var nQUICConnectionsCounter metric.Int64UpDownCounter
var nQUICStreamsCounter metric.Int64UpDownCounter
var nIngressBytesCounter metric.Int64ObservableUpDownCounter

type connectionRecord struct {
	mx           sync.Mutex
	connection   *quic.Connection
	lastMigrated time.Time
	lastPath     *quic.Path
}

// migrationWindow: when migrating from WebSocket A to B, how long should we wait after WebSocket
// A goes away for WebSocket B to appear, before giving up and deleting the QUIC connection state?
// Setting this value larger than your quic.Config's MaxIdleTimeout will break things, because that
// will create scenarios where we attempt to migrate a QUIC connection that has timed out and closed.

// probeTimeout: during migration, how long should we wait for a probe response before giving up?
// This should be set to a larger value than migrationWindow. This ensures that upon migration
// failure, QUIC connection state is deleted from the connection manager before a second attempt
// is made.
type connectionManager struct {
	mx              sync.Mutex
	connections     map[string]*connectionRecord
	tlsConfig       *tls.Config
	migrationWindow time.Duration
	probeTimeout    time.Duration
}

func (manager *connectionManager) deleteIfNotMigratedSince(csid string, t time.Time) {
	manager.mx.Lock()

	record, ok := manager.connections[csid]

	if !ok {
		manager.mx.Unlock()
		return
	}

	record.mx.Lock()

	if !record.lastMigrated.After(t) {
		(*record.connection).CloseWithError(quic.ApplicationErrorCode(42069), "expired before migration")
		nQUICConnectionsCounter.Add(context.Background(), -1)
		delete(manager.connections, csid)
		common.Debugf("QUIC connection for CSID %v expired, closed, and deleted", csid)
	}

	record.mx.Unlock()
	manager.mx.Unlock()
}

func (manager *connectionManager) createOrMigrate(csid string, pconn *errorlessWebSocketPacketConn) (*quic.Connection, error) {
	manager.mx.Lock()

	transport := &quic.Transport{Conn: pconn}
	record, ok := manager.connections[csid]

	// Atomic creation path
	if !ok {
		common.Debugf("No existing QUIC connection for %v [CSID: %v], dialing...", pconn.addr, csid)
		newConn, err := transport.Dial(
			context.Background(),
			common.DebugAddr("NELSON WUZ HERE"),
			manager.tlsConfig,
			&common.QUICCfg,
		)

		if err != nil {
			manager.mx.Unlock()
			return nil, err
		}

		nQUICConnectionsCounter.Add(context.Background(), 1)
		common.Debugf("%v dialed a new QUIC connection!", pconn.addr)
		manager.connections[csid] = &connectionRecord{connection: &newConn, lastMigrated: time.Now()}
		manager.mx.Unlock()
		return &newConn, nil
	}

	// Atomic migration path
	common.Debugf("Trying to migrate QUIC connection for %v [CSID %v]", pconn.addr, csid)
	t1 := time.Now()
	record.mx.Lock()
	manager.mx.Unlock()
	defer record.mx.Unlock()

	path, err := (*record.connection).AddPath(transport)
	if err != nil {
		return nil, fmt.Errorf("AddPath error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), manager.probeTimeout)
	defer cancel()
	err = path.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("path probe error: %v", err)
	}

	err = path.Switch()
	if err != nil {
		return nil, fmt.Errorf("path switch error: %v", err)
	}

	t2 := time.Now()
	common.Debugf("Migrated a QUIC connection to %v! (took %vs)", pconn.addr, t2.Sub(t1).Seconds())
	record.lastMigrated = time.Now()

	if record.lastPath != nil {
		err = (*record.lastPath).Close()

		// If we encounter an error closing the last path, we still proceed with a successful migration
		if err != nil {
			common.Debugf("Error closing last path for %v: %v", pconn.addr, err)
		} else {
			common.Debugf("Closed old path for %v", pconn.addr)
		}
	}

	record.lastPath = path
	return record.connection, nil
}

// errorlessWebSocketPacketConn adapts a websocket.Conn for use as a net.PacketConn, and it performs
// one additional trick: if its underlying websocket.Conn is closed such that reads and writes
// return errors, it will intercept and hide those errors from the caller. The purpose of this
// functionality is to enable QUIC connection migration from WebSocket A to B, when B is not yet
// known at the moment that A disconnects. Put another way: if you establish a QUIC connection over
// an errorlessWebSocketPacketConn, the underlying WebSocket can disconnect and go away, but your
// QUIC connection will not close, because its transport sneakily hides the read and write errors.
// This gives you an opportunity to migrate your QUIC connection to a new errorlessWebSocketPacketConn
// at some point in the future. Intercepted *read* errors are sent over the readError channel.
// Currently, intercepted *write* errors are simply discarded.
type errorlessWebSocketPacketConn struct {
	w         *websocket.Conn
	addr      net.Addr
	keepalive time.Duration
	tcpAddr   *net.TCPAddr
	readError chan error
}

func (q errorlessWebSocketPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	// TODO: The channel and goroutine we fire off here are used to implement serverside keepalive.
	// For as long as we're reading from this WebSocket, if we haven't received any readable data for
	// a while, we send a ping. Keepalive is only desirable to prevent lots of disconnections
	// and reconnections on idle WebSockets, and so it's worth asking whether the cycles added by this
	// keepalive logic are worth the overhead we're saving in reduced discon/recon loops. Ultimately,
	// we'd rather implement keepalive on the client side, but that's a much bigger lift. See:
	// https://github.com/getlantern/broflake/issues/127
	readDone := make(chan struct{})

	go func() {
		for {
			select {
			case <-time.After(q.keepalive):
				common.Debugf("%v PING", q.addr)
				q.w.Ping(context.Background())
			case <-readDone:
				return
			}
		}
	}()

	_, b, err := q.w.Read(context.Background())
	readDone <- struct{}{}

	// Intercept and hide errors from the caller
	// TODO: be more specific about which error(s) to hide?
	if err != nil {
		select {
		case q.readError <- err:
		default:
		}

		// If our underlying WebSocket is disconnected, let's just block forever, simulating no data
		// received on the socket. TODO: in a world where we need to implement SetDeadline / SetReadDeadline,
		// we will need to respect those here...
		select {}
	}

	copy(p, b)
	atomic.AddUint64(&nIngressBytes, uint64(len(b)))
	return len(b), q.tcpAddr, err
}

func (q errorlessWebSocketPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	// TODO https://github.com/getlantern/engineering/issues/2437
	unboundedPacket := common.UnboundedPacket{
		SourceAddr: q.addr.String(),
		Payload:    p,
	}

	b, err := json.Marshal(unboundedPacket)
	if err != nil {
		// XXX: it's not clear what the desired behavior is here. Sure, this is an "errorless" PacketConn,
		// but we really only intended to hide errors related to *transport failure* from the caller...
		// *not* serialization errors like this. The right thing is probably to be more specific about
		// which errors to hide, per the comment below. But since we haven't implemented that yet, we'll
		// just hide this error too! I hope you're reading the logs...
		common.Debugf("WriteTo JSON marshaling error (hidden from caller): %v", err)
		return len(p), nil
	}

	err = q.w.Write(context.Background(), websocket.MessageBinary, b)

	// Intercept and hide errors from the caller
	// TODO: be more specific about which error(s) to hide?
	err = nil

	return len(p), err
}

func (q errorlessWebSocketPacketConn) Close() error {
	defer common.Debugf("Closed a WebSocket connection! (%v total)", atomic.AddUint64(&nClients, ^uint64(0)))
	defer nClientsCounter.Add(context.Background(), -1)
	return q.w.Close(websocket.StatusNormalClosure, "")
}

func (q errorlessWebSocketPacketConn) LocalAddr() net.Addr {
	return q.addr
}

func (q errorlessWebSocketPacketConn) SetDeadline(t time.Time) error {
	panic("you've discovered the unimplemented SetDeadline")
}

func (q errorlessWebSocketPacketConn) SetReadDeadline(t time.Time) error {
	panic("you've discovered the unimplemented SetReadDeadline")
}

func (q errorlessWebSocketPacketConn) SetWriteDeadline(t time.Time) error {
	panic("you've discovered the unimplemented SetWriteDeadline")
}

type proxyListener struct {
	net.Listener
	*connectionManager
	connections  chan net.Conn
	addr         net.Addr
	closeMetrics func(ctx context.Context) error
}

func (l proxyListener) Accept() (net.Conn, error) {
	conn := <-l.connections
	return conn, nil
}

func (l proxyListener) Addr() net.Addr {
	return l.addr
}

func (l proxyListener) Close() error {
	err := l.Listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l.closeMetrics(ctx)
	return err
}

func (l proxyListener) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	// TODO: InsecureSkipVerify=true just disables origin checking, we need to instead add origin
	// patterns as strings using AcceptOptions.OriginPattern
	// TODO: disabling compression is a workaround for a WebKit bug:
	// https://github.com/getlantern/broflake/issues/45

	consumerSessionID := r.Header.Get(common.ConsumerSessionIDHeader)
	if consumerSessionID == "" {
		common.Debugf("Refused WebSocket connection, missing consumer session ID")
		return
	}

	c, err := websocket.Accept(
		w,
		r,
		&websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		},
	)
	if err != nil {
		common.Debugf("Error accepting WebSocket connection: %v", err)
		return
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		common.Debugf("Error resolving TCPAddr: %v", err)
		return
	}

	wspconn := errorlessWebSocketPacketConn{
		w:         c,
		addr:      common.DebugAddr(fmt.Sprintf("WebSocket connection %v", uuid.NewString())),
		keepalive: websocketKeepalive,
		tcpAddr:   tcpAddr,
		readError: make(chan error),
	}

	defer wspconn.Close()

	common.Debugf("Accepted a new WebSocket connection! [CSID: %v] (%v total)", consumerSessionID, atomic.AddUint64(&nClients, 1))
	nClientsCounter.Add(context.Background(), 1)

	conn, err := l.connectionManager.createOrMigrate(consumerSessionID, &wspconn)
	if err != nil {
		common.Debugf("createOrMigrate error: %v, closing!", err)
		return
	}

	// Here we enter the steady state for the WebSocket tunnel and continue until there's some reason
	// to tear the tunnel down. An explainer about teardown: teardown begins when we intercept a read
	// error on the errorlessWebSocketPacketConn, which indicates that the underlying websocket.Conn
	// is no longer connected. (See commentary around errorlessWebSocketPacketConn for more context
	// around error interception). When we intercept a read error on the errorlessWebSocketPacketConn,
	// we wait for a bounded duration of time (the "migration window"), and then we delete the QUIC
	// connection state from the connection manager if it has not been migrated within that window.
	// The deletion operation will cause AcceptStream (below) to return an error, which returns from
	// and cleans up the stream handling goroutine. If the QUIC connection DID migrate within the
	// migration window, we keep its state intact, and we forcibly kill the stream handling goroutine
	// for *this WebSocket* by cancelling wsContext. In both cases, we then return from this function,
	// which cleans up the WebSocket resource. In operation, you will observe WebSockets "dangle" for
	// a duration of time equal to migrationWindow, and the total number of WebSocket connections
	// logged by the server will eventually converge to the correct value when the server has quiesced.
	wsContext, wsCancel := context.WithCancel(context.Background())
	QUICLayerError := make(chan struct{}, 1)

	go func() {
		for {
			stream, err := (*conn).AcceptStream(wsContext)

			if err != nil {
				common.Debugf("QUIC AcceptStream error for %v, terminating handler (%v)", wspconn.addr, err)
				QUICLayerError <- struct{}{}
				close(QUICLayerError)
				return
			}

			common.Debugf("Accepted a new QUIC stream! (%v total)", atomic.AddUint64(&nQUICStreams, 1))
			nQUICStreamsCounter.Add(context.Background(), 1)

			l.connections <- common.QUICStreamNetConn{
				Stream: stream,
				OnClose: func() {
					defer common.Debugf("Closed a QUIC stream! (%v total)", atomic.AddUint64(&nQUICStreams, ^uint64(0)))
					nQUICStreamsCounter.Add(context.Background(), -1)
				},
				AddrLocal:  l.addr,
				AddrRemote: tcpAddr,
			}
		}
	}()

	select {
	case <-wspconn.readError:
		// Normal *outside-in* tunnel collapse: on the first read error intercepted at the WebSocket
		// layer, we initiate the migration procedure, delete the inner QUIC layer connection state if
		// necessary, then return from handleWebsocket.
		common.Debugf(
			"%v read error, waiting %vs for migration...",
			wspconn.addr,
			l.connectionManager.migrationWindow.Seconds(),
		)

		t1 := time.Now()
		<-time.After(l.connectionManager.migrationWindow)
		l.connectionManager.deleteIfNotMigratedSince(consumerSessionID, t1)
		wsCancel()
	case <-QUICLayerError:
		// Unexpected *inside-out* tunnel collapse: we should only enter this path if there's a bug. If
		// we're here, it means there was an AcceptStream error on a QUIC connection that we didn't
		// initiate as part of our orderly outside-in tunnel collapse. This can happen, for example,
		// if the QUIC connection times out due to inactivity. To resynchronize, we delete the QUIC
		// connection state and return from handleWebsocket, closing the tunnel completely.
		l.connectionManager.deleteIfNotMigratedSince(consumerSessionID, time.Now().Add(24*time.Hour))
	}
}

func NewListener(ctx context.Context, ll net.Listener, tlsConfig *tls.Config) (net.Listener, error) {
	closeFuncMetric := telemetry.EnableOTELMetrics(ctx)
	m := otel.Meter("github.com/getlantern/broflake/egress")
	var err error
	nClientsCounter, err = m.Int64UpDownCounter("concurrent-websockets")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nQUICConnectionsCounter, err = m.Int64UpDownCounter("concurrent-quic-connections")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nQUICStreamsCounter, err = m.Int64UpDownCounter("concurrent-quic-streams")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nIngressBytesCounter, err = m.Int64ObservableUpDownCounter("ingress-bytes")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	_, err = m.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			b := atomic.LoadUint64(&nIngressBytes)
			o.ObserveInt64(nIngressBytesCounter, int64(b))
			common.Debugf("Ingress bytes: %v", b)
			atomic.StoreUint64(&nIngressBytes, uint64(0))
			return nil
		},
		nIngressBytesCounter,
	)
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	cm := &connectionManager{
		connections:     make(map[string]*connectionRecord),
		tlsConfig:       tlsConfig,
		migrationWindow: 30 * time.Second,
		probeTimeout:    35 * time.Second,
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebSocket connections
	l := proxyListener{
		Listener:          ll,
		connectionManager: cm,
		connections:       make(chan net.Conn, 2048),
		addr:              ll.Addr(),
		closeMetrics:      closeFuncMetric,
	}

	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	http.Handle("/ws", otelhttp.NewHandler(http.HandlerFunc(l.handleWebsocket), "/ws"))
	common.Debugf("Egress server listening for WebSocket connections on %v", ll.Addr())
	go func() {
		err := srv.Serve(ll)
		panic(fmt.Sprintf("stopped listening and serving for some reason: %v", err))
	}()

	return l, nil
}
