package egress

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

func newTestPacketConn(t *testing.T) (*errorlessWebSocketPacketConn, *websocket.Conn) {
	t.Helper()
	serverWS, clientWS := newWSPair(t)
	return &errorlessWebSocketPacketConn{
		w:         serverWS,
		addr:      common.DebugAddr("websocket-test"),
		keepalive: time.Minute,
		tcpAddr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		readError: make(chan error, 1),
	}, clientWS
}

// ReadFrom must honor the net.PacketConn contract: n is what was copied into p, never more.
func TestReadFrom_OversizedMessageReturnsBytesCopied(t *testing.T) {
	q, clientWS := newTestPacketConn(t)

	msg := make([]byte, 4000)
	if err := clientWS.Write(context.Background(), websocket.MessageBinary, msg); err != nil {
		t.Fatalf("writing message: %v", err)
	}

	p := make([]byte, 1452)
	n, _, err := q.ReadFrom(p)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != len(p) {
		t.Fatalf("ReadFrom returned n = %d for a %d-byte buffer, want %d", n, len(p), len(p))
	}
}

// quic-go slices a pooled buffer of one max-size packet to ReadFrom's n, so an oversized message
// from an unauthenticated client must not reach it as an n past that buffer's capacity.
func TestQUICSurvivesOversizedWebSocketMessage(t *testing.T) {
	q, clientWS := newTestPacketConn(t)

	tr := &quic.Transport{Conn: q}
	ln, err := tr.Listen(testServerTLS(), &common.QUICCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	for _, size := range []int{2000, 20000} {
		if err := clientWS.Write(context.Background(), websocket.MessageBinary, make([]byte, size)); err != nil {
			t.Fatalf("writing %d-byte message: %v", size, err)
		}
	}
	// A panic in quic-go's read loop kills the test binary; give it time to happen.
	time.Sleep(500 * time.Millisecond)
}
