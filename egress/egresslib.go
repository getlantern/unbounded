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
				common.Debugf("Ingress bytes: %v", b)
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
	net.PacketConn
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

type proxyListener struct {
	net.Listener // the websocket or webtransport listener
	connections  chan net.Conn
	tlsConfig    *tls.Config
	addr         net.Addr
	closeMetrics func(ctx context.Context) error

	mutex  sync.Mutex
	closed bool

	// The path for this proxy listener on the server, such as
	// /ws for websockets or /wt for webtransport.
	path string
	name string
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
	l.mutex.Lock()
	if l.closed {
		l.mutex.Unlock()
		return nil
	}
	l.closed = true
	close(l.connections)
	l.mutex.Unlock()

	err := l.Listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if l.closeMetrics != nil {
		l.closeMetrics(ctx)
	}
	return err
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

func extractTeamId(r *http.Request) string {
	v := r.Header.Values("Sec-Websocket-Protocol")
	for _, s := range v {
		if strings.HasPrefix(s, common.TeamIdPrefix) {
			return strings.TrimPrefix(s, common.TeamIdPrefix)
		}
	}
	return ""
}

func (l *proxyListener) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	teamId := extractTeamId(r)
	common.Debugf("Websocket connection from %v team: %v", r.Host, teamId)
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

	wspconn := websocketPacketConn{
		w:         c,
		addr:      common.DebugAddr(fmt.Sprintf("WebSocket connection %v", uuid.NewString())),
		keepalive: websocketKeepalive,
		tcpAddr:   tcpAddr,
	}

	defer wspconn.Close()

	common.Debugf("Accepted a new WebSocket connection! (%v total)", atomic.AddUint64(&nClients, 1))
	nClientsCounter.Add(context.Background(), 1)

	listener, err := quic.Listen(wspconn, l.tlsConfig, &common.QUICCfg)
	if err != nil {
		common.Debugf("Error creating QUIC listener: %v", err)
		return
	}

	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			switch websocket.CloseStatus(err) {
			case websocket.StatusNormalClosure, websocket.StatusGoingAway:
				common.Debugf("%v closed normally", wspconn.addr)
			default:
				common.Debugf("%v QUIC listener error (%v), closing!", wspconn.addr, err)
			}
			listener.Close()
			break

		}

		nQUICConnectionsCounter.Add(context.Background(), 1)
		common.Debugf("%v accepted a new QUIC connection!", wspconn.addr)

		go func() {
			for {
				stream, err := conn.AcceptStream(context.Background())

				if err != nil {
					// We interpret an error while accepting a stream to indicate an unrecoverable error with
					// the QUIC connection, and so we close the QUIC connection altogether
					errString := fmt.Sprintf("%v stream error (%v), closing QUIC connection!", wspconn.addr, err)
					common.Debugf("%v", errString)
					conn.CloseWithError(quic.ApplicationErrorCode(42069), errString)
					nQUICConnectionsCounter.Add(context.Background(), -1)
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
					TeamId:     teamId,
				}
			}
		}()
	}
}

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

func (l *proxyListener) handleWebTransport(pconn net.PacketConn, remoteAddr net.Addr) {
	addr := common.DebugAddr(fmt.Sprintf("WebTransport connection %v", uuid.NewString()))
	wtpconn := webtransportPacketConn{pconn}
	defer wtpconn.Close()

	common.Debugf("Accepted a new WebTransport connection! (%v total)", atomic.AddUint64(&nClients, 1))
	nClientsCounter.Add(context.Background(), 1)

	listener, err := quic.Listen(wtpconn, l.tlsConfig, &common.QUICCfg)
	if err != nil {
		common.Debugf("Error creating QUIC listener on webtransport datagram: %v", err)
		return
	}
	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			common.Debugf("%v QUIC listener error (%v), closing!", addr, err)
			listener.Close()
			break
		}

		nQUICConnectionsCounter.Add(context.Background(), 1)
		common.Debugf("%v accepted a new QUIC connection!", addr)

		go func() {
			for {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					// We interpret an error while accepting a stream to indicate an unrecoverable error with
					// the QUIC connection, and so we close the QUIC connection altogether
					errString := fmt.Sprintf("%v stream error (%v), closing QUIC connection!", addr, err)
					common.Debugf("%v", errString)
					conn.CloseWithError(quic.ApplicationErrorCode(42069), errString)
					nQUICConnectionsCounter.Add(context.Background(), -1)
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
					AddrRemote: remoteAddr,
				}
			}
		}()
	}
}

func NewWebTransportListener(ctx context.Context, addr, certPEM, keyPEM string) (net.Listener, error) {
	if err := otelInit(ctx); err != nil {
		return nil, err
	}
	closeFuncMetric := telemetry.EnableOTELMetrics(ctx)
	tlsConfig, err := tlsConfig(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to load tlsconfig %w", err)
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		common.Debugf("Error resolving TCPAddr: %v", err)
		return nil, fmt.Errorf("error resolving TCPAddr %w", err)
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebTransport connections
	l := &proxyListener{
		connections:  make(chan net.Conn, 2048),
		tlsConfig:    tlsConfig,
		addr:         tcpAddr,
		closeMetrics: closeFuncMetric,
		path:         "/wt",
		name:         "webtransport",
	}

	options := &webt.ListenOptions{
		Addr:      addr,
		TLSConfig: tlsConfig,
		QUICConfig: &quicwrapper.Config{
			MaxIncomingStreams: 2000,
			EnableDatagrams:    true,
		},
		Path:            "/wt",
		DatagramHandler: l.handleWebTransport,
	}
	wl, err := webt.ListenAddr(options)
	if err != nil {
		return nil, err
	}
	// set the webtransport listener to our wrapped listener so it can be closed properly
	l.Listener = wl

	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		common.Debugf("Egress server listening for WebTransport connections on %v", wl.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srv.Serve(wl)
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

	return l, nil
}

func NewWebSocketListener(ctx context.Context, ll net.Listener, certPEM, keyPEM string) (net.Listener, error) {
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
		Listener:     ll,
		connections:  make(chan net.Conn, 2048),
		tlsConfig:    tlsConfig,
		addr:         ll.Addr(),
		closeMetrics: closeFuncMetric,
		path:         "/ws",
		name:         "websocket",
	}

	srv := &http.Server{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	http.Handle(l.path, otelhttp.NewHandler(http.HandlerFunc(l.handleWebSocket), l.path))
	go func() {
		common.Debugf("Egress server listening for WebSocket connections on %v", ll.Addr())

		errChan := make(chan error, 1)
		go func() {
			errChan <- srv.Serve(ll)
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
