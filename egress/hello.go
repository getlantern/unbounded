package egress

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

// consumerHelloWait bounds how long the egress waits for a consumer's hello
// after dialing it. A consumer that predates the hello never sends one, and
// waiting the life of the connection would hold a goroutine per connection for
// nothing.
const consumerHelloWait = 30 * time.Second

// consumerHelloSlot holds the hello for one QUIC connection. A session takes
// the slot when it starts and reads it when it ends, so a hello that arrives
// mid-session, or after a migration, still reaches that session's log line.
type consumerHelloSlot struct {
	hello atomic.Pointer[common.ConsumerHello]
}

// load returns the hello, or the zero value if none has arrived.
func (s *consumerHelloSlot) load() common.ConsumerHello {
	if s == nil {
		return common.ConsumerHello{}
	}
	if h := s.hello.Load(); h != nil {
		return *h
	}
	return common.ConsumerHello{}
}

// trackConsumerHello registers a slot for a newly dialed connection, starts
// waiting for its hello, and drops the slot when the connection closes.
func (manager *connectionManager) trackConsumerHello(conn *quic.Conn) {
	slot := &consumerHelloSlot{}
	manager.hellos.Store(conn, slot)
	go func() {
		<-conn.Context().Done()
		manager.hellos.Delete(conn)
	}()
	go acceptConsumerHello(conn, slot)
}

// consumerHelloFor returns the slot for conn, or nil if conn is not tracked.
func (manager *connectionManager) consumerHelloFor(conn *quic.Conn) *consumerHelloSlot {
	if v, ok := manager.hellos.Load(conn); ok {
		return v.(*consumerHelloSlot)
	}
	return nil
}

// acceptConsumerHello reads the first unidirectional stream on conn as the
// consumer's hello. Bidirectional streams are the proxied traffic and are
// accepted elsewhere, so this never competes with them.
func acceptConsumerHello(conn *quic.Conn, slot *consumerHelloSlot) {
	ctx, cancel := context.WithTimeout(conn.Context(), consumerHelloWait)
	defer cancel()
	s, err := conn.AcceptUniStream(ctx)
	if err != nil {
		return
	}
	_ = s.SetReadDeadline(time.Now().Add(consumerHelloWait))
	h, err := common.ReadConsumerHello(s)
	s.CancelRead(0)
	if err != nil {
		slog.Debug("Couldn't read consumer hello", "error", err)
		return
	}
	slot.hello.Store(&h)
}
