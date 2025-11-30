package clientcore

import (
	"context"
	"crypto/tls"
	"net"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

type ReliableStreamLayer interface {
	DialContext(ctx context.Context) (net.Conn, error)
}

func NewQUICLayer(
	bfconn *BroflakeConn,
	tlsConfig *tls.Config,
	cancelQUICStreamDeadlinesOnClose bool,
) (*QUICLayer, error) {
	q := &QUICLayer{
		bfconn:                           bfconn,
		t:                                &quic.Transport{Conn: bfconn},
		tlsConfig:                        tlsConfig,
		eventualConn:                     newEventualConn(),
		cancelQUICStreamDeadlinesOnClose: cancelQUICStreamDeadlinesOnClose,
	}

	return q, nil
}

type QUICLayer struct {
	bfconn                           *BroflakeConn
	t                                *quic.Transport
	tlsConfig                        *tls.Config
	eventualConn                     *eventualConn
	mx                               sync.RWMutex
	ctx                              context.Context
	cancel                           context.CancelFunc
	cancelQUICStreamDeadlinesOnClose bool
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

	// XXX: we set AddrLocal and AddrRemote here only for compatibility with go-socks5:
	// https://github.com/armon/go-socks5/issues/50
	// These IP/port values are nonsense and have no relevance.
	return &common.QUICStreamNetConn{
		Stream:                 stream,
		AddrLocal:              &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
		AddrRemote:             &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
		CancelDeadlinesOnClose: c.cancelQUICStreamDeadlinesOnClose,
	}, nil
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
