package egress

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/getlantern/broflake/common"
)

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
	w            *websocket.Conn
	addr         net.Addr
	keepalive    time.Duration
	tcpAddr      *net.TCPAddr
	readError    chan error
	peerID       string
	ingressBytes *uint64 // per-peer atomic counter, shared across all connections from the same peer
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

		// XXX nelson 08/18/2025: This code path remains the source of much confusion. Let me unpack
		// some observations that led us to to the present behavior. First, we observed that if you
		// never return an error from ReadFrom, then quic-go will continue to call ReadFrom on old
		// transports forever -- even if you've switched to a new path *and* closed the old paths, and
		// even if the QUIC connection is *closed completely!* Yes, you read that correctly: when we
		// close a QUIC connection, we discover that some internal process of quic-go is still calling
		// ReadFrom on all of its historical transports, presumably because we never returned an error.
		// However, we never found a way to return an error that worked. We attempted a design wherein
		// ReadFrom would, upon intercepting the first error, block until someone called Close() on the
		// errorlessWebSocketPacketConn, at which point it would return an error of our own choosing. But
		// we discovered that an error returned by transport A is received by the AcceptStream handler
		// of transport B, even after we'd migrated from A to B -- and that an error returned by
		// transport A puts the entire connection in a permanently bad state -- you can no longer call
		// AcceptStream on the connection, as it will forever return a "transport closed" error. So we
		// find ourselves in a pickle: if we do return an error, we bork the connection, and if we don't
		// return an error, we seem to never release the resource. Examining the call stack before the
		// infinite calls to ReadFrom, it looks like runtime.Goexit was implicated, and thus:
		runtime.Goexit()
	}

	copy(p, b)
	atomic.AddUint64(q.ingressBytes, uint64(len(b)))
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
