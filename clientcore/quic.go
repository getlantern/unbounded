package clientcore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

type ReliableStreamLayer interface {
	DialContext(ctx context.Context) (net.Conn, error)
}

func NewQUICLayer(bfconn *BroflakeConn, tlsConfig *tls.Config) (*QUICLayer, error) {
	q := &QUICLayer{
		bfconn:       bfconn,
		t:            &quic.Transport{Conn: bfconn},
		tlsConfig:    tlsConfig,
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

func (c *QUICLayer) ListenAndMaintainQUICConnection() {
	c.ctx, c.cancel = context.WithCancel(context.Background())

	for {
		// Bail out if Close() cancelled the context. Without this check
		// the outer loop would spin-create a new quic.Listen → Accept
		// pair every millisecond, each Accept returning context.Canceled
		// immediately, burning CPU and logging "QUIC listener error
		// (context canceled), closing!" thousands of times per second
		// whenever a single tunnel is torn down.
		if c.ctx.Err() != nil {
			return
		}

		listener, err := quic.Listen(c.bfconn, c.tlsConfig, &common.QUICCfg)
		if err != nil {
			slog.Debug(fmt.Sprintf("Error creating QUIC listener: %v\n", err))
			continue
		}

		for {
			conn, err := listener.Accept(c.ctx)
			if err != nil {
				slog.Debug("QUIC listener error, closing",
					"err", err,
					"err_class", classifyQUICError(err),
				)
				listener.Close()
				break
			}

			c.mx.Lock()
			c.eventualConn.set(conn)
			c.mx.Unlock()
			connStart := time.Now()
			slog.Debug("QUIC connection established, ready to proxy!")

			// Connection established, block until we detect a half open or a ctx cancel
			_, err = conn.AcceptStream(c.ctx)
			if err != nil {
				// Classify so we can tell the difference between
				//
				//   - the consumer's own QUIC stack idle-timing-out the
				//     connection during a producer re-pair gap (err_class
				//     = "idle_timeout"); this is the suspect cause of the
				//     "path probe error" failures the egress sees
				//   - the egress closing the connection from its side
				//     (err_class = "application_close_remote"); part of
				//     normal teardown when migrationWindow expires
				//   - QUICLayer.Close() being called locally
				//     (err_class = "context_canceled")
				//   - a local app-error close (err_class =
				//     "application_close_local"), which shouldn't happen
				//     during steady-state operation but if it does we want
				//     it visible separately, not lumped in with "remote"
				//   - other transport-level failures
				//
				// Connection lifetime is the other key signal — a
				// stillborn connection (lifetime < a few seconds) means
				// the handshake itself was unstable; a connection that
				// lives ~MaxIdleTimeout-ish before erroring is the
				// classic re-pair-gap idle timeout signature.
				slog.Debug("QUIC connection ended",
					"err", err,
					"err_class", classifyQUICError(err),
					"lifetime_s", time.Since(connStart).Seconds(),
				)
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
		Stream:     stream,
		AddrLocal:  &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
		AddrRemote: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
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
	conn  *quic.Conn
	ready chan struct{}
}

func (w *eventualConn) get(ctx context.Context) (*quic.Conn, error) {
	select {
	case <-w.ready:
		return w.conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *eventualConn) set(conn *quic.Conn) {
	w.conn = conn
	close(w.ready)
}

// classifyQUICError maps a quic-go / context error into a short tag
// suitable for structured-log filtering. Used by the consumer-side
// QUICLayer to distinguish failure modes that all show up in production
// as the same "QUIC connection error" line:
//
//   - idle_timeout: the connection sat idle past MaxIdleTimeout and
//     quic-go's local state was discarded. This is the suspect cause
//     of the "egress sees path probe timeout" prod failures — if the
//     consumer GC'd its connection during a WebRTC re-pair gap, the
//     egress's PATH_CHALLENGE has nothing to respond to it.
//   - handshake_timeout: the QUIC handshake itself didn't finish in
//     time. Distinct from idle_timeout because it never reached the
//     "ready to proxy" state.
//   - application_close_remote: the peer (egress, in our case) called
//     CloseWithError. Normal teardown when migrationWindow expires
//     and the egress flushes the connection record.
//   - application_close_local: WE called CloseWithError on the
//     connection. In the consumer's QUICLayer this should not happen
//     during steady-state operation — a non-zero count here would
//     indicate either a future code path closing locally or a quic-go
//     internal that surfaces as a local app-error. Worth flagging
//     separately so triage doesn't conflate it with normal teardown.
//   - transport_error: a quic-go-internal transport failure
//     (e.g. protocol violation, decryption failure).
//   - stateless_reset: the egress sent a stateless reset because it
//     no longer recognises the connection ID. Usually means egress-
//     side state was lost and re-creation is required.
//   - version_negotiation: client/server couldn't agree on a QUIC
//     version. Should not happen in our setup.
//   - context_canceled / deadline_exceeded: the QUICLayer's own
//     context was cancelled (typically by Close()).
//   - other: anything else, including raw I/O errors from bfconn.
func classifyQUICError(err error) string {
	if err == nil {
		return ""
	}
	var (
		idleErr      *quic.IdleTimeoutError
		handshakeErr *quic.HandshakeTimeoutError
		appErr       *quic.ApplicationError
		transportErr *quic.TransportError
		versionErr   *quic.VersionNegotiationError
		resetErr     *quic.StatelessResetError
	)
	switch {
	case errors.As(err, &idleErr):
		return "idle_timeout"
	case errors.As(err, &handshakeErr):
		return "handshake_timeout"
	case errors.As(err, &appErr):
		// quic.ApplicationError carries a Remote bool: true means the
		// peer initiated the close, false means we did. Lumping both
		// into "application_close" would hide the difference between
		// "egress finished its migrationWindow and flushed the record"
		// (remote, expected) and "the local QUICLayer closed its own
		// connection mid-flight" (local, suspicious).
		if appErr.Remote {
			return "application_close_remote"
		}
		return "application_close_local"
	case errors.As(err, &transportErr):
		return "transport_error"
	case errors.As(err, &versionErr):
		return "version_negotiation"
	case errors.As(err, &resetErr):
		return "stateless_reset"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	return "other"
}
