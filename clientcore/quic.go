package clientcore

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

type ReliableStreamLayer interface {
	DialContext(ctx context.Context) (net.Conn, error)
}

func CreateHTTPTransport(c ReliableStreamLayer) *http.Transport {
	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse("http://i.do.nothing")
		},
		Dial: func(network, addr string) (net.Conn, error) {
			return c.DialContext(context.Background())
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.DialContext(ctx)
		},
	}
}

func NewQUICLayer(bfconn *BroflakeConn) (*QUICLayer, error) {
	q := &QUICLayer{
		bfconn:       bfconn,
		t:            &quic.Transport{Conn: bfconn},
		tlsConfig:    generateTLSConfig(), // TODO nelson 07/24/2025: actually configure TLS
		eventualConn: newEventualConn(),
	}

	return q, nil
}

type QUICLayer struct {
	bfconn       *BroflakeConn
	t            *quic.Transport
	tlsConfig    *tls.Config
	eventualConn *eventualConn
	mx           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
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

func (c *QUICLayer) ListenAndMaintainQUICConnection() {
	c.ctx, c.cancel = context.WithCancel(context.Background())

	for {
		listener, err := quic.Listen(c.bfconn, c.tlsConfig, &common.QUICCfg)
		if err != nil {
			common.Debugf("Error creating QUIC listener: %v\n", err)
			continue
		}

		for {
			conn, err := listener.Accept(c.ctx)
			if err != nil {
				common.Debugf("QUIC listener error (%v), closing!\n", err)
				listener.Close()
				break
			}

			c.mx.Lock()
			c.eventualConn.set(conn)
			c.mx.Unlock()
			common.Debug("QUIC connection established, ready to proxy!")

			// Connection established, block until we detect a half open or a ctx cancel
			_, err = conn.AcceptStream(c.ctx)
			if err != nil {
				common.Debugf("QUIC connection error (%v), closing!", err)
				conn.CloseWithError(42069, "")
			}

			// If we've hit this path, either our QUIC connection has broken or the caller wants to
			// destroy this QUICLayer, so we iterate the loop to proceed. If there's a process that's
			// using this QUICLayer for communication, they'll block on their next call to DialContext
			// until a new QUIC connection is acquired (or their context deadline expires).
			c.mx.Lock()
			c.eventualConn = newEventualConn()
			c.mx.Unlock()
		}
	}
}

// Close a QUICLayer which was previously opened via a call to DialAndMaintainQUICConnection.
func (c *QUICLayer) Close() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *QUICLayer) DialContext(ctx context.Context) (net.Conn, error) {
	c.mx.RLock()
	waiter := c.eventualConn
	c.mx.RUnlock()

	qconn, err := waiter.get(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &common.QUICStreamNetConn{Stream: stream}, nil
}

// QUICLayer is a ReliableStreamLayer
var _ ReliableStreamLayer = &QUICLayer{}

func newEventualConn() *eventualConn {
	return &eventualConn{
		ready: make(chan struct{}, 0),
	}
}

type eventualConn struct {
	conn  quic.Connection
	ready chan struct{}
}

func (w *eventualConn) get(ctx context.Context) (quic.Connection, error) {
	select {
	case <-w.ready:
		return w.conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *eventualConn) set(conn quic.Connection) {
	w.conn = conn
	close(w.ready)
}
