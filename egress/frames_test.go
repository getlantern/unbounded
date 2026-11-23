package egress

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// framesConn is a WebSocket packet conn as handleWebsocket builds one, with its consumer's end.
func framesConn(t *testing.T) (errorlessWebSocketPacketConn, *websocket.Conn) {
	t.Helper()
	serverWS, clientWS := newWSPair(t)
	return errorlessWebSocketPacketConn{
		w:         serverWS,
		addr:      common.DebugAddr("WebSocket connection frames-test"),
		keepalive: time.Minute,
		tcpAddr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		readError: make(chan error, 1),
		frameV1:   &atomic.Bool{},
	}, clientWS
}

func consumerWrite(t *testing.T, ws *websocket.Conn, b []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, ws.Write(ctx, websocket.MessageBinary, b))
}

func consumerRead(t *testing.T, ws *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := ws.Read(ctx)
	require.NoError(t, err)
	return b
}

// Packets go as JSON until the consumer says it reads binary frames, and as FrameV1 after. The
// control frame that says so is never handed to QUIC.
func TestControlFrameSwitchesTheConsumerToBinaryFrames(t *testing.T) {
	q, consumer := framesConn(t)

	_, err := q.WriteTo([]byte("before"), nil)
	require.NoError(t, err)
	assert.Equal(t, byte('{'), consumerRead(t, consumer)[0], "JSON until the consumer says otherwise")

	consumerWrite(t, consumer, common.AppendControlFrame(nil, common.Control{Capabilities: common.CapFrameV1}))
	consumerWrite(t, consumer, []byte("a quic packet"))
	p := make([]byte, 1500)
	n, _, err := q.ReadFrom(p)
	require.NoError(t, err)
	assert.Equal(t, "a quic packet", string(p[:n]), "the control frame isn't a packet")

	_, err = q.WriteTo([]byte("after"), nil)
	require.NoError(t, err)
	b := consumerRead(t, consumer)
	assert.Equal(t, common.FrameV1, b[0])
	addr, payload, err := common.ParsePacketFrame(b)
	require.NoError(t, err)
	assert.Equal(t, q.addr.String(), addr)
	assert.Equal(t, []byte("after"), payload)
}

// A control frame that doesn't offer binary frames leaves the consumer on JSON.
func TestControlFrameWithoutTheCapabilityKeepsJSON(t *testing.T) {
	q, consumer := framesConn(t)
	consumerWrite(t, consumer, common.AppendControlFrame(nil, common.Control{}))
	consumerWrite(t, consumer, []byte("a quic packet"))
	p := make([]byte, 1500)
	_, _, err := q.ReadFrom(p)
	require.NoError(t, err)

	_, err = q.WriteTo([]byte("still json"), nil)
	require.NoError(t, err)
	assert.Equal(t, byte('{'), consumerRead(t, consumer)[0])
}

// An egress that predates control frames hands them to QUIC as packets. QUIC must drop them and
// carry on, or a new consumer would break an old egress's connections.
func TestQUICDropsAControlFrame(t *testing.T) {
	consumer, _ := startTestConsumer(t)
	t.Cleanup(func() { _ = consumer.Close() })
	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
	t.Cleanup(func() { closeAllRecords(cm) })
	pconn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pconn.Close() })

	conn, _, err := cm.createOrMigrate("test-csid-control", dialedPconn{PacketConn: pconn, dst: consumer.LocalAddr()})
	require.NoError(t, err)
	stream, err := openWritableStream(t, conn)
	require.NoError(t, err)
	_ = stream.Close()

	// The control frame arrives where the egress's QUIC reads, as an old egress would deliver it.
	control := common.AppendControlFrame(nil, common.Control{Capabilities: common.CapFrameV1})
	for range 5 {
		_, err := consumer.WriteTo(control, pconn.LocalAddr())
		require.NoError(t, err)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, conn.Context().Err(), "the connection survived")
	stream, err = openWritableStream(t, conn)
	require.NoError(t, err, "and still carries streams")
	_ = stream.Close()
}
