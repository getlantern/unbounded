//go:build !js

package clientcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/covertdtls"
)

func startTURN(t *testing.T) []webrtc.ICEServer {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	key := turn.GenerateAuthKey("u", "test", "p")
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == "u"
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return []webrtc.ICEServer{{URLs: []string{"turn:" + pc.LocalAddr().String() + "?transport=udp"}, Username: "u", Credential: "p"}}
}

func TestAddICEServers_KeepsExisting(t *testing.T) {
	stun := []webrtc.ICEServer{{URLs: []string{"stun:stun.example:3478"}}}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: stun})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	turnSrv := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"}}
	if err := addICEServers(pc, nil); err != nil {
		t.Fatal(err)
	}
	if got := pc.GetConfiguration().ICEServers; len(got) != 1 {
		t.Fatalf("an empty list must change nothing, got %+v", got)
	}
	if err := addICEServers(pc, turnSrv); err != nil {
		t.Fatal(err)
	}
	got := pc.GetConfiguration().ICEServers
	if len(got) != 2 || got[0].URLs[0] != stun[0].URLs[0] || got[1].URLs[0] != turnSrv[0].URLs[0] {
		t.Fatalf("got %+v, want STUN then TURN", got)
	}
}

func TestAddICEServers_ConnectsThroughRelay(t *testing.T) {
	ice := startTURN(t)
	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay})
	if err != nil {
		t.Fatal(err)
	}
	defer offerer.Close()
	answerer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay})
	if err != nil {
		t.Fatal(err)
	}
	defer answerer.Close()
	if selectedPath(offerer) != "" {
		t.Fatal("no pair is selected before connecting")
	}
	for _, pc := range []*webrtc.PeerConnection{offerer, answerer} {
		if err := addICEServers(pc, ice); err != nil {
			t.Fatal(err)
		}
	}

	opened := make(chan struct{})
	answerer.OnDataChannel(func(d *webrtc.DataChannel) { d.OnOpen(func() { close(opened) }) })
	if _, err := offerer.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(offerer)
	if err := offerer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := answerer.SetRemoteDescription(*offerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered = webrtc.GatheringCompletePromise(answerer)
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := offerer.SetRemoteDescription(*answerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(15 * time.Second):
		t.Fatal("no data channel through the relay")
	}
	if got := selectedPath(answerer); got != common.PathRelay {
		t.Fatalf("selectedPath = %q, want relay", got)
	}
}

// consumerState1 runs consumer state 1 on pc against a Freddie that streams lines.
func consumerState1(t *testing.T, fsm *WorkerFSM, pc *webrtc.PeerConnection) (int, []interface{}) {
	t.Helper()
	return fsm.state[1](context.Background(), fsm.com, []interface{}{
		pc,
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
}

func streamingFreddie(t *testing.T, msgs ...common.SignalMsg) *WebRTCOptions {
	t.Helper()
	var body []byte
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		body = append(append(body, b...), '\n')
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Patience = 50 * time.Millisecond
	return o
}

// A room genesis's TURN servers are added before the offer, and a peer connection that has them
// never comes back through state 1: it is traded for a fresh one, so they are added at most once
// and never carry over to the next pick.
func TestConsumerAddsRoomTURNOnce(t *testing.T) {
	turnSrv := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"}}
	o := streamingFreddie(t, common.SignalMsg{ReplyTo: "room-genesis", Type: common.SignalMsgGenesis, Payload: `{}`, Room: true, ICEServers: turnSrv})
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	pc, _ := newTestPeerConnection(t)

	next, out := consumerState1(t, fsm, pc)
	if next != 2 || out[1].(string) != "room-genesis" {
		t.Fatalf("state 1 went to state %d answering %v, want 2 answering the room genesis", next, out[1])
	}
	got := pc.GetConfiguration().ICEServers
	if len(got) != 1 || got[0].URLs[0] != turnSrv[0].URLs[0] {
		t.Fatalf("the room's TURN server was not added: %d servers", len(got))
	}

	// State 2 failed before SetLocalDescription and handed the same peer connection back.
	if next, _ := consumerState1(t, fsm, pc); next != 0 {
		t.Fatalf("state 1 on a peer connection with TURN servers went to state %d, want 0", next)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Error("the peer connection with TURN servers was not closed")
	}
	if got := pc.GetConfiguration().ICEServers; len(got) != 1 {
		t.Errorf("the TURN servers were added again: %d servers", len(got))
	}
}

// A pool genesis carries no ICE servers, so its peer connection is left as it was and kept.
func TestConsumerPoolGenesisAddsNoICEServers(t *testing.T) {
	o := streamingFreddie(t, common.SignalMsg{ReplyTo: "pool-genesis", Type: common.SignalMsgGenesis, Payload: `{}`})
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	pc, _ := newTestPeerConnection(t)

	for i := 0; i < 2; i++ {
		if next, _ := consumerState1(t, fsm, pc); next != 2 {
			t.Fatalf("pass %d: state 1 went to state %d, want 2", i+1, next)
		}
		if got := pc.GetConfiguration().ICEServers; len(got) != 0 {
			t.Fatalf("pass %d: a pool genesis added %d ICE servers", i+1, len(got))
		}
	}
}

// The producer carries the offer's TURN servers from state 2 to state 3, which adds them before
// the offer is applied.
func TestProducerAddsOfferTURN(t *testing.T) {
	turnSrv := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"}}
	payload, err := json.Marshal(common.OfferMsg{SDP: webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "not sdp"}})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := json.Marshal(common.SignalMsg{ReplyTo: "offer-id", Type: common.SignalMsgOffer, Payload: string(payload), ICEServers: turnSrv})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(offer)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	pc, _ := newTestPeerConnection(t)

	next, out := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc,
		common.PathAssertion{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	if next != 3 || out[1].(string) != "offer-id" {
		t.Fatalf("state 2 went to state %d with %v, want 3 with the offer", next, out[1])
	}
	if got, _ := out[6].([]webrtc.ICEServer); len(got) != 1 || got[0].URLs[0] != turnSrv[0].URLs[0] {
		t.Fatalf("state 2 did not pass the offer's TURN servers on: %d", len(got))
	}

	// The offer's SDP is invalid, so state 3 stops at SetRemoteDescription, after adding the servers.
	if next, _ := fsm.state[3](context.Background(), fsm.com, out); next != 0 {
		t.Fatalf("state 3 went to state %d, want 0", next)
	}
	if got := pc.GetConfiguration().ICEServers; len(got) != 1 || got[0].URLs[0] != turnSrv[0].URLs[0] {
		t.Fatalf("state 3 did not add the offer's TURN servers: %d", len(got))
	}
}

// blackholeNet is the host's network stack, except that a TCP dial hangs until unblocked, as it
// does when a censor drops a TURN server's SYNs.
type blackholeNet struct {
	*stdnet.Net
	unblock chan struct{}
}

func (n *blackholeNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	<-n.unblock
	return nil, errors.New("blackholed")
}

// shortTURNDial sets turnDialTimeout to d for the rest of the test.
func shortTURNDial(t *testing.T, d time.Duration) {
	t.Helper()
	prev := turnDialTimeout
	turnDialTimeout = d
	t.Cleanup(func() { turnDialTimeout = prev })
}

// A blackholed TCP TURN server holds gathering, but the bounded wait gives up on it in time and
// leaves the host candidates gathered so far, in the snapshot and in the local description. And
// closing the peer connection while the dial still hangs returns once the dial times out, not
// when the OS gives up on it.
func TestAwaitGatheringBoundsBlackholedTURN(t *testing.T) {
	const limit = 500 * time.Millisecond
	// Past limit, so gathering is still held when the bounded wait gives up.
	shortTURNDial(t, 2*time.Second)
	std, err := stdnet.NewNet()
	if err != nil {
		t.Fatal(err)
	}
	bh := &blackholeNet{Net: std, unblock: make(chan struct{})}
	// Registered first, so it runs last: the endpoint stays blackholed through Close.
	defer close(bh.unblock)
	pc, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, iceNet{net: bh})
	if err != nil {
		t.Fatal(err)
	}
	if err := addICEServers(pc, []webrtc.ICEServer{{URLs: []string{"turn:127.0.0.1:3478?transport=tcp"}, Username: "u", Credential: "p"}}); err != nil {
		t.Fatal(err)
	}
	var gathered candidateLog
	pc.OnICECandidate(gathered.add)
	if _, err := pc.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if awaitGathering(gatherComplete, limit) {
		t.Fatal("gathering completed with its TURN server blackholed")
	}
	if waited := time.Since(start); waited > limit+time.Second {
		t.Fatalf("the bounded wait took %v, want about %v", waited, limit)
	}
	cands := gathered.snapshot()
	if len(cands) == 0 {
		t.Fatal("no candidates gathered before the deadline")
	}
	if got := strings.Count(pc.LocalDescription().SDP, "a=candidate:"); got < len(cands) {
		t.Fatalf("the local description carries %d candidates, want at least the %d gathered", got, len(cands))
	}

	closed := make(chan struct{})
	go func() {
		pc.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(turnDialTimeout + 2*time.Second):
		t.Fatal("Close waited on the blackholed TURN dial past turnDialTimeout")
	}
}

// slowNet is the host's network stack, except that a TCP dial waits until released.
type slowNet struct {
	*stdnet.Net
	release chan struct{}
}

func (n *slowNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	<-n.release
	return n.Net.DialTCP(network, laddr, raddr)
}

// A TCP dial that outlasts turnDialTimeout fails at the timeout, and the connection it makes
// after all is closed rather than leaked. One that finishes in time is returned as made.
func TestBoundedDialNet(t *testing.T) {
	shortTURNDial(t, 100*time.Millisecond)
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	raddr := ln.Addr().(*net.TCPAddr)
	std, err := stdnet.NewNet()
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowNet{Net: std, release: make(chan struct{})}
	n := boundedDialNet{slow}

	start := time.Now()
	if _, err := n.DialTCP("tcp4", nil, raddr); !errors.Is(err, errTURNDialTimeout) {
		t.Fatalf("DialTCP = %v, want errTURNDialTimeout", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("DialTCP took %v, want about %v", waited, turnDialTimeout)
	}
	close(slow.release)
	late, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	late.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := late.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the late connection was not closed: read returned %v", err)
	}

	conn, err := n.DialTCP("tcp4", nil, raddr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestAwaitGathering(t *testing.T) {
	never := make(chan struct{})
	start := time.Now()
	if awaitGathering(never, 50*time.Millisecond) {
		t.Fatal("reported complete for gathering that never completes")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the bounded wait took %v", waited)
	}

	done := make(chan struct{})
	close(done)
	if !awaitGathering(done, time.Hour) {
		t.Fatal("bounded wait missed completed gathering")
	}

	// Unbounded, it waits as long as gathering takes.
	later := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(later)
	}()
	start = time.Now()
	if !awaitGathering(later, 0) || time.Since(start) < 100*time.Millisecond {
		t.Fatal("unbounded wait did not wait for gathering to complete")
	}
}

// genesisFreddie answers every POST with an empty body, as Freddie does when nobody answered a
// genesis, and passes on each one's form.
func genesisFreddie(t *testing.T) (*WebRTCOptions, <-chan url.Values) {
	t.Helper()
	posted := make(chan url.Values, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			posted <- r.PostForm
		}
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	o.RoomTickets = []string{"aa.bb"}
	return o, posted
}

// failingSTUN are the two ways STUN fails before an attempt: the STUN list can't be fetched, or
// its servers don't answer the preflight (here at once, as a server that isn't one).
var failingSTUN = []struct {
	name  string
	batch func(uint32) ([]string, error)
}{
	{"list", func(uint32) ([]string, error) { return nil, errors.New("STUN list unavailable") }},
	{"preflight", func(uint32) ([]string, error) { return []string{"turn:127.0.0.1:3478"}, nil }},
}

// producerState2 runs a producer's state 2 on pc.
func producerState2(fsm *WorkerFSM, pc *webrtc.PeerConnection) int {
	next, _ := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc,
		common.PathAssertion{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	return next
}

// A room slot without a mapped address offers itself to its rooms even when STUN fails, since
// Freddie may hand it TURN servers with its room's offer.
func TestRoomProducerGoesOnWithoutSTUN(t *testing.T) {
	for _, tc := range failingSTUN {
		t.Run(tc.name, func(t *testing.T) {
			o, posted := genesisFreddie(t)
			o.STUNBatch = tc.batch
			fsm := newProducerWebRTC(o, nil, true, newVolunteerSignaler(o))

			next, pc := producerState0(t, fsm)
			if next != 1 || pc == nil {
				t.Fatalf("state 0 went to state %d, want 1 and a peer connection", next)
			}
			if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
				t.Errorf("%d ICE servers, want none", len(servers))
			}
			if next := producerState2(fsm, pc); next != 1 {
				t.Fatalf("state 2 went to state %d, want 1", next)
			}
			select {
			case form := <-posted:
				if got := form.Get("send-to"); got != common.SignalSendToGenesisRooms {
					t.Errorf("genesis sent to %q, want %q", got, common.SignalSendToGenesisRooms)
				}
			default:
				t.Fatal("no genesis posted")
			}
		})
	}
}

// A producer that serves the pool still needs STUN without a mapped address: a stranger has no
// TURN servers to offer it.
func TestPoolProducerNeedsSTUN(t *testing.T) {
	for _, tc := range failingSTUN {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := genesisFreddie(t)
			o.STUNBatch = tc.batch
			fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))

			// A failed preflight waits out its backoff, so cut that short.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			next, out := fsm.state[0](ctx, fsm.com, nil)
			if next != 0 || len(out) != 0 {
				t.Fatalf("state 0 went to state %d with %d values, want 0 with none", next, len(out))
			}
		})
	}
}

// A producer that serves only its rooms but has no tickets can't be given TURN servers, so it
// still needs STUN without a mapped address and posts no genesis.
func TestTicketlessRoomsOnlyProducerNeedsSTUN(t *testing.T) {
	for _, tc := range failingSTUN {
		t.Run(tc.name, func(t *testing.T) {
			o, posted := genesisFreddie(t)
			o.RoomTickets = nil
			o.SetServePool(false)
			o.STUNBatch = tc.batch
			fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))

			// A failed preflight waits out its backoff, so cut that short.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			next, out := fsm.state[0](ctx, fsm.com, nil)
			if next != 0 || len(out) != 0 {
				t.Fatalf("state 0 went to state %d with %d values, want 0 with none", next, len(out))
			}
			select {
			case form := <-posted:
				t.Errorf("posted a genesis to %q", form.Get("send-to"))
			default:
			}
		})
	}
}

// A room slot that went without STUN goes back to state 0 rather than offer itself to the pool
// once its tickets are gone.
func TestRoomProducerWithoutSTUNLeavesThePoolAlone(t *testing.T) {
	o, posted := genesisFreddie(t)
	o.STUNBatch = failingSTUN[0].batch
	fsm := newProducerWebRTC(o, nil, true, newVolunteerSignaler(o))
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1 and a peer connection", next)
	}

	roomsMu.Lock()
	o.RoomTickets = nil
	roomsMu.Unlock()
	if next := producerState2(fsm, pc); next != 0 {
		t.Fatalf("state 2 went to state %d, want 0", next)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Error("the peer connection was not closed")
	}
	select {
	case form := <-posted:
		t.Errorf("posted a genesis to %q", form.Get("send-to"))
	default:
	}
}

// A room slot that went without STUN and gets no TURN servers with its offer, as from a Freddie
// that predates them, gathers only host candidates and gives up before answering, as it did
// when it never offered itself. It reports that with its next genesis.
func TestRoomProducerWithoutSTUNOrTURNGivesUp(t *testing.T) {
	o, posted := genesisFreddie(t)
	o.STUNBatch = failingSTUN[0].batch
	fsm := newProducerWebRTC(o, nil, true, newVolunteerSignaler(o))
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1 and a peer connection", next)
	}

	consumer, _ := newTestPeerConnection(t)
	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	next, _ = fsm.state[3](context.Background(), fsm.com, []interface{}{
		pc,
		"reply-to",
		common.OfferMsg{SDP: *consumer.LocalDescription()},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
		[]webrtc.ICEServer(nil),
	})
	if next != 0 {
		t.Fatalf("state 3 went to state %d, want 0", next)
	}
	select {
	case <-posted:
		t.Fatal("answered with host candidates only")
	default:
	}

	_, pc = producerState0(t, fsm)
	if next := producerState2(fsm, pc); next != 1 {
		t.Fatalf("state 2 went to state %d, want 1", next)
	}
	if got := (<-posted).Get(common.PrevOutcomeField); got != common.OutcomeNoLocalSrflx {
		t.Errorf("next genesis reported %q, want %q", got, common.OutcomeNoLocalSrflx)
	}
}

// consumerState0 runs a consumer's state 0 once and returns the state it went to and its peer
// connection, if it built one.
func consumerState0(t *testing.T, fsm *WorkerFSM) (int, *webrtc.PeerConnection) {
	t.Helper()
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	var pc *webrtc.PeerConnection
	if len(out) > 0 {
		pc, _ = out[0].(*webrtc.PeerConnection)
	}
	if pc != nil {
		t.Cleanup(func() { pc.Close() })
	}
	return next, pc
}

// A consumer with room tickets goes on when the STUN list can't be fetched, since its room's
// genesis may carry TURN servers. One without tickets still waits for the list.
func TestTicketedConsumerGoesOnWithoutSTUNList(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.STUNBatch = failingSTUN[0].batch
	if next, pc := consumerState0(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now))); next != 0 || pc != nil {
		t.Fatalf("without tickets, state 0 went to state %d, want 0", next)
	}

	o.RoomTickets = []string{"aa.bb"}
	next, pc := consumerState0(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)))
	if next != 1 || pc == nil {
		t.Fatalf("with tickets, state 0 went to state %d, want 1 and a peer connection", next)
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
		t.Errorf("%d ICE servers, want none", len(servers))
	}
}

// A ticketed consumer without STUN or a mapped address subscribes with its tickets and answers
// only a room genesis that carries TURN servers: neither a pool genesis nor a room genesis
// without them could connect it.
func TestConsumerWithoutSTUNAnswersOnlyRoomTURN(t *testing.T) {
	turnSrv := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"}}
	pool := common.SignalMsg{ReplyTo: "pool-genesis", Type: common.SignalMsgGenesis, Payload: `{}`}
	bare := common.SignalMsg{ReplyTo: "bare-room-genesis", Type: common.SignalMsgGenesis, Payload: `{}`, Room: true}
	relayed := common.SignalMsg{ReplyTo: "turn-room-genesis", Type: common.SignalMsgGenesis, Payload: `{}`, Room: true, ICEServers: turnSrv}

	for _, tc := range []struct {
		name     string
		msgs     []common.SignalMsg
		next     int
		answered string
	}{
		{"none usable", []common.SignalMsg{pool, bare}, 1, ""},
		{"room with TURN", []common.SignalMsg{pool, bare, relayed, pool}, 2, relayed.ReplyTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			for _, m := range tc.msgs {
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				body = append(append(body, b...), '\n')
			}
			subscribed := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case subscribed <- r.Header.Get(common.RoomTicketsHeader):
				default:
				}
				w.Write(body)
			}))
			t.Cleanup(srv.Close)
			o := NewDefaultWebRTCOptions()
			o.DiscoverySrv = srv.URL
			o.Patience = 50 * time.Millisecond
			o.RoomTickets = []string{"aa.bb"}
			o.STUNBatch = failingSTUN[0].batch
			fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
			if next, pc := consumerState0(t, fsm); next != 1 || pc == nil {
				t.Fatalf("state 0 went to state %d, want 1", next)
			} else {
				next, out := consumerState1(t, fsm, pc)
				if next != tc.next {
					t.Fatalf("state 1 went to state %d, want %d", next, tc.next)
				}
				if tc.answered != "" && out[1].(string) != tc.answered {
					t.Errorf("answered %v, want %s", out[1], tc.answered)
				}
			}
			select {
			case got := <-subscribed:
				if got != "aa.bb" {
					t.Errorf("subscribed with tickets %q, want aa.bb", got)
				}
			default:
				t.Fatal("never subscribed")
			}
		})
	}
}

// A consumer that went without STUN goes back to state 0 for it once its tickets are gone.
func TestConsumerWithoutSTUNOrTicketsGoesBack(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.RoomTickets = []string{"aa.bb"}
	o.STUNBatch = failingSTUN[0].batch
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	next, pc := consumerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	roomsMu.Lock()
	o.RoomTickets = nil
	roomsMu.Unlock()
	if next, _ := consumerState1(t, fsm, pc); next != 0 {
		t.Fatalf("state 1 went to state %d, want 0", next)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Error("the peer connection was not closed")
	}
}

// flakySTUN is a STUN list that can't be fetched the first time and is just server afterwards.
func flakySTUN(server string) func(uint32) ([]string, error) {
	var calls atomic.Int32
	return func(uint32) ([]string, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("STUN list unavailable")
		}
		return []string{server}, nil
	}
}

// fixedPreflightNow stops the preflight clock for the test and returns a way to move it.
func fixedPreflightNow(t *testing.T) func(time.Duration) {
	now := time.Now()
	preflightNow = func() time.Time { return now }
	t.Cleanup(func() { preflightNow = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

// A ticketed consumer that went without STUN goes back to state 0 for it once the list fetch's
// backoff ends, rather than wait without it for a room genesis.
func TestConsumerWithoutSTUNRetriesIt(t *testing.T) {
	advance := fixedPreflightNow(t)
	o := streamingFreddie(t)
	o.RoomTickets = []string{"aa.bb"}
	o.STUNBatch = flakySTUN("stun:127.0.0.1:3478")
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))

	next, pc := consumerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if next, _ := consumerState1(t, fsm, pc); next != 1 {
		t.Fatalf("before the backoff ends, state 1 went to state %d, want 1", next)
	}
	advance(stunPreflightMinBackoff)
	if next, _ := consumerState1(t, fsm, pc); next != 0 {
		t.Fatalf("after the backoff, state 1 went to state %d, want 0", next)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Error("the peer connection was not closed")
	}
	next, pc = consumerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 1 {
		t.Errorf("%d ICE servers after the retry, want 1", len(servers))
	}
}

// A room slot that went without STUN goes back to state 0 for it once the list fetch's backoff
// ends, rather than keep offering itself without it.
func TestRoomProducerWithoutSTUNRetriesIt(t *testing.T) {
	advance := fixedPreflightNow(t)
	o, posted := genesisFreddie(t)
	o.STUNBatch = flakySTUN(stunServer(t, answer))
	fsm := newProducerWebRTC(o, nil, true, newVolunteerSignaler(o))

	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if next := producerState2(fsm, pc); next != 1 {
		t.Fatalf("before the backoff ends, state 2 went to state %d, want 1", next)
	}
	<-posted
	advance(stunPreflightMinBackoff)
	if next := producerState2(fsm, pc); next != 0 {
		t.Fatalf("after the backoff, state 2 went to state %d, want 0", next)
	}
	select {
	case form := <-posted:
		t.Errorf("posted a genesis to %q", form.Get("send-to"))
	default:
	}
	next, pc = producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 1 {
		t.Errorf("%d ICE servers after the retry, want 1", len(servers))
	}
}

// emptySTUN is a STUN list fetched without error but empty, as from an empty response body.
func emptySTUN(uint32) ([]string, error) { return []string{}, nil }

// A ticketed consumer whose STUN list comes back empty subscribes without STUN, and goes back
// for the list only once its backoff ends, as after a failed fetch.
func TestConsumerWithEmptySTUNListSubscribes(t *testing.T) {
	advance := fixedPreflightNow(t)
	o := streamingFreddie(t)
	o.RoomTickets = []string{"aa.bb"}
	o.STUNBatch = emptySTUN
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))

	next, pc := consumerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if next, _ := consumerState1(t, fsm, pc); next != 1 {
		t.Fatalf("before the backoff ends, state 1 went to state %d, want 1", next)
	}
	advance(stunPreflightMinBackoff)
	if next, _ := consumerState1(t, fsm, pc); next != 0 {
		t.Fatalf("after the backoff, state 1 went to state %d, want 0", next)
	}
}

// A room slot whose STUN list comes back empty posts its genesis without STUN, and goes back for
// the list only once its backoff ends, as after a failed fetch.
func TestRoomProducerWithEmptySTUNListPosts(t *testing.T) {
	advance := fixedPreflightNow(t)
	o, posted := genesisFreddie(t)
	o.STUNBatch = emptySTUN
	fsm := newProducerWebRTC(o, nil, true, newVolunteerSignaler(o))

	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	if next := producerState2(fsm, pc); next != 1 {
		t.Fatalf("before the backoff ends, state 2 went to state %d, want 1", next)
	}
	select {
	case form := <-posted:
		if got := form.Get("send-to"); got != common.SignalSendToGenesisRooms {
			t.Errorf("genesis sent to %q, want %q", got, common.SignalSendToGenesisRooms)
		}
	default:
		t.Fatal("no genesis posted")
	}
	advance(stunPreflightMinBackoff)
	if next := producerState2(fsm, pc); next != 0 {
		t.Fatalf("after the backoff, state 2 went to state %d, want 0", next)
	}
}
