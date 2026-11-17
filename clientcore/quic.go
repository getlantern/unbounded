package clientcore

import (
	"context"
	"crypto/tls"
	"errors"
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
	// ctx/cancel are set here, not in ListenAndMaintainQUICConnection, so they
	// exist before that goroutine starts and before any Close() can race it.
	// If they were initialized inside the goroutine, a Close() that landed
	// first would read a nil cancel and no-op, and the goroutine would then run
	// quic.Listen/Accept on a context that never gets cancelled — leaking the
	// listener, the maintain goroutine, and bfconn on every teardown that beats
	// the goroutine's first line.
	ctx, cancel := context.WithCancel(context.Background())
	q := &QUICLayer{
		bfconn:       bfconn,
		t:            &quic.Transport{Conn: bfconn},
		tlsConfig:    tlsConfig,
		eventualConn: newEventualConn(),
		ctx:          ctx,
		cancel:       cancel,
	}
	if bfconn != nil && bfconn.noPath != nil {
		q.noPath = bfconn.noPath
		q.pathDown = make(chan struct{})
		q.streams = make(map[*quic.Stream]struct{})
		go q.guardPaths()
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

	// noPath is the rooms-only consumer's no-path guard, nil for any other
	// layer, which then has neither pathDown nor streams.
	noPath *noPathGuard
	// pathDown is closed while the guard has failed the path (see
	// noPathGuard). Guarded by mx.
	pathDown chan struct{}
	// streams are those DialContext opened and not yet closed, for the guard
	// to reset. Guarded by smx, which is taken before mx where both are.
	smx     sync.Mutex
	streams map[*quic.Stream]struct{}
}

func (c *QUICLayer) ListenAndMaintainQUICConnection() {
	// ctx and cancel are set together by NewQUICLayer (the only supported
	// constructor). Guard against a QUICLayer built via struct literal:
	// refuse to start if either is missing, rather than nil-deref on
	// c.ctx.Err() below (nil ctx) or run an uncancellable loop that Close()
	// can never stop (nil cancel). We deliberately do NOT lazily init them
	// here — that would put the write back in this goroutine and reintroduce
	// the Close() race NewQUICLayer exists to prevent.
	if c.ctx == nil || c.cancel == nil {
		slog.Error("QUICLayer.ListenAndMaintainQUICConnection called on a layer not built via NewQUICLayer; refusing to start")
		return
	}

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
			slog.Debug("Error creating QUIC listener", "error", err)
			continue
		}

		err = c.acceptConns(listener)
		slog.Debug("QUIC listener error, closing",
			"err", err,
			"err_class", classifyQUICError(err),
		)
		listener.Close()
	}
}

// acceptConns accepts connections from listener until it fails, returning its error. Each
// connection it accepts becomes the one DialContext opens streams on, and the one it replaces,
// if any, is closed.
//
// It keeps accepting while a connection is live because the egress dials a new connection for
// this consumer only once it has dropped its record of the old one: at the end of its migration
// window, or when the old one failed on its side. So a new connection is proof the old one is
// dead, though ours may not learn so for up to MaxIdleTimeout, the egress's CONNECTION_CLOSE
// having gone out on a path that was already gone. Waiting for the old one to idle out before
// accepting again, as this loop once did, left the new connection queued in the listener and
// every dial meanwhile hanging on the dead one. A consumer has a single WebRTC slot and so a
// single path, so this holds even when the new connection comes from another egress instance:
// the old connection's packets now go to an egress that doesn't know it.
func (c *QUICLayer) acceptConns(listener *quic.Listener) error {
	for {
		conn, err := listener.Accept(c.ctx)
		if err != nil {
			return err
		}

		c.mx.Lock()
		old := c.eventualConn.current()
		if old != nil {
			// The current waiter is already ready with the old connection, so
			// a new one takes its place. Nobody waits on a ready waiter.
			c.eventualConn = newEventualConn()
		}
		c.eventualConn.set(conn)
		c.mx.Unlock()
		if old != nil {
			slog.Debug("New QUIC connection accepted, closing the one it replaces")
			old.CloseWithError(supersededQUICCode, "superseded")
		}
		slog.Debug("QUIC connection established, ready to proxy!")
		go c.serveConn(conn)
	}
}

// supersededQUICCode is the QUIC application error code acceptConns closes a replaced connection
// with.
const supersededQUICCode = 42071

// serveConn waits until conn ends, then, if it is still the current connection, has DialContext
// wait for the next.
func (c *QUICLayer) serveConn(conn *quic.Conn) {
	connStart := time.Now()

	// Connection established, block until we detect a half open or a ctx cancel
	_, err := conn.AcceptStream(c.ctx)
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
		//     "application_close_local"): acceptConns closing a
		//     connection a new one replaced (it logs that just
		//     before), and otherwise something that shouldn't happen
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

	// Either our QUIC connection has broken or the caller wants to destroy this QUICLayer. If
	// it is still the current connection, a process using this QUICLayer for communication will
	// block on its next call to DialContext until a new QUIC connection is accepted (or its
	// context deadline expires). If a new connection has already replaced it, that one stays.
	c.mx.Lock()
	if c.eventualConn.current() == conn {
		c.eventualConn = newEventualConn()
	}
	c.mx.Unlock()
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
	pathDown := c.pathDown
	c.mx.RUnlock()

	// A layer whose no-path guard has failed the path fails its dials at once,
	// rather than open a stream on a connection with no path under it, or wait
	// for a connection no path will bring.
	if isClosed(pathDown) {
		return nil, ErrNoConnectedPath
	}
	qconn, err := waiter.get(ctx, pathDown)
	if err != nil {
		return nil, err
	}
	// With the peer's stream credit spent, OpenStreamSync waits for the peer, which a dead path
	// never brings, so a failed path ends that wait too.
	openCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if pathDown != nil {
		go func() {
			select {
			case <-pathDown:
				cancel()
			case <-openCtx.Done():
			}
		}()
	}
	stream, err := qconn.OpenStreamSync(openCtx)
	if err != nil {
		if ctx.Err() == nil && isClosed(pathDown) {
			return nil, ErrNoConnectedPath
		}
		return nil, err
	}

	// XXX: we set AddrLocal and AddrRemote here only for compatibility with go-socks5:
	// https://github.com/armon/go-socks5/issues/50
	// These IP/port values are nonsense and have no relevance.
	conn := &common.QUICStreamNetConn{
		Stream:     stream,
		AddrLocal:  &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
		AddrRemote: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 31337},
	}
	if c.noPath != nil {
		if !c.trackStream(stream) {
			stream.CancelRead(noPathQUICCode)
			stream.CancelWrite(noPathQUICCode)
			return nil, ErrNoConnectedPath
		}
		conn.OnClose = func() { c.untrackStream(stream) }
	}
	return conn, nil
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

// get waits for the connection, failing with ErrNoConnectedPath if pathDown
// closes first. A nil pathDown never does.
func (w *eventualConn) get(ctx context.Context, pathDown chan struct{}) (*quic.Conn, error) {
	select {
	case <-w.ready:
		return w.conn, nil
	case <-pathDown:
		return nil, ErrNoConnectedPath
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// current returns the connection if it is set, or nil.
func (w *eventualConn) current() *quic.Conn {
	select {
	case <-w.ready:
		return w.conn
	default:
		return nil
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
//     connection. In the consumer's QUICLayer that is acceptConns
//     closing a connection the egress's next one replaced (see
//     acceptConns). Otherwise it should not happen
//     during steady-state operation — a non-zero count here would
//     indicate either another code path closing locally or a quic-go
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
