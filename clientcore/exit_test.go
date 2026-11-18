package clientcore

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/getlantern/broflake/common"
)

func TestExitReady(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	assert.False(t, exitReady(o), "no Exit, as in a browser widget")
	o.Exit = &ExitOptions{}
	assert.False(t, exitReady(o), "no Ready func")
	ready := true
	o.Exit.Ready = func() bool { return ready }
	assert.False(t, exitReady(o), "no Params or Serve func")
	o.Exit.Params = func() (string, string, bool) { return "pin", "pw", true }
	assert.False(t, exitReady(o), "no Serve func")
	o.Exit.Serve = func(context.Context, net.PacketConn, string) {}
	assert.True(t, exitReady(o))
	ready = false
	assert.False(t, exitReady(o))
}

func TestExitParams(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	_, _, ok := exitParams(o)
	assert.False(t, ok, "no Exit")
	o.Exit = &ExitOptions{Ready: func() bool { return true }}
	_, _, ok = exitParams(o)
	assert.False(t, ok, "no Params func")
	o.Exit.Params = func() (string, string, bool) { return "pin", "pw", true }
	o.Exit.Serve = func(context.Context, net.PacketConn, string) {}
	pin, pw, ok := exitParams(o)
	assert.True(t, ok)
	assert.Equal(t, "pin", pin)
	assert.Equal(t, "pw", pw)
	o.Exit.Serve = nil
	_, _, ok = exitParams(o)
	assert.False(t, ok, "nothing to serve the session with")
}

func TestTakeExit(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	assert.False(t, takeExit(o), "no OnExitPath")
	o.TakeExit = func() bool { return true }
	assert.False(t, takeExit(o), "TakeExit alone can't take a session")
	o.OnExitPath = func(ExitPath) {}
	assert.True(t, takeExit(o))
	o.TakeExit = func() bool { return false }
	assert.False(t, takeExit(o), "the embedder backed off")
	o.TakeExit = nil
	assert.True(t, takeExit(o), "no TakeExit takes every direct exit")
}

func TestObserveSession(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	observeSession(o, true) // no Exit: no panic
	var got []bool
	o.Exit = &ExitOptions{OnSession: func(directExit bool) { got = append(got, directExit) }}
	observeSession(o, true)
	observeSession(o, false)
	assert.Equal(t, []bool{true, false}, got)
}

// A direct exit session's ConsumerInfo reaches the JIT egress worker like any other, but the
// worker must not dial the egress for it: the producer exits that session itself.
func TestJITEgressIgnoresExitSessions(t *testing.T) {
	eo := NewDefaultEgressOptions()
	eo.Addr = "http://127.0.0.1:1" // never dialed
	var wg sync.WaitGroup
	fsm := NewJITEgressConsumer(eo, nil, &wg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		next, _ := fsm.state[0](ctx, fsm.com, nil)
		done <- next
	}()
	// State 0 first offers the slot to the producer.
	select {
	case msg := <-fsm.com.tx:
		assert.Equal(t, PathAssertionIPC, msg.IpcType)
	case <-time.After(5 * time.Second):
		t.Fatal("no path assertion")
	}
	fsm.com.rx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{SessionID: "s", Exit: true}}
	select {
	case msg := <-fsm.com.tx:
		t.Fatalf("an exit session claimed the egress slot: %+v", msg)
	case next := <-done:
		t.Fatalf("state 0 went on to state %d for an exit session", next)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-done
}
