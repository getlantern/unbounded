//go:build !js

package clientcore

import (
	"net"
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
