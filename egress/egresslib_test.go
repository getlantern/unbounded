package egress

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// TestNewListener_CleanShutdownDoesNotPanic is a regression test for the
// unconditional panic that used to fire from the serve goroutine spawned
// inside NewListener. Closing the listener is a normal shutdown path; the
// goroutine should exit quietly, not take the process down with it.
//
// The assertion is implicit: a panic from the internal serve goroutine is
// fatal to the test binary (goroutine panics can't be recovered from outside
// the panicking goroutine). If this test reaches the end, the fix holds.
func TestNewListener_CleanShutdownDoesNotPanic(t *testing.T) {
	ctx := context.Background()

	tcpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Close the underlying listener even if NewListener errors — ll.Close()
	// only covers the success path, and t.Fatalf skips the explicit close.
	t.Cleanup(func() { _ = tcpL.Close() })

	ll, err := NewListener(ctx, tcpL, &tls.Config{
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	// Clean shutdown path. We don't need to wait for the serve goroutine to
	// enter Accept first — http.Server.Serve handles a pre-closed listener
	// identically to one closed mid-Accept (both surface net.ErrClosed).
	if err := ll.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// We need to give the serve goroutine a moment to run its exit branch
	// before the test returns — if the fix regressed, the panic fires from
	// that goroutine asynchronously after Close(), and we want to observe it.
	//
	// A goroutine-count poll would be ideal instead of a fixed delay, but
	// broflake spawns background workers we can't distinguish from the serve
	// goroutine, so the count never converges cleanly. 100ms is enough for
	// a scheduler tick to run the error-handling branch on any reasonable
	// machine and doesn't meaningfully slow down the test suite.
	time.Sleep(100 * time.Millisecond)
}
