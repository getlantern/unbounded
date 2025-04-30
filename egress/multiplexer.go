package egress

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
)

type MultiplexedPacketConn struct {
	mu           sync.RWMutex
	tunnels      map[string]net.PacketConn
	closeCh      map[string]chan struct{} // the channel used to signal closure of a tunnel
	addrToTunnel map[string]string        // client addresses (WebTransport or WebSocket remote address) to tunnel IDs

	// channel for incoming packets from all tunnels
	packetCh chan packetInfo

	localAddr net.Addr
}

type packetInfo struct {
	data     []byte
	addr     net.Addr
	tunnelID string
}

func NewMultiplexedPacketConn(localAddr net.Addr) *MultiplexedPacketConn {
	return &MultiplexedPacketConn{
		tunnels:      make(map[string]net.PacketConn),
		closeCh:      make(map[string]chan struct{}),
		addrToTunnel: make(map[string]string),
		packetCh:     make(chan packetInfo),
		localAddr:    localAddr,
	}
}

func (m *MultiplexedPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	// block until a packet arrives from any tunnel
	packet, ok := <-m.packetCh
	if !ok {
		return 0, nil, io.EOF
	}

	n = copy(p, packet.data)
	return n, packet.addr, nil
}

func (m *MultiplexedPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.RLock()
	tunnelID, ok := m.addrToTunnel[addr.String()]
	m.mu.RUnlock()

	if !ok {
		// Route to all tunnels if we don't know which one to use
		// (e.g., during connection establishment)
		return m.writeToAllTunnels(p, addr)
	}

	m.mu.RLock()
	tunnel, ok := m.tunnels[tunnelID]
	m.mu.RUnlock()

	if !ok {
		// Tunnel disappeared, try all tunnels
		return m.writeToAllTunnels(p, addr)
	}

	return tunnel.WriteTo(p, addr)
}

func (m *MultiplexedPacketConn) writeToAllTunnels(p []byte, addr net.Addr) (n int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var allErr error
	for _, tunnel := range m.tunnels {
		n, err = tunnel.WriteTo(p, addr)
		if err != nil {
			// append err to allErr
			allErr = errors.Join(allErr, err)
		}
	}

	return n, allErr
}

func (m *MultiplexedPacketConn) AddTunnel(id string, conn net.PacketConn) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := make(chan struct{})
	m.tunnels[id] = conn
	m.closeCh[id] = c

	// Start a goroutine to read from this tunnel
	go m.readFromTunnel(id, conn)
	return c
}

// RemoveTunnel closes the tunnel by signaling the close channel, and remove it from the map
func (m *MultiplexedPacketConn) RemoveTunnel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tunnels, id)
	if c, ok := m.closeCh[id]; ok {
		close(c)
		delete(m.closeCh, id)
	}
}

func (m *MultiplexedPacketConn) readFromTunnel(id string, conn net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			common.Debugf("Error reading from tunnel %v: %v", id, err)
			m.RemoveTunnel(id)
			return
		}

		packet := packetInfo{
			data:     make([]byte, n),
			addr:     addr,
			tunnelID: id,
		}
		copy(packet.data, buf[:n])

		// Remember which tunnel this client is using
		m.mu.Lock()
		m.addrToTunnel[addr.String()] = id
		m.mu.Unlock()

		// Send packet to multiplexer
		m.packetCh <- packet
	}
}

func (m *MultiplexedPacketConn) Close() error {
	close(m.packetCh)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.Close()
	}
	return nil
}

func (m *MultiplexedPacketConn) LocalAddr() net.Addr {
	return m.localAddr
}

func (m *MultiplexedPacketConn) SetDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetDeadline(t)
	}
	return nil
}

func (m *MultiplexedPacketConn) SetReadDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetReadDeadline(t)
	}
	return nil
}

func (m *MultiplexedPacketConn) SetWriteDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetWriteDeadline(t)
	}
	return nil
}
