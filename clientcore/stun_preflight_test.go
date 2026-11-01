//go:build !wasm

package clientcore

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// stunServer starts a local UDP server that answers binding requests with reply, or never answers
// when reply is nil, and returns its URI.
func stunServer(t *testing.T, reply func(req *stun.Message, from *net.UDPAddr) *stun.Message) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return serveSTUN(t, conn, reply)
}

func serveSTUN(t *testing.T, conn *net.UDPConn, reply func(req *stun.Message, from *net.UDPAddr) *stun.Message) string {
	t.Helper()
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
	res := stunPreflight(context.Background(), nil, []string{stunServer(t, nil), stunServer(t, answer)}, time.Second)
	if res.Outcome != STUNPreflightOK || res.Servers != 2 {
		t.Fatalf("got %+v, want ok from 2 servers", res)
	}
}

func TestSTUNPreflightNoReply(t *testing.T) {
	const timeout = 300 * time.Millisecond
	res := stunPreflight(context.Background(), nil, []string{stunServer(t, nil)}, timeout)
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
	if res := stunPreflight(context.Background(), nil, []string{stunServer(t, other)}, 300*time.Millisecond); res.Outcome != STUNPreflightNoReply {
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
	if res := stunPreflight(context.Background(), nil, []string{stunServer(t, dropFirst)}, time.Second); res.Outcome != STUNPreflightOK {
		t.Fatalf("got %+v, want ok from the resent request", res)
	}
}

func TestSTUNPreflightIPv6(t *testing.T) {
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	server := serveSTUN(t, conn, answer)
	if res := stunPreflight(context.Background(), nil, []string{server}, time.Second); res.Outcome != STUNPreflightOK {
		t.Fatalf("got %+v from %s, want ok", res, server)
	}
}

// A name with both an IPv4 and an IPv6 address passes when only one family answers.
func TestSTUNPreflightTriesBothFamilies(t *testing.T) {
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", "localhost")
	if err != nil || !slices.ContainsFunc(ips, func(ip net.IP) bool { return ip.To4() != nil }) ||
		!slices.ContainsFunc(ips, func(ip net.IP) bool { return ip.To4() == nil }) {
		t.Skip("localhost doesn't resolve to both families here:", ips, err)
	}
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	serveSTUN(t, conn, answer)
	server := "stun:localhost:" + strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
	if res := stunPreflight(context.Background(), nil, []string{server}, time.Second); res.Outcome != STUNPreflightOK {
		t.Fatalf("got %+v from %s, want ok over IPv6", res, server)
	}
}

// Servers are probed independently: one that doesn't resolve or doesn't answer doesn't keep
// another's answer from counting.
func TestSTUNPreflightProbesServersIndependently(t *testing.T) {
	servers := []string{"stun:no-such-host.invalid:3478", stunServer(t, nil), stunServer(t, answer)}
	start := time.Now()
	res := stunPreflight(context.Background(), nil, servers, 5*time.Second)
	if res.Outcome != STUNPreflightOK || res.Servers != 3 {
		t.Fatalf("got %+v, want ok from 3 servers", res)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("took %v; the answering server should decide at once", waited)
	}
}

// Without an answer, the result is the furthest any server got.
func TestSTUNPreflightReportsFurthestFailure(t *testing.T) {
	res := stunPreflight(context.Background(), nil, []string{"not a uri", stunServer(t, nil)}, 300*time.Millisecond)
	if res.Outcome != STUNPreflightNoReply {
		t.Fatalf("got %+v, want no_reply", res)
	}
}

// A probe that starts late, after a slow lookup, still gets its resend before the deadline.
func TestProbeAddrResendsWithinRemainingTime(t *testing.T) {
	var seen int
	dropFirst := func(req *stun.Message, from *net.UDPAddr) *stun.Message {
		if seen++; seen == 1 {
			return nil
		}
		return answer(req, from)
	}
	server := stunServer(t, dropFirst)
	uri, err := stun.ParseURI(server)
	if err != nil {
		t.Fatal(err)
	}
	// What's left of stunPreflightTimeout after a 2.4s lookup, less than half of it.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if got := probeAddr(ctx, hostProber().listen, &net.UDPAddr{IP: net.ParseIP(uri.Host), Port: uri.Port}); got != STUNPreflightOK {
		t.Fatalf("got %s, want ok from the resent request", got)
	}
}

func TestSTUNPreflightNoServers(t *testing.T) {
	res := stunPreflight(context.Background(), nil, []string{"", "not a uri", "turn:127.0.0.1:3478"}, time.Second)
	if res.Outcome != STUNPreflightNoServers {
		t.Fatalf("got %+v, want no_servers", res)
	}
}

func TestSTUNPreflightStopsWithCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	stunPreflight(ctx, nil, []string{stunServer(t, nil)}, time.Minute)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %v after ctx ended", waited)
	}
}

func TestPreflightStateBacksOffAndResets(t *testing.T) {
	var p preflightState
	now := time.Now()
	cohort := []string{"stun:192.0.2.1:3478"}
	if !p.due(now, cohort) {
		t.Fatal("a new worker must preflight")
	}
	if p.expired(now) {
		t.Fatal("a worker that hasn't passed has nothing to expire; state 0 checks it")
	}
	want := stunPreflightMinBackoff
	for i := 0; i < 10; i++ {
		if got := p.failed(); got != want {
			t.Fatalf("failure %d: backoff %v, want %v", i+1, got, want)
		}
		want = min(2*want, stunPreflightMaxBackoff)
	}
	p.passed(now, cohort)
	if p.due(now.Add(stunPreflightValidFor), cohort) {
		t.Fatal("a passed preflight must hold for stunPreflightValidFor")
	}
	if !p.due(now.Add(stunPreflightValidFor+time.Second), cohort) || !p.expired(now.Add(stunPreflightValidFor+time.Second)) {
		t.Fatal("a passed preflight must expire")
	}
	if p.expired(now.Add(stunPreflightValidFor)) {
		t.Fatal("a pass mustn't expire early")
	}
	if !p.due(now, []string{"stun:192.0.2.2:3478"}) {
		t.Fatal("a passed preflight must not vouch for another cohort")
	}
	if got := p.failed(); got != stunPreflightMinBackoff {
		t.Fatalf("a pass must reset the backoff, got %v", got)
	}
	p.passed(now, cohort)
	p.invalidate()
	if !p.due(now, cohort) {
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

// A worker that loops through states 1 and 2 past its pass goes back to state 0 to check again,
// without sending genesis.
func TestProducerRechecksExpiredPassBeforeGenesis(t *testing.T) {
	server := stunServer(t, answer)
	var genesis atomic.Int32
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		genesis.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(freddie.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = freddie.URL
	o.STUNBatch = func(uint32) ([]string, error) { return []string{server}, nil }

	now := time.Now()
	preflightNow = func() time.Time { return now }
	t.Cleanup(func() { preflightNow = time.Now })

	fsm := NewProducerWebRTC(o, nil)
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	if next != 1 {
		t.Fatalf("went to state %d after a passed preflight, want 1", next)
	}
	pc := out[0].(*webrtc.PeerConnection)
	defer pc.Close()

	now = now.Add(stunPreflightValidFor + time.Second)
	next, _ = fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc,
		common.PathAssertion{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	if next != 0 || genesis.Load() != 0 {
		t.Fatalf("went to state %d after %d genesis requests, want state 0 and none", next, genesis.Load())
	}
}

func TestPreflightReportWaitsForAPass(t *testing.T) {
	var r preflightReport
	r.record(STUNPreflightNoReply, "connected")
	r.record(STUNPreflightNoSocket, "connecting")
	form := url.Values{}
	r.addTo(form)
	if len(form) != 0 {
		t.Fatalf("failures alone must wait for a pass, got %v", form)
	}

	r.record(STUNPreflightOK, "disconnected")
	r.addTo(form)
	want := url.Values{
		common.PreflightVPNField:          {"disconnected"},
		common.PrevPreflightFailuresField: {"2"},
		common.PrevPreflightOutcomeField:  {STUNPreflightNoSocket},
		common.PrevPreflightVPNField:      {"connecting"},
	}
	if !reflect.DeepEqual(form, want) {
		t.Fatalf("got %v, want %v", form, want)
	}

	// Pending until delivered, so a genesis that never reached Freddie doesn't lose it.
	again := url.Values{}
	r.addTo(again)
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("an undelivered report must be sent again, got %v", again)
	}
	r.delivered()
	form = url.Values{}
	r.addTo(form)
	if len(form) != 0 {
		t.Fatalf("a delivered report must not be sent again, got %v", form)
	}

	// The count stops where Freddie stops accepting it.
	for range common.MaxReportedPreflightFailures + 5 {
		r.record(STUNPreflightNoReply, "")
	}
	r.record(STUNPreflightOK, "")
	form = url.Values{}
	r.addTo(form)
	if n, ok := common.ParsePreflightFailures(form.Get(common.PrevPreflightFailuresField)); !ok || n != common.MaxReportedPreflightFailures {
		t.Fatalf("a long run of failures must report the cap, got %q", form.Get(common.PrevPreflightFailuresField))
	}
	r.delivered()
	r.record(STUNPreflightOK, "")
	form = url.Values{}
	r.addTo(form)
	if !reflect.DeepEqual(form, url.Values{common.PreflightVPNField: {""}}) {
		t.Fatalf("a pass with no failures reports only its VPN state, got %v", form)
	}
}

// A volunteer whose preflight fails and then passes tells Freddie about both on its next genesis,
// with the VPN state its app reported at each.
func TestProducerReportsPreflightsOnGenesis(t *testing.T) {
	var answering atomic.Bool
	server := stunServer(t, func(req *stun.Message, from *net.UDPAddr) *stun.Message {
		if !answering.Load() {
			return nil
		}
		return answer(req, from)
	})
	// The first genesis is refused, as for a bad protocol version, and the second fails at a proxy
	// in front of Freddie, both before Freddie counts anything. Only a genesis Freddie takes clears
	// the report.
	forms := make(chan url.Values, 4)
	var requests atomic.Int32
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		forms <- r.PostForm
		switch requests.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTeapot)
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(freddie.Close)

	vpn := "connected"
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = freddie.URL
	o.STUNBatch = func(uint32) ([]string, error) { return []string{server}, nil }
	o.VPNStatus = func() string { return vpn }
	o.ErrorBackoff = time.Millisecond
	fsm := NewProducerWebRTC(o, nil)

	// The failed preflight's backoff ends with ctx, which the hook cancels once the result is in.
	ctx, cancel := context.WithCancel(context.Background())
	o.OnSTUNPreflight = func(STUNPreflightResult) {
		if !answering.Load() {
			time.AfterFunc(10*time.Millisecond, cancel)
		}
	}
	if next, _ := fsm.state[0](ctx, fsm.com, nil); next != 0 {
		t.Fatalf("went to state %d after a failed preflight, want 0", next)
	}

	answering.Store(true)
	vpn = "disconnected"
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	if next != 1 {
		t.Fatalf("went to state %d after a passed preflight, want 1", next)
	}
	pc := out[0].(*webrtc.PeerConnection)
	defer pc.Close()
	genesis := func() url.Values {
		t.Helper()
		fsm.state[2](context.Background(), fsm.com, []interface{}{
			pc, common.PathAssertion{}, make(chan *webrtc.DataChannel, 1),
			make(chan webrtc.PeerConnectionState, 1), make(chan struct{}, 1),
		})
		select {
		case form := <-forms:
			return form
		default:
			t.Fatal("no genesis was sent")
			return nil
		}
	}
	want := map[string]string{
		common.PreflightVPNField:          "disconnected",
		common.PrevPreflightFailuresField: "1",
		common.PrevPreflightOutcomeField:  STUNPreflightNoReply,
		common.PrevPreflightVPNField:      "connected",
	}
	// The two that didn't reach Freddie and the one that did all carry the report; the next doesn't.
	for i, form := range []url.Values{genesis(), genesis(), genesis()} {
		for field, w := range want {
			if got := form.Get(field); got != w {
				t.Errorf("genesis %d: %s = %q, want %q", i+1, field, got, w)
			}
		}
	}
	if form := genesis(); form.Has(common.PreflightVPNField) {
		t.Errorf("a delivered report was sent again: %v", form)
	}
}

// listenCountingNet counts the UDP sockets the preflight opens on it. Like ICE's server-reflexive
// gathering, the preflight listens and never dials, so DialUDP fails.
type listenCountingNet struct {
	*stdnet.Net
	listens atomic.Int32
}

func (n *listenCountingNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	n.listens.Add(1)
	return n.Net.ListenUDP(network, laddr)
}

func (n *listenCountingNet) DialUDP(string, *net.UDPAddr, *net.UDPAddr) (transport.UDPConn, error) {
	return nil, errors.New("DialUDP not supported")
}

// blockingNet's ListenUDP blocks until release closes, then opens a socket.
type blockingNet struct {
	*stdnet.Net
	release chan struct{}
	opened  chan transport.UDPConn
}

func (n *blockingNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	<-n.release
	c, err := n.Net.ListenUDP(network, laddr)
	if err == nil {
		n.opened <- c
	}
	return c, err
}

// With WebRTCOptions.Net set, the preflight probes through it, as pion's sockets do, so it tests
// the path ICE will use rather than the host's default.
func TestSTUNPreflightDialsOnNet(t *testing.T) {
	std, err := stdnet.NewNet()
	if err != nil {
		t.Fatal(err)
	}
	n := &listenCountingNet{Net: std}
	if res := stunPreflight(context.Background(), n, []string{stunServer(t, answer)}, time.Second); res.Outcome != STUNPreflightOK {
		t.Fatalf("got %+v, want ok", res)
	}
	if n.listens.Load() == 0 {
		t.Fatal("the preflight opened its socket on the host's stack, not on the Net it was given")
	}
}

// A Net whose ListenUDP blocks can't hold the preflight past its timeout, and the socket it opens
// late is closed.
func TestSTUNPreflightBoundsABlockedListen(t *testing.T) {
	std, err := stdnet.NewNet()
	if err != nil {
		t.Fatal(err)
	}
	n := &blockingNet{Net: std, release: make(chan struct{}), opened: make(chan transport.UDPConn, 1)}
	start := time.Now()
	res := stunPreflight(context.Background(), n, []string{stunServer(t, answer)}, 200*time.Millisecond)
	if res.Outcome != STUNPreflightNoSocket {
		t.Fatalf("got %+v, want %s", res, STUNPreflightNoSocket)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the preflight waited %v on a blocked ListenUDP", took)
	}
	close(n.release)
	c := <-n.opened
	deadline := time.Now().Add(2 * time.Second)
	for c.SetReadDeadline(time.Time{}) == nil {
		if time.Now().After(deadline) {
			t.Fatal("a socket opened after the preflight ended was left open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
