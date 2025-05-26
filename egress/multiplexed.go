package egress

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
)

// multiplexedPacketConn multiplexes packets from multiple tunnels (WebTransport and WebSocket as net.PacketConn)
type multiplexedPacketConn struct {
	mu           sync.RWMutex
	tunnels      map[string]net.PacketConn
	closeCh      map[string]chan struct{} // signal tunnel closure
	addrToTunnel map[string]string        // maps client addresses (WebTransport or WebSocket remote address) -> tunnel ID

	packetCh  chan packetInfo // channel for incoming packets from all tunnels
	localAddr net.Addr        // local address
}

// packetInfo contains packet data, remote address, and tunnel ID
type packetInfo struct {
	data     []byte
	addr     net.Addr
	tunnelID string
}

func newMultiplexedPacketConn(localAddr net.Addr) *multiplexedPacketConn {
	return &multiplexedPacketConn{
		tunnels:      make(map[string]net.PacketConn),
		closeCh:      make(map[string]chan struct{}),
		addrToTunnel: make(map[string]string),
		packetCh:     make(chan packetInfo, 1024), // buffered to avoid blocking the incoming traffic
		localAddr:    localAddr,
	}
}

func (m *multiplexedPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	// block until a packet arrives from any tunnel
	packet, ok := <-m.packetCh
	if !ok {
		return 0, nil, io.EOF
	}
	return copy(p, packet.data), packet.addr, nil
}

func (m *multiplexedPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.RLock()
	tunnelID, ok := m.addrToTunnel[addr.String()]
	tunnel := m.tunnels[tunnelID]
	m.mu.RUnlock()

	if ok && tunnel != nil {
		return tunnel.WriteTo(p, addr)
	}
	// route to all tunnels if we don't know which one to use, or during connection establishment
	return m.writeToAllTunnels(p, addr)
}

func (m *multiplexedPacketConn) writeToAllTunnels(p []byte, addr net.Addr) (n int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var combinedErr error
	// maybe this can be parallelized
	for _, tunnel := range m.tunnels {
		n, err = tunnel.WriteTo(p, addr)
		if err != nil {
			combinedErr = errors.Join(combinedErr, err)
		}
	}
	return n, combinedErr
}

// AddTunnel adds a new tunnel to the multiplexer
func (m *multiplexedPacketConn) AddTunnel(id string, conn net.PacketConn) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tunnels[id]; exists {
		return m.closeCh[id]
	}

	c := make(chan struct{})
	m.tunnels[id] = conn
	m.closeCh[id] = c
	// start reading from this tunnel
	go m.readFromTunnel(id, conn, c)
	return c
}

// RemoveTunnel closes the tunnel by signaling the close channel, and remove it from the map
func (m *multiplexedPacketConn) RemoveTunnel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.closeCh[id]; ok {
		close(c)
		delete(m.closeCh, id)
	}
	delete(m.tunnels, id)
}

func (m *multiplexedPacketConn) readFromTunnel(id string, conn net.PacketConn, stopCh chan struct{}) {
	buf := make([]byte, 65535)
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-stopCh:
					return
				default:
					continue
				}
			}
			common.Debugf("Error reading from tunnel %v: %v", id, err)
			m.RemoveTunnel(id)
			return
		}

		copyBuf := make([]byte, n)
		copy(copyBuf, buf[:n])

		// remember which tunnel this client is using
		m.mu.Lock()
		m.addrToTunnel[addr.String()] = id
		m.mu.Unlock()

		// send packet to multiplexer
		select {
		case m.packetCh <- packetInfo{data: copyBuf, addr: addr, tunnelID: id}:
		default:
			common.Debugf("Dropping packet from %s: channel full", id)
		}
	}
}

func (m *multiplexedPacketConn) Close() error {
	close(m.packetCh)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.Close()
	}
	return nil
}

func (m *multiplexedPacketConn) LocalAddr() net.Addr {
	return m.localAddr
}

func (m *multiplexedPacketConn) SetDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetDeadline(t)
	}
	return nil
}

func (m *multiplexedPacketConn) SetReadDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetReadDeadline(t)
	}
	return nil
}

func (m *multiplexedPacketConn) SetWriteDeadline(t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tunnel := range m.tunnels {
		tunnel.SetWriteDeadline(t)
	}
	return nil
}
