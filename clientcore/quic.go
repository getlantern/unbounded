package clientcore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

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

type QUICLayerOptions struct {
	ServerName         string
	InsecureSkipVerify bool
	CA                 *x509.CertPool
}

func NewQUICLayer(bfconn *BroflakeConn, qopt *QUICLayerOptions) (*QUICLayer, error) {
	q := &QUICLayer{
		bfconn: bfconn,
		t:      &quic.Transport{Conn: bfconn},
		tlsConfig: &tls.Config{
			ServerName:         qopt.ServerName,
			InsecureSkipVerify: qopt.InsecureSkipVerify,
			NextProtos:         []string{"broflake"},
			RootCAs:            qopt.CA,
		},
		eventualConn: newEventualConn(),
		dialTimeout:  8 * time.Second,
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
	dialTimeout  time.Duration
}

// DialAndMaintainQUICConnection attempts to create and maintain an e2e QUIC connection by dialing
// the other end, detecting if that connection breaks, and redialing. Forever.
func (c *QUICLayer) DialAndMaintainQUICConnection() {
	c.ctx, c.cancel = context.WithCancel(context.Background())

	// State 1 of 2: Keep dialing until we acquire a connection
	for {
		select {
		case <-c.ctx.Done():
			common.Debugf("Cancelling QUIC dialer!")
			return
		default:
			// Do nothing
		}

		ctxDial, cancelDial := context.WithTimeout(context.Background(), c.dialTimeout)
		connEstablished := make(chan quic.Connection)
		connErr := make(chan error)

		go func() {
			defer cancelDial()
			conn, err := c.t.Dial(ctxDial, common.DebugAddr("NELSON WUZ HERE"), c.tlsConfig, &common.QUICCfg)

			if err != nil {
				connErr <- err
				return
			}

			teamId := "teamid-not-set"
			teamDatagram := []byte(common.TeamIdPrefix + teamId)
			conn.SendDatagram(teamDatagram)

			connEstablished <- conn
		}()

		select {
		case err := <-connErr:
			common.Debugf("QUIC dial failed (%v), retrying...", err)
		case conn := <-connEstablished:
			c.mx.Lock()
			c.eventualConn.set(conn)
			c.mx.Unlock()
			common.Debug("QUIC connection established, ready to proxy!")

			// State 2 of 2: Connection established, block until we detect a half open or a ctx cancel
			_, err := conn.AcceptStream(c.ctx)
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
