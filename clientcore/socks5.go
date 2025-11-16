package clientcore

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/getlantern/broflake/common"
)

type SOCKS5Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

func CreateSOCKS5Dialer(c ReliableStreamLayer) SOCKS5Dialer {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portString, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		port, err := strconv.Atoi(portString)
		if err != nil {
			return nil, err
		}

		connectReq := []byte{
			0x05, // VER
			0x01, // CMD
			0x00, // RESERVED
		}

		// Determine and set ATYP (domain, IPv4 addr, or IPv6 addr)
		ip := net.ParseIP(host)
		if ip == nil {
			// Domain
			connectReq = append(connectReq, 0x03)
			connectReq = append(connectReq, []byte(host)...)
			connectReq = append(connectReq, byte(len(host)))
		} else if ip.To4() != nil {
			// IPv4
			connectReq = append(connectReq, 0x01)
			ip4 := ip.To4()
			connectReq = append(connectReq, ip4...)
		} else if ip.To16() != nil {
			// IPv6
			connectReq = append(connectReq, 0x04)
			ip6 := ip.To16()
			connectReq = append(connectReq, ip6...)
		} else {
			common.Debug("Congratulations, you found the unimplemented handler for malformed SOCKS5 hosts!")
		}

		// Port as big endian
		connectReq = append(connectReq, byte(port>>8), byte(port&0xFF))

		conn, err := c.DialContext(ctx)
		if err != nil {
			return nil, err
		}

		// Send greeting
		greeting := []byte{
			0x05, // VER
			0x01, // NMETHODS
			0x00, // METHODS
		}

		_, err = conn.Write(greeting)
		if err != nil {
			return nil, err
		}

		// Handle greeting response
		res := make([]byte, 2)
		_, err = conn.Read(res)
		if err != nil {
			return nil, err
		}

		if res[0] != 0x05 {
			return nil, fmt.Errorf("bad SOCKS version: %v", res[0])
		}

		if res[1] != 0x00 {
			return nil, fmt.Errorf("server requires unsupported auth method: %v", res[1])
		}

		// Send CONNECT req
		_, err = conn.Write(connectReq)
		if err != nil {
			return nil, err
		}

		// Handle CONNECT response
		header := make([]byte, 4)
		_, err = conn.Read(header)
		if err != nil {
			return nil, err
		}

		if header[0] != 0x05 {
			return nil, fmt.Errorf("bad SOCKS version: %v", header[0])
		}

		if header[1] != 0x00 {
			return nil, fmt.Errorf("received non-success response to CONNECT request: %v", header[1])
		}

		// Parse ATYP and consume+discard the correct number of bytes from the connection
		var readLen int
		switch header[3] {
		case 0x03:
			// Domain, the length is stashed in the 1st byte
			readLen = int(header[4])
		case 0x01:
			// IPv4
			readLen = 4
		case 0x04:
			// IPv6
			readLen = 16
		default:
			common.Debug("Congratulations, you found the unimplemented handler for malformed SOCKS CONNECT responses!")
		}

		// +2 for the port
		responseAddr := make([]byte, readLen+2)
		_, err = conn.Read(responseAddr)
		if err != nil {
			return nil, err
		}

		return conn, nil
	}
}
