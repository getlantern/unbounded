package clientcore

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/getlantern/broflake/common"
)

func TestExitReady(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	assert.False(t, exitReady(o, false), "no Exit, as in a browser widget")
	o.Exit = &ExitOptions{}
	assert.False(t, exitReady(o, false), "no Ready func")
	ready := true
	var asked []bool
	o.Exit.Ready = func(personal bool) bool { asked = append(asked, personal); return ready }
	assert.False(t, exitReady(o, false), "no Params or Serve func")
	o.Exit.Params = func(bool) (string, string, bool) { return "pin", "pw", true }
	assert.False(t, exitReady(o, false), "no Serve func")
	o.Exit.Serve = func(context.Context, net.PacketConn, string, bool) {}
	assert.True(t, exitReady(o, false))
	assert.True(t, exitReady(o, true))
	assert.Equal(t, []bool{false, true}, asked, "Ready is asked about the genesis's kind")
	ready = false
	assert.False(t, exitReady(o, false))
}

// A producer may exit one kind of session and not the other: an exit with no allowlist for its
// pool sessions offers only personal ones.
func TestExitReadyByKind(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.Exit = &ExitOptions{
		Ready:  func(personal bool) bool { return personal },
		Params: func(bool) (string, string, bool) { return "pin", "pw", true },
		Serve:  func(context.Context, net.PacketConn, string, bool) {},
	}
	assert.True(t, exitReady(o, true))
	assert.False(t, exitReady(o, false))
	_, _, ok := exitParams(o, false)
	assert.False(t, ok, "no pool session's credentials are minted")
}

func TestExitParams(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	_, _, ok := exitParams(o, false)
	assert.False(t, ok, "no Exit")
	ready := true
	o.Exit = &ExitOptions{Ready: func(bool) bool { return ready }}
	_, _, ok = exitParams(o, false)
	assert.False(t, ok, "no Params func")
	var minted []bool
	o.Exit.Params = func(personal bool) (string, string, bool) {
		minted = append(minted, personal)
		return "pin", "pw", true
	}
	o.Exit.Serve = func(context.Context, net.PacketConn, string, bool) {}
	pin, pw, ok := exitParams(o, true)
	assert.True(t, ok)
	assert.Equal(t, "pin", pin)
	assert.Equal(t, "pw", pw)
	assert.Equal(t, []bool{true}, minted, "the credentials are minted for the session's kind")
	ready = false
	_, _, ok = exitParams(o, false)
	assert.False(t, ok, "readiness withdrawn after the genesis went out")
	ready = true
	o.Exit.Serve = nil
	_, _, ok = exitParams(o, false)
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
	// The egress records the subprotocols of each dial, which carry the session ID, and refuses it.
	dials := make(chan string, 4)
	egress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials <- r.Header.Get("Sec-WebSocket-Protocol")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(egress.Close)
	eo := NewDefaultEgressOptions()
	eo.Addr = egress.URL
	eo.ErrorBackoff = 0
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
	// The worker reads its messages in order: had it taken the exit session, the dial would carry
	// its ID, not the WebRTC session's that follows.
	fsm.com.rx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{SessionID: "exit-session", Exit: true}}
	fsm.com.rx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{SessionID: "webrtc-session"}}
	select {
	case protocols := <-dials:
		assert.Contains(t, protocols, "webrtc-session")
		assert.NotContains(t, protocols, "exit-session")
	case <-time.After(5 * time.Second):
		t.Fatal("the WebRTC session never claimed the slot")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("state 0 didn't return after the failed dial")
	}
	assert.Empty(t, dials, "one dial, for the WebRTC session")
}
