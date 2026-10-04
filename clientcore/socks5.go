package clientcore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"
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
			connectReq = append(connectReq, byte(len(host)))
			connectReq = append(connectReq, []byte(host)...)
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
			slog.Debug("Congratulations, you found the unimplemented handler for malformed SOCKS5 hosts!")
		}

		// Port as big endian
		connectReq = append(connectReq, byte(port>>8), byte(port&0xFF))

		conn, err := c.DialContext(ctx)
		if err != nil {
			return nil, err
		}
		if err := socks5Handshake(ctx, conn, connectReq); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// socks5Handshake runs the client side of a no-auth SOCKS5 CONNECT on conn.
//
// Its reads block until the egress answers, so ctx bounds them: ctx's deadline
// is applied to conn, and cancelling ctx expires it. Without that a stalled
// egress holds the dial (and its stream) for as long as the QUIC connection
// lives. On success the deadline is cleared so the caller gets an unbounded
// conn.
func socks5Handshake(ctx context.Context, conn net.Conn, connectReq []byte) (err error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			// ctx ended during the handshake and has expired conn's deadline;
			// report that rather than whichever read or write tripped on it.
			err = ctx.Err()
			return
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			return
		}
		err = conn.SetDeadline(time.Time{})
	}()

	// Send greeting
	greeting := []byte{
		0x05, // VER
		0x01, // NMETHODS
		0x00, // METHODS
	}
	if _, err := conn.Write(greeting); err != nil {
		return err
	}

	// Handle greeting response
	res := make([]byte, 2)
	if _, err := io.ReadFull(conn, res); err != nil {
		return err
	}
	if res[0] != 0x05 {
		return fmt.Errorf("bad SOCKS version: %v", res[0])
	}
	if res[1] != 0x00 {
		return fmt.Errorf("server requires unsupported auth method: %v", res[1])
	}

	// Send CONNECT req
	if _, err := conn.Write(connectReq); err != nil {
		return err
	}

	// Handle CONNECT response: VER, REP, RSV, ATYP, then BND.ADDR and BND.PORT
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return fmt.Errorf("bad SOCKS version: %v", header[0])
	}
	if header[1] != 0x00 {
		return fmt.Errorf("received non-success response to CONNECT request: %v", header[1])
	}

	// Consume and discard BND.ADDR and BND.PORT
	var addrLen int
	switch header[3] {
	case 0x01:
		// IPv4
		addrLen = 4
	case 0x04:
		// IPv6
		addrLen = 16
	case 0x03:
		// Domain, prefixed with its length
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return err
		}
		addrLen = int(l[0])
	default:
		return fmt.Errorf("unsupported address type in CONNECT response: %v", header[3])
	}
	_, err = io.ReadFull(conn, make([]byte, addrLen+2))
	return err
}
