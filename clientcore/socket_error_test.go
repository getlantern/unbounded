//go:build !wasm && !windows

package clientcore

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/getlantern/broflake/common"
)

// A socket that can't open or send reports why, as a step and an error class, so Freddie can tell
// a tunnel's missing routes from a firewall or a vanished address.
func TestSTUNPreflightNoSocketCause(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3478}
	refused := func(err error) listenFunc {
		return func(string) (udpConn, error) { return nil, err }
	}
	for _, tc := range []struct {
		name   string
		listen listenFunc
		want   string
	}{
		{"listen unreachable", refused(&net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.ENETUNREACH)}), "listen_unreachable"},
		{"listen address gone", refused(os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)), "listen_addr_unavailable"},
		{"listen refused", refused(syscall.EACCES), "listen_permission"},
		{"listen other", refused(errors.New("boom")), "listen_other"},
		{"send unreachable", func(string) (udpConn, error) { return failingSend{syscall.EHOSTUNREACH}, nil }, "send_unreachable"},
		{"send no buffers", func(string) (udpConn, error) { return failingSend{syscall.ENOBUFS}, nil }, "send_no_buffers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probeAddr(context.Background(), tc.listen, addr)
			if got.outcome != STUNPreflightNoSocket || got.cause != tc.want {
				t.Fatalf("got %+v, want %s from %s", got, STUNPreflightNoSocket, tc.want)
			}
			if !common.KnownPreflightCause(got.cause) {
				t.Fatalf("cause %q isn't one Freddie counts", got.cause)
			}
		})
	}
}

// failingSend is a socket whose every write fails with err.
type failingSend struct{ err error }

func (f failingSend) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, net.ErrClosed }
func (f failingSend) WriteTo([]byte, net.Addr) (int, error) {
	return 0, &net.OpError{Op: "write", Err: os.NewSyscallError("sendto", f.err)}
}
func (f failingSend) Close() error { return nil }
