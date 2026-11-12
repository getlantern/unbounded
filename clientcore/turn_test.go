//go:build !js

package clientcore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
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
	fsm := newProducerWebRTC(o, nil, false)
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
