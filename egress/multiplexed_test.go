package egress

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/stretchr/testify/require"
)

// wraps a net.Conn to a net.PacketConn
type packetConnAdapter struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (a *packetConnAdapter) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := a.Conn.Read(b)
	return n, a.remote, err
}
func (a *packetConnAdapter) WriteTo(b []byte, addr net.Addr) (int, error) {
	return a.Conn.Write(b)
}
func (a *packetConnAdapter) LocalAddr() net.Addr                { return a.local }
func (a *packetConnAdapter) SetDeadline(t time.Time) error      { return a.Conn.SetDeadline(t) }
func (a *packetConnAdapter) SetReadDeadline(t time.Time) error  { return a.Conn.SetReadDeadline(t) }
func (a *packetConnAdapter) SetWriteDeadline(t time.Time) error { return a.Conn.SetWriteDeadline(t) }

func TestMultiplexedPacketConn(t *testing.T) {
	localAddr := common.DebugAddr("test_server")
	mpc := newMultiplexedPacketConn(localAddr)

	const tunnelCount = 1000
	writeConns := make([]net.Conn, tunnelCount)

	for i := range tunnelCount {
		clientAddr := common.DebugAddr(fmt.Sprintf("client-%d", i))
		serverConn, clientConn := net.Pipe()

		packetConn := &packetConnAdapter{
			Conn:   serverConn,
			local:  localAddr,
			remote: clientAddr,
		}
		mpc.AddTunnel(fmt.Sprintf("tunnel-%d", i), packetConn)

		// simulate incoming data from client
		msg := fmt.Sprintf("ping-from-%d", i)
		go func(conn net.Conn, data string) {
			conn.Write([]byte(data))
		}(clientConn, msg)

		writeConns[i] = clientConn
	}

	// Read all packets
	for range tunnelCount {
		buf := make([]byte, 1024)
		n, addr, err := mpc.ReadFrom(buf)
		require.NoError(t, err)
		expected := fmt.Sprintf("ping-from-%s", addr.String()[7:]) // "client-N"
		require.Equal(t, expected, string(buf[:n]))
	}

	// Write response back to each client
	for i := range tunnelCount {
		clientAddr := common.DebugAddr(fmt.Sprintf("client-%d", i))
		msg := fmt.Sprintf("pong-to-%d", i)
		go func(conn net.PacketConn, data string) {
			_, err := conn.WriteTo([]byte(data), clientAddr)
			require.NoError(t, err)
		}(mpc, msg)

		buf := make([]byte, 1024)
		n, err := writeConns[i].Read(buf)
		require.NoError(t, err)
		require.Equal(t, msg, string(buf[:n]))
	}
}
