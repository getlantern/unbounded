//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// producerExitState2 runs producer state 2 against a Freddie that answers each genesis with reply,
// and returns the next state, its input, and the genesis message the producer sent.
func producerExitState2(t *testing.T, o *WebRTCOptions, reply []byte) (int, []interface{}, common.GenesisMsg, *webrtc.PeerConnection) {
	t.Helper()
	sentCh := make(chan common.GenesisMsg, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var g common.GenesisMsg
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the genesis POST: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal([]byte(r.Form.Get("data")), &g); err != nil {
			t.Errorf("decoding the genesis: %v", err)
			http.Error(w, "bad genesis", http.StatusBadRequest)
			return
		}
		select {
		case sentCh <- g:
		default:
			t.Error("more genesis messages than expected")
		}
		_, _ = w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:1.2.3.1:3478"}}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	pa := common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}
	next, input := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc, pa, make(chan *webrtc.DataChannel, 1), make(chan webrtc.PeerConnectionState, 16), make(chan struct{}, 1),
	})
	var sent common.GenesisMsg
	for len(sentCh) > 0 {
		sent = <-sentCh
	}
	return next, input, sent, pc
}

func readyExit() *ExitOptions {
	return &ExitOptions{
		Ready:  func() bool { return true },
		Params: func() (string, string, bool) { return "pin", "pw", true },
		Serve:  func(context.Context, net.PacketConn, string) {},
	}
}

func offerReply(t *testing.T, offer common.OfferMsg, servers []webrtc.ICEServer) []byte {
	t.Helper()
	p, err := json.Marshal(offer)
	require.NoError(t, err)
	b, err := json.Marshal(common.SignalMsg{ReplyTo: "offer-id", Type: common.SignalMsgOffer, Payload: string(p), ICEServers: servers})
	require.NoError(t, err)
	return b
}

func TestProducerGenesisOffersDirectExitOnlyWhenReady(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	_, _, sent, _ := producerExitState2(t, o, nil)
	assert.False(t, sent.DirectExit, "a producer without Exit, such as a widget")

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	o.Exit.Ready = func() bool { return false }
	_, _, sent, _ = producerExitState2(t, o, nil)
	assert.False(t, sent.DirectExit, "a producer that can't exit now")

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	_, _, sent, _ = producerExitState2(t, o, nil)
	assert.True(t, sent.DirectExit)
}

func TestProducerRoutesICEOfferToDirectExit(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	turnServers := []webrtc.ICEServer{{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"}}
	offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}, Tag: "tag"}
	next, input, _, pc := producerExitState2(t, o, offerReply(t, offer, turnServers))
	require.Equal(t, 6, next)
	assert.Equal(t, "offer-id", input[0])
	assert.Equal(t, offer, input[1])
	servers := input[2].([]webrtc.ICEServer)
	require.Len(t, servers, 2, "the STUN cohort and the room's TURN server")
	assert.Equal(t, "stun:1.2.3.1:3478", servers[0].URLs[0])
	assert.Equal(t, true, input[3])
	assert.Equal(t, webrtc.PeerConnectionStateClosed, pc.ConnectionState(), "a direct exit needs no peer connection")
}

func TestProducerRoutesSDPOfferAsBefore(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	next, _, _, _ := producerExitState2(t, o, offerReply(t, common.OfferMsg{SDP: webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0"}, Tag: "tag"}, nil))
	assert.Equal(t, 3, next, "an old consumer's SDP offer gets today's path")
}

// An ICE offer when the producer can no longer exit is dropped without an answer, so the consumer
// moves on to another genesis.
func TestProducerDropsICEOfferWhenItCantExit(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1) }))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Exit = readyExit()
	o.Exit.Params = func() (string, string, bool) { return "", "", false }
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{
		"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}, []webrtc.ICEServer(nil), false,
	})
	assert.Equal(t, 0, next)
	assert.Zero(t, posts.Load(), "no answer is sent")
}

// An ICE offer without credentials is dropped before the producer mints any, and reported as bad
// consumer ICE with the next genesis.
func TestProducerDropsICEOfferWithoutCredentials(t *testing.T) {
	cn, _, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	posted := make(chan url.Values, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() == nil {
			posted <- r.PostForm
		}
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	o.Net = cn
	o.Exit = readyExit()
	var minted atomic.Int32
	o.Exit.Params = func() (string, string, bool) { minted.Add(1); return "pin", "pw", true }
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{
		"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Pwd: "p"}}, []webrtc.ICEServer{{URLs: []string{stunURL}}}, false,
	})
	assert.Equal(t, 0, next)
	assert.Zero(t, minted.Load(), "no credentials are minted")
	select {
	case <-posted:
		t.Fatal("answered an offer without credentials")
	default:
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	producerState2(fsm, pc)
	select {
	case form := <-posted:
		assert.Equal(t, common.OutcomeBadConsumerICE, form.Get(common.PrevOutcomeField))
	case <-time.After(5 * time.Second):
		t.Fatal("the producer posted no next genesis")
	}
}
