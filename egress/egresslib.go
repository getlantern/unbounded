package egress

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
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
	"github.com/getlantern/quicwrapper"
	"github.com/getlantern/quicwrapper/webt"
	"github.com/getlantern/telemetry"
)

// TODO: rate limiters and fancy settings and such:
// https://github.com/nhooyr/websocket/blob/master/examples/echo/server.go

const (
	websocketKeepalive = 15 * time.Second
)

// Multi-writer values used for logging and otel metrics
// nClients is the number of open WebSocket/WebTransport connections
var nClients uint64

// nQUICStreams is the number of open QUIC streams (not to be confused with QUIC connections)
var nQUICStreams uint64

// nIngressBytes is the number of bytes received over all WebSocket/WebTransport connections since the last otel measurement callback
var nIngressBytes uint64

// Otel instruments
var nClientsCounter metric.Int64UpDownCounter

// TODO: weirdly, we report the number of open QUIC conections to otel but we don't maintain an atomic value to log it?
var nQUICConnectionsCounter metric.Int64UpDownCounter
var nQUICStreamsCounter metric.Int64UpDownCounter
var nIngressBytesCounter metric.Int64ObservableUpDownCounter

// for initializing Otel instruments
var otelOnce sync.Once

func otelInit(ctx context.Context) error {
	var err error
	otelOnce.Do(func() {
		m := otel.Meter("github.com/getlantern/broflake/egress")
		closeFuncMetric := telemetry.EnableOTELMetrics(ctx)

		nClientsCounter, err = m.Int64UpDownCounter("concurrent-connections")
		if err != nil {
			closeFuncMetric(ctx)
			return
		}
		nQUICConnectionsCounter, err = m.Int64UpDownCounter("concurrent-quic-connections")
		if err != nil {
			closeFuncMetric(ctx)
			return
		}
		nQUICStreamsCounter, err = m.Int64UpDownCounter("concurrent-quic-streams")
		if err != nil {
			closeFuncMetric(ctx)
			return
		}

		nIngressBytesCounter, err = m.Int64ObservableUpDownCounter("ingress-bytes")
		if err != nil {
			closeFuncMetric(ctx)
			return
		}

		_, err = m.RegisterCallback(
			func(ctx context.Context, o metric.Observer) error {
				b := atomic.LoadUint64(&nIngressBytes)
				o.ObserveInt64(nIngressBytesCounter, int64(b))
				atomic.StoreUint64(&nIngressBytes, uint64(0))
				return nil
			},
			nIngressBytesCounter,
		)
		if err != nil {
			closeFuncMetric(ctx)
			return
		}
	})
	return err
}

// webSocketPacketConn wraps a websocket.Conn as a net.PacketConn
type websocketPacketConn struct {
	w         *websocket.Conn
	addr      net.Addr
	keepalive time.Duration
	tcpAddr   *net.TCPAddr
}

func (q websocketPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
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
	copy(p, b)
	atomic.AddUint64(&nIngressBytes, uint64(len(b)))
	return len(b), q.tcpAddr, err
}

func (q websocketPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	err = q.w.Write(context.Background(), websocket.MessageBinary, p)
	return len(p), err
}

func (q websocketPacketConn) Close() error {
	defer common.Debugf("Closed a WebSocket connection! (%v total)", atomic.AddUint64(&nClients, ^uint64(0)))
	defer nClientsCounter.Add(context.Background(), -1)
	return q.w.Close(websocket.StatusNormalClosure, "")
}

func (q websocketPacketConn) LocalAddr() net.Addr {
	return q.addr
}

func (q websocketPacketConn) SetDeadline(t time.Time) error      { return nil }
func (q websocketPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (q websocketPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// webtransportPacketConn wraps a net.PacketConn and provides statistics
type webtransportPacketConn struct {
	net.PacketConn
}

func (wtpconn webtransportPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := wtpconn.PacketConn.ReadFrom(p)
	atomic.AddUint64(&nIngressBytes, uint64(n))
	return n, addr, err
}

func (wtpconn webtransportPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return wtpconn.PacketConn.WriteTo(p, addr)
}

func (wtpconn webtransportPacketConn) Close() error {
	defer common.Debugf("Closed a WebTransport connection! (%v total)", atomic.AddUint64(&nClients, ^uint64(0)))
	defer nClientsCounter.Add(context.Background(), -1)
	return wtpconn.PacketConn.Close()
}

func (wtpconn webtransportPacketConn) LocalAddr() net.Addr {
	return wtpconn.PacketConn.LocalAddr()
}

func (wtpconn webtransportPacketConn) SetDeadline(t time.Time) error {
	return wtpconn.PacketConn.SetDeadline(t)
}

func (wtpconn webtransportPacketConn) SetReadDeadline(t time.Time) error {
	return wtpconn.PacketConn.SetReadDeadline(t)
}

func (wtpconn webtransportPacketConn) SetWriteDeadline(t time.Time) error {
	return wtpconn.PacketConn.SetWriteDeadline(t)
}

// proxyListener implements net.Listener and listens for QUIC connections
type proxyListener struct {
	listeners []net.Listener // the listeners associated with it

	mpconn *multiplexedPacketConn // the multiplexed PacketConn which will be used as the QUIC transport

	connections  chan net.Conn
	tlsConfig    *tls.Config
	addr         net.Addr
	closeMetrics func(ctx context.Context) error

	closeOnce sync.Once
}

func (l *proxyListener) Accept() (net.Conn, error) {
	conn, ok := <-l.connections
	if !ok {
		return nil, io.EOF
	}
	return conn, nil
}

func (l *proxyListener) Addr() net.Addr {
	return l.addr
}

func (l *proxyListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.connections)

		// close listeners
		for _, listener := range l.listeners {
			err = errors.Join(err, listener.Close())
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if l.closeMetrics != nil {
			l.closeMetrics(ctx)
		}
	})
	return err
}

// handleWebSocket is a http.HandlerFunc that acepts websocket connections, wraps to a net.PacketConn, and add the connection to the multiplex PacketConn
func (l *proxyListener) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// TODO: InsecureSkipVerify=true just disables origin checking, we need to instead add origin
	// patterns as strings using AcceptOptions.OriginPattern
	// TODO: disabling compression is a workaround for a WebKit bug:
	// https://github.com/getlantern/broflake/issues/45
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
	id := uuid.NewString()
	wspconn := websocketPacketConn{
		w:         c,
		addr:      common.DebugAddr(fmt.Sprintf("WebSocket connection %v", id)),
		keepalive: websocketKeepalive,
		tcpAddr:   tcpAddr,
	}
	close := l.mpconn.AddTunnel(id, wspconn)
	common.Debugf("Accepted a new WebSocket connection %v! (%v total)", id, atomic.AddUint64(&nClients, 1))
	nClientsCounter.Add(context.Background(), 1)

	// wait for the current tunnel to close, which will only happen if wspconn is closed
	<-close
}

// handleWebTransport is a datagram handler that adds the PacketConn to the multiplex PacketConn
func (l *proxyListener) handleWebTransport(pconn net.PacketConn, remoteAddr net.Addr) {
	id := uuid.NewString()
	wtpconn := webtransportPacketConn{pconn}
	close := l.mpconn.AddTunnel(id, wtpconn)
	common.Debugf("Accepted a new WebTransport connection %v! (%v total)", id, atomic.AddUint64(&nClients, 1))
	nClientsCounter.Add(context.Background(), 1)

	// wait for the current tunnel to close, which will only happen if wtpconn is closed
	<-close
}

// listenQUIC starts a QUIC listener on the given PacketConn, and for each accepted quic.Stream wraps it to a net.Conn and send it over the connections channel
func (l *proxyListener) listenQUIC(pc net.PacketConn, quicConfig *quic.Config) {
	tr := &quic.Transport{
		Conn:              pc,
		StatelessResetKey: &quic.StatelessResetKey{}, // enable stateless reset
	}
	listener, err := tr.Listen(l.tlsConfig, quicConfig)
	if err != nil {
		common.Debugf("Unable to start QUIC listener: %v", err)
		return
	}

	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			common.Debugf("QUIC listener error (%v), closing!", err)
			listener.Close()
			break
		}

		var teamId string
		if !quicConfig.EnableDatagrams {
			common.Debugf("QUIC datagrams disabled, no team ID will be recorded")
		} else {
			go func() {
				ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				teamIdBytes, err := conn.ReceiveDatagram(ctxWithTimeout)
				if err != nil {
					common.Debugf("QUIC accepted connection without team ID datagram")
				} else {
					if !strings.HasPrefix(string(teamIdBytes), common.TeamIdPrefix) {
						common.Debug("QUIC datagram was not a teamID")
						teamId = "none"
					} else {
						teamId = strings.TrimPrefix(string(teamIdBytes), common.TeamIdPrefix)
					}
				}
				cancel()
			}()
		}

		nQUICConnectionsCounter.Add(context.Background(), 1)
		common.Debugf("QUIC accepted a new connection (remote addr: %v)", conn.RemoteAddr())

		go func() {
			defer nQUICConnectionsCounter.Add(context.Background(), -1)
			for {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					// We interpret an error while accepting a stream to indicate an unrecoverable error with
					// the QUIC connection, and so we close the QUIC connection altogether
					errString := fmt.Sprintf("QUIC stream error (%v), closing QUIC connection!", err)
					common.Debugf("%v", errString)
					conn.CloseWithError(quic.ApplicationErrorCode(42069), errString)
					return
				}

				common.Debugf("Accepted a new QUIC stream! (%v total)", atomic.AddUint64(&nQUICStreams, 1))
				nQUICStreamsCounter.Add(context.Background(), 1)

				l.connections <- &common.QUICStreamNetConn{
					Stream: stream,
					OnClose: func() {
						defer common.Debugf("Closed a QUIC stream! (%v total)", atomic.AddUint64(&nQUICStreams, ^uint64(0)))
						nQUICStreamsCounter.Add(context.Background(), -1)
					},
					AddrLocal:  l.addr,
					AddrRemote: conn.RemoteAddr(),
					TeamId:     teamId,
				}
			}
		}()
	}
}

// NewListenerFromPacketConn starts a QUIC listener on the given PacketConn and returns the QUIC listener
func NewListenerFromPacketConn(ctx context.Context, pc net.PacketConn, certPEM, keyPEM string) (net.Listener, error) {
	if err := otelInit(ctx); err != nil {
		return nil, err
	}
	tlsConfig, err := tlsConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to load tlsconfig %w", err)
	}

	l := &proxyListener{
		connections: make(chan net.Conn, 2048),
		tlsConfig:   tlsConfig,
	}

	// start QUIC listener
	go l.listenQUIC(pc, &common.QUICCfg)

	return l, nil
}

// NewWebSocketListener starts a WebSocket listener on path "/ws" from the baseListener, and then starts a QUIC listener on top of
// all accepted websocket connections, returning the QUIC listener
func NewWebSocketListener(ctx context.Context, baseListener net.Listener, certPEM, keyPEM string) (net.Listener, error) {
	if err := otelInit(ctx); err != nil {
		return nil, err
	}
	closeFuncMetric := telemetry.EnableOTELMetrics(ctx)
	tlsConfig, err := tlsConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to load tlsconfig %w", err)
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebSocket connections
	l := &proxyListener{
		listeners:    []net.Listener{baseListener},
		connections:  make(chan net.Conn, 2048),
		mpconn:       newMultiplexedPacketConn(common.DebugAddr("1.1.1.1:1111")), //TODO: the local address
		tlsConfig:    tlsConfig,
		addr:         baseListener.Addr(),
		closeMetrics: closeFuncMetric,
	}

	// start WebSocket server
	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	http.Handle("/ws", otelhttp.NewHandler(http.HandlerFunc(l.handleWebSocket), "/ws"))
	go func() {
		common.Debugf("Egress server listening for WebSocket connections on %v", baseListener.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srv.Serve(baseListener)
		}()

		select {
		case <-ctx.Done():
			common.Debugf("Egress server context cancelled. Shutting down WebSocket server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
				common.Debugf("Error shutting down WebSocket server: %v", err)
			}
		case err := <-errChan:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				common.Debugf("Egress server failed to serve WebSocket: %v", err)
			}
		}
	}()

	// start QUIC listener
	go l.listenQUIC(l.mpconn, &common.QUICCfg)

	return l, nil
}

// NewWebTransportListener starts a WebTransport listener on the given address from baseListener at path "/wt", and then starts a QUIC listener on top of
// all accepted WebTransport datagram connections, returning the QUIC listener
func NewWebTransportListener(ctx context.Context, baseListener net.Listener, certPEM, keyPEM string) (net.Listener, error) {
	if err := otelInit(ctx); err != nil {
		return nil, err
	}
	closeFuncMetric := telemetry.EnableOTELMetrics(ctx)
	tlsConfig, err := tlsConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to load tlsconfig %w", err)
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebSocket connections
	l := &proxyListener{
		connections:  make(chan net.Conn, 2048),
		mpconn:       newMultiplexedPacketConn(common.DebugAddr("1.1.1.1:1111")), //TODO: the local address
		tlsConfig:    tlsConfig,
		addr:         baseListener.Addr(),
		closeMetrics: closeFuncMetric,
	}

	options := &webt.ListenOptions{
		Addr:      baseListener.Addr().String(),
		TLSConfig: tlsConfig,
		QUICConfig: &quicwrapper.Config{
			MaxIncomingStreams: 2000,
			EnableDatagrams:    true,
		},
		Path:            "/wt",
		DatagramHandler: l.handleWebTransport,
	}

	// WebTransport listener
	wtl, err := webt.ListenAddr(options)
	if err != nil {
		return nil, err
	}
	l.listeners = []net.Listener{wtl, baseListener}

	// start WebTransport server
	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		common.Debugf("Egress server listening for WebTransport connections on %v", wtl.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srv.Serve(wtl)
		}()

		select {
		case <-ctx.Done():
			common.Debugf("Egress server context cancelled. Shutting down WebTransport server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
				common.Debugf("Error shutting down WebTransport server: %v", err)
			}
		case err := <-errChan:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				common.Debugf("Egress server failed to serve WebTransport: %v", err)
			}
		}
	}()

	// start QUIC listener
	go l.listenQUIC(l.mpconn, &common.QUICCfg)

	return l, nil
}

// NewWebSocketWebTransportListener starts both a WebSocket listener on wsAddr at path "/ws", and a WebTransport listener on wtAddr at path "/wt",
// and then starts a QUIC listener on top of all accepts WebSocket connections and WebTransport datagram connections, returning the QUIC listener
// Convinience function to serve both WebSocket and WebTransport on the same baseListener (e.g. a TCP listener).
func NewWebSocketWebTransportListener(ctx context.Context, baseListener net.Listener, certPEM, keyPEM string) (net.Listener, error) {
	if err := otelInit(ctx); err != nil {
		return nil, err
	}
	closeFuncMetric := telemetry.EnableOTELMetrics(ctx)
	tlsConfig, err := tlsConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to load tlsconfig %w", err)
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebSocket connections
	l := &proxyListener{
		listeners:    []net.Listener{baseListener},
		connections:  make(chan net.Conn, 2048),
		mpconn:       newMultiplexedPacketConn(common.DebugAddr("1.1.1.1:1111")), //TODO: the local address
		tlsConfig:    tlsConfig,
		addr:         baseListener.Addr(), // we ignore webtransport address here and just use the websocket address
		closeMetrics: closeFuncMetric,
	}

	// Serve webtransport on baseListner+1
	addr, ok := baseListener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("baseListener is not a TCP listener: %v", baseListener.Addr())
	}
	addr.Port++

	options := &webt.ListenOptions{
		Addr:      addr.String(),
		TLSConfig: tlsConfig,
		QUICConfig: &quicwrapper.Config{
			MaxIncomingStreams: 2000,
			EnableDatagrams:    true,
		},
		Path:            "/wt",
		DatagramHandler: l.handleWebTransport,
	}

	// WebTransport listener
	wtl, err := webt.ListenAddr(options)
	if err != nil {
		return nil, err
	}
	l.listeners = append(l.listeners, wtl)

	// start WebSocket server
	srvWS := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	http.Handle("/ws", otelhttp.NewHandler(http.HandlerFunc(l.handleWebSocket), "/ws"))
	go func() {
		common.Debugf("Egress server listening for WebSocket connections on %v", l.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srvWS.Serve(baseListener)
		}()

		select {
		case <-ctx.Done():
			common.Debugf("Egress server context cancelled. Shutting down WebSocket server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srvWS.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
				common.Debugf("Error shutting down WebSocket server: %v", err)
			}
		case err := <-errChan:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				common.Debugf("Egress server failed to serve WebSocket: %v", err)
			}
		}
	}()

	// start WebTransport server
	srvWT := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		common.Debugf("Egress server listening for WebTransport connections on %v", wtl.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srvWT.Serve(wtl)
		}()

		select {
		case <-ctx.Done():
			common.Debugf("Egress server context cancelled. Shutting down WebTransport server...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srvWT.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
				common.Debugf("Error shutting down WebTransport server: %v", err)
			}
		case err := <-errChan:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				common.Debugf("Egress server failed to serve WebTransport: %v", err)
			}
		}
	}()

	// start QUIC listener
	go l.listenQUIC(l.mpconn, &common.QUICCfg)

	return l, nil
}

func tlsConfig(cert, key string) (*tls.Config, error) {
	if config, err := tlsConfigFromPEM(cert, key); err == nil {
		return config, nil
	} else {
		common.Debugf("Unable to load tlsconfig from PEM...trying file paths: %v", err)
		return tlsConfigFromFiles(cert, key)
	}
}

func tlsConfigFromFiles(certFile, keyFile string) (*tls.Config, error) {
	certPem, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("unable to load certfile %v: %w", certFile, err)
	}
	keyPem, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("unable to load keyfile %v: %w", keyFile, err)
	}
	return tlsConfigFromPEM(string(certPem), string(keyPem))
}

func tlsConfigFromPEM(certPEM, keyPEM string) (*tls.Config, error) {
	if certPEM != "" && keyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("unable to load cert/key from PEM for broflake: %v", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"broflake"},
		}, nil
	} else {
		common.Debugf("!!! WARNING !!! No certfile and/or keyfile specified, generating an insecure TLSConfig!")
		return generateTLSConfig(), nil
	}
}

func generateTLSConfig() *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"broflake"},
	}
}
