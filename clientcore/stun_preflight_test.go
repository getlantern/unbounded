//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

// stunServer starts a local UDP server that answers binding requests with reply, or never answers
// when reply is nil, and returns its URI.
func stunServer(t *testing.T, reply func(req *stun.Message, from *net.UDPAddr) *stun.Message) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			req := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if req.Decode() != nil || reply == nil {
				continue
			}
			if res := reply(req, from); res != nil {
				_, _ = conn.WriteToUDP(res.Raw, from)
			}
		}
	}()
	return "stun:" + conn.LocalAddr().String()
}

func answer(req *stun.Message, from *net.UDPAddr) *stun.Message {
	return stun.MustBuild(req, stun.BindingSuccess, &stun.XORMappedAddress{IP: from.IP, Port: from.Port})
}

func TestSTUNPreflightOK(t *testing.T) {
	res := stunPreflight(context.Background(), []string{stunServer(t, nil), stunServer(t, answer)}, time.Second)
	if res.Outcome != STUNPreflightOK || res.Servers != 2 {
		t.Fatalf("got %+v, want ok from 2 servers", res)
	}
}

func TestSTUNPreflightNoReply(t *testing.T) {
	const timeout = 300 * time.Millisecond
	res := stunPreflight(context.Background(), []string{stunServer(t, nil)}, timeout)
	if res.Outcome != STUNPreflightNoReply {
		t.Fatalf("got %+v, want no_reply", res)
	}
	if res.Elapsed < timeout || res.Elapsed > timeout+time.Second {
		t.Fatalf("took %v, want about %v", res.Elapsed, timeout)
	}
}

// A reply to some other transaction isn't an answer to ours.
func TestSTUNPreflightIgnoresOtherTransactions(t *testing.T) {
	other := func(_ *stun.Message, from *net.UDPAddr) *stun.Message {
		return stun.MustBuild(stun.TransactionID, stun.BindingSuccess, &stun.XORMappedAddress{IP: from.IP, Port: from.Port})
	}
	if res := stunPreflight(context.Background(), []string{stunServer(t, other)}, 300*time.Millisecond); res.Outcome != STUNPreflightNoReply {
		t.Fatalf("got %+v, want no_reply", res)
	}
}

// The request is sent again halfway through, so one lost request doesn't fail the preflight.
func TestSTUNPreflightResends(t *testing.T) {
	var seen int
	dropFirst := func(req *stun.Message, from *net.UDPAddr) *stun.Message {
		if seen++; seen == 1 {
			return nil
		}
		return answer(req, from)
	}
	if res := stunPreflight(context.Background(), []string{stunServer(t, dropFirst)}, time.Second); res.Outcome != STUNPreflightOK {
		t.Fatalf("got %+v, want ok from the resent request", res)
	}
}

func TestSTUNPreflightNoServers(t *testing.T) {
	res := stunPreflight(context.Background(), []string{"", "not a uri", "turn:127.0.0.1:3478"}, time.Second)
	if res.Outcome != STUNPreflightNoServers {
		t.Fatalf("got %+v, want no_servers", res)
	}
}

func TestSTUNPreflightStopsWithCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	stunPreflight(ctx, []string{stunServer(t, nil)}, time.Minute)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %v after ctx ended", waited)
	}
}

func TestPreflightStateBacksOffAndResets(t *testing.T) {
	var p preflightState
	now := time.Now()
	if !p.due(now) {
		t.Fatal("a new worker must preflight")
	}
	want := stunPreflightMinBackoff
	for i := 0; i < 10; i++ {
		if got := p.failed(); got != want {
			t.Fatalf("failure %d: backoff %v, want %v", i+1, got, want)
		}
		want = min(2*want, stunPreflightMaxBackoff)
	}
	p.passed(now)
	if p.due(now.Add(stunPreflightValidFor)) {
		t.Fatal("a passed preflight must hold for stunPreflightValidFor")
	}
	if !p.due(now.Add(stunPreflightValidFor + time.Second)) {
		t.Fatal("a passed preflight must expire")
	}
	if got := p.failed(); got != stunPreflightMinBackoff {
		t.Fatalf("a pass must reset the backoff, got %v", got)
	}
	p.passed(now)
	p.invalidate()
	if !p.due(now) {
		t.Fatal("invalidate must make the next attempt preflight")
	}
}

// A volunteer whose STUN servers don't answer must not go on to ask for an offer.
func TestProducerStopsBeforeGenesisWhenPreflightFails(t *testing.T) {
	silent := stunServer(t, nil)
	o := NewDefaultWebRTCOptions()
	o.STUNBatch = func(uint32) ([]string, error) { return []string{silent}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []STUNPreflightResult
	// Cancelling once the result is in cuts the backoff sleep short.
	o.OnSTUNPreflight = func(r STUNPreflightResult) { got = append(got, r); cancel() }

	fsm := NewProducerWebRTC(o, nil)
	if next, _ := fsm.state[0](ctx, fsm.com, nil); next != 0 {
		t.Fatalf("went to state %d after a failed preflight, want 0", next)
	}
	if len(got) != 1 || got[0].Outcome != STUNPreflightNoReply {
		t.Fatalf("OnSTUNPreflight got %+v, want one no_reply", got)
	}
}

func TestProducerContinuesWhenPreflightPasses(t *testing.T) {
	server := stunServer(t, answer)
	o := NewDefaultWebRTCOptions()
	o.STUNBatch = func(uint32) ([]string, error) { return []string{server}, nil }
	var got []STUNPreflightResult
	o.OnSTUNPreflight = func(r STUNPreflightResult) { got = append(got, r) }

	fsm := NewProducerWebRTC(o, nil)
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	if len(out) > 0 {
		if pc, ok := out[0].(*webrtc.PeerConnection); ok {
			defer pc.Close()
		}
	}
	if next != 1 {
		t.Fatalf("went to state %d after a passed preflight, want 1", next)
	}
	// A second attempt within stunPreflightValidFor doesn't preflight again.
	next, out = fsm.state[0](context.Background(), fsm.com, nil)
	if len(out) > 0 {
		if pc, ok := out[0].(*webrtc.PeerConnection); ok {
			defer pc.Close()
		}
	}
	if next != 1 || len(got) != 1 || got[0].Outcome != STUNPreflightOK {
		t.Fatalf("state %d, OnSTUNPreflight got %+v, want one ok", next, got)
	}
}
