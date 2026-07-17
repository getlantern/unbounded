package clientcore

import (
	"crypto/tls"
	"testing"
	"time"
)

// TestQUICLayerCloseBeforeListen guards the ctx/cancel initialization fix: a
// QUICLayer must be cancellable via Close() even if Close() runs before
// ListenAndMaintainQUICConnection's goroutine has started. Previously ctx/cancel
// were set at the top of that goroutine, so a Close() that beat it read a nil
// cancel, no-op'd, and the goroutine then ran quic.Listen/Accept on a context
// that never got cancelled — leaking the listener, the goroutine, and bfconn.
func TestQUICLayerCloseBeforeListen(t *testing.T) {
	q, err := NewQUICLayer(&BroflakeConn{}, &tls.Config{})
	if err != nil {
		t.Fatalf("NewQUICLayer: %v", err)
	}
	if q.ctx == nil || q.cancel == nil {
		t.Fatal("NewQUICLayer must initialize ctx/cancel before the maintain goroutine can run")
	}

	// Close before the maintain loop ever starts must cancel the context.
	q.Close()
	if q.ctx.Err() == nil {
		t.Fatal("Close() before ListenAndMaintainQUICConnection did not cancel the context")
	}

	// The maintain loop must then observe the cancelled context and return
	// immediately via its ctx.Err() bailout, rather than spinning quic.Listen.
	done := make(chan struct{})
	go func() {
		q.ListenAndMaintainQUICConnection()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndMaintainQUICConnection did not return after Close()")
	}
}
