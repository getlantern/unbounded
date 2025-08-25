package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
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
	// Our subprotocols should be a slice containing a single comma-separated string. But weird browsers
	// could theoretically send multiple Sec-Websocket-Protocol headers, one for each subprotocol, which
	// would result in a slice containing multiple strings. We handle both cases:
	rawSubprotocols := r.Header[common.SubprotocolsHeader]
	joined := strings.Join(rawSubprotocols, ",")

	subprotocols := []string{}
	for _, sp := range strings.Split(joined, ",") {
		trimmed := strings.TrimSpace(sp)
		if trimmed != "" {
			subprotocols = append(subprotocols, trimmed)
		}
	}

	consumerSessionID, version, ok := common.ParseSubprotocolsRequest(subprotocols)
	if !ok {
		common.Debugf("Refused WebSocket connection, missing subprotocols")
		return
	}

	versionHeader := &http.Header{}
	versionHeader.Add(common.VersionHeader, version)

	if !common.IsValidProtocolVersion(versionHeader) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		common.Debugf("Refused WebSocket connection, bad protocol version")
		return
	}

	// TODO: InsecureSkipVerify=true just disables origin checking, we need to instead add origin
	// patterns as strings using AcceptOptions.OriginPattern
	// TODO: disabling compression is a workaround for a WebKit bug:
	// https://github.com/getlantern/broflake/issues/45

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
			Subprotocols:       common.NewSubprotocolsResponse(),
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
