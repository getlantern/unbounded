package egress

import (
	"context"
	"crypto/tls"
	"net"
	"runtime"
	"testing"
	"time"
)

// TestNewListener_CleanShutdownDoesNotPanic is a regression test for the
// unconditional panic that used to fire from the serve goroutine spawned
// inside NewListener. Closing the listener is a normal shutdown path; the
// goroutine should exit quietly, not take the process down with it.
func TestNewListener_CleanShutdownDoesNotPanic(t *testing.T) {
	ctx := context.Background()

	tcpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ll, err := NewListener(ctx, tcpL, &tls.Config{
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	// Give the internal serve goroutine a moment to enter Accept.
	time.Sleep(50 * time.Millisecond)

	// Clean shutdown path: closing the wrapped listener cascades to the
	// underlying TCP listener, which causes srv.Serve to return net.ErrClosed.
	// Pre-fix this panicked the process; post-fix it should log-and-return.
	if err := ll.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Give the goroutine time to exit. If the fix regresses, the panic fires
	// here and takes the test binary with it — there's no good way to recover
	// from a goroutine panic, so a hang-or-crash is the signal.
	time.Sleep(100 * time.Millisecond)

	// Sanity check: the test goroutine is still alive and well.
	runtime.GC()
}
