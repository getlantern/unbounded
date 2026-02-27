package egress

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/getlantern/broflake/common"
)

const (
	uotMagicAddress       = "sp.v2.udp-over-tcp.arpa"
	uotLegacyMagicAddress = "sp.udp-over-tcp.arpa"

	// SOCKS5 address types (used in UoT v2 request header via SocksaddrSerializer)
	socksAddrTypeIPv4 = 0x01
	socksAddrTypeFQDN = 0x03
	socksAddrTypeIPv6 = 0x04

	maxUDPPayload = 65507
)

// UoTResolver is a SOCKS5 DNS resolver that passes UoT magic addresses through
// without resolution, falling back to standard DNS for everything else.
// go-socks5 resolves FQDNs before calling Dial, so we must intercept here.
type UoTResolver struct{}

func (r *UoTResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	if isUoTMagicName(name) {
		// Return nil IP so AddrSpec.Address() falls through to the FQDN,
		// allowing the Dial function to match on the magic address.
		return ctx, nil, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	if len(ips) == 0 {
		return ctx, nil, fmt.Errorf("no IP addresses found for %s", name)
	}
	return ctx, ips[0].IP, nil
}

func isUoTMagicName(name string) bool {
	name = strings.TrimSuffix(name, ".")
	return strings.EqualFold(name, uotMagicAddress) || strings.EqualFold(name, uotLegacyMagicAddress)
}

func isUoTAddress(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	return isUoTMagicName(host)
}

// tcpPipeConn wraps a net.Conn (from net.Pipe) so that LocalAddr() returns a
// *net.TCPAddr. go-socks5 does target.LocalAddr().(*net.TCPAddr) after Dial.
type tcpPipeConn struct {
	net.Conn
}

func (c *tcpPipeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (c *tcpPipeConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

type uotRequest struct {
	IsConnect   bool
	Destination *net.UDPAddr
}

// readUoTRequest reads a UoT v2 request header.
// Wire format: 1 byte IsConnect (bool) + SOCKS5-style address (ATYP + addr + port).
func readUoTRequest(r io.Reader) (*uotRequest, error) {
	var isConnect byte
	if err := binary.Read(r, binary.BigEndian, &isConnect); err != nil {
		return nil, fmt.Errorf("read IsConnect: %w", err)
	}

	addr, err := readSocksAddr(r)
	if err != nil {
		return nil, fmt.Errorf("read destination: %w", err)
	}

	return &uotRequest{
		IsConnect:   isConnect != 0,
		Destination: addr,
	}, nil
}

// readSocksAddr reads a SOCKS5-style address: ATYP + address + 2-byte port (big-endian).
func readSocksAddr(r io.Reader) (*net.UDPAddr, error) {
	var atyp byte
	if err := binary.Read(r, binary.BigEndian, &atyp); err != nil {
		return nil, err
	}

	var ip net.IP
	var host string

	switch atyp {
	case socksAddrTypeIPv4:
		ip = make(net.IP, 4)
		if _, err := io.ReadFull(r, ip); err != nil {
			return nil, err
		}
	case socksAddrTypeIPv6:
		ip = make(net.IP, 16)
		if _, err := io.ReadFull(r, ip); err != nil {
			return nil, err
		}
	case socksAddrTypeFQDN:
		var length byte
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		domain := make([]byte, length)
		if _, err := io.ReadFull(r, domain); err != nil {
			return nil, err
		}
		host = string(domain)
	default:
		return nil, fmt.Errorf("unknown address type: %d", atyp)
	}

	var port uint16
	if err := binary.Read(r, binary.BigEndian, &port); err != nil {
		return nil, err
	}

	if host != "" {
		resolved, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			return nil, err
		}
		return resolved, nil
	}

	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

// handleUoT handles a UoT v2 connection by relaying framed UDP packets between
// a TCP stream and a real UDP socket.
func handleUoT(tcpConn net.Conn) {
	defer tcpConn.Close()

	req, err := readUoTRequest(tcpConn)
	if err != nil {
		common.Debugf("UoT: failed to read request: %v", err)
		return
	}

	common.Debugf("UoT: relay to %v (isConnect=%v)", req.Destination, req.IsConnect)

	if !req.IsConnect {
		common.Debugf("UoT: non-connect mode not supported")
		return
	}

	udpConn, err := net.DialUDP("udp", nil, req.Destination)
	if err != nil {
		common.Debugf("UoT: failed to dial UDP %v: %v", req.Destination, err)
		return
	}
	defer udpConn.Close()

	common.Debugf("UoT: connected UDP to %v", req.Destination)

	done := make(chan struct{}, 2)

	// TCP → UDP: read length-prefixed packets from TCP, send as raw UDP
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65535)
		for {
			var length uint16
			if err := binary.Read(tcpConn, binary.BigEndian, &length); err != nil {
				return
			}
			if int(length) > maxUDPPayload {
				common.Debugf("UoT: dropping oversized frame (%d bytes, max %d)", length, maxUDPPayload)
				if _, err := io.CopyN(io.Discard, tcpConn, int64(length)); err != nil {
					return
				}
				continue
			}
			if _, err := io.ReadFull(tcpConn, buf[:length]); err != nil {
				return
			}
			if _, err := udpConn.Write(buf[:length]); err != nil {
				return
			}
		}
	}()

	// UDP → TCP: read raw UDP, write as length-prefixed packets to TCP
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65535)
		for {
			n, err := udpConn.Read(buf)
			if err != nil {
				return
			}
			if err := binary.Write(tcpConn, binary.BigEndian, uint16(n)); err != nil {
				return
			}
			if _, err := tcpConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	<-done
}

// UoTDialer returns a SOCKS5-compatible dial function that intercepts UoT magic
// addresses and handles them as UDP-over-TCP tunnels, while dialing everything
// else normally.
func UoTDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if isUoTAddress(addr) {
			common.Debugf("UoT: intercepting %v", addr)
			client, server := net.Pipe()
			go func() {
				<-ctx.Done()
				client.Close()
				server.Close()
			}()
			go handleUoT(server)
			return &tcpPipeConn{Conn: client}, nil
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}
