//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

// producerExitState2 runs producer state 2 against a Freddie that answers each genesis with reply,
// and returns the next state, its input, and the genesis message the producer sent.
func producerExitState2(t *testing.T, o *WebRTCOptions, reply []byte) (int, []interface{}, common.GenesisMsg, *webrtc.PeerConnection) {
	t.Helper()
	return producerExitState2Slot(t, o, false, reply, nil)
}

// producerExitState2Slot is producerExitState2 for a room slot if roomSlot is set. onGenesis, if
// set, runs as Freddie takes the genesis, before it answers.
func producerExitState2Slot(t *testing.T, o *WebRTCOptions, roomSlot bool, reply []byte, onGenesis func()) (int, []interface{}, common.GenesisMsg, *webrtc.PeerConnection) {
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
		if onGenesis != nil {
			onGenesis()
		}
		_, _ = w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	fsm := newProducerWebRTC(o, nil, roomSlot, newVolunteerSignaler(o))
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
		Ready:  func(bool) bool { return true },
		Params: func(bool) (string, string, bool) { return "pin", "pw", true },
		Serve:  func(context.Context, net.PacketConn, string, bool) {},
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
	o.Exit.Ready = func(bool) bool { return false }
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
	assert.Equal(t, false, input[4], "a pool genesis's session")
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
	for _, tc := range []struct {
		name  string
		setup func(e *ExitOptions, minted *atomic.Int32)
	}{
		{"params refused", func(e *ExitOptions, minted *atomic.Int32) {
			e.Params = func(bool) (string, string, bool) { minted.Add(1); return "", "", false }
		}},
		// The genesis offered a direct exit, then the embedder withdrew readiness (its flag turned
		// off) before the offer arrived.
		{"readiness withdrawn", func(e *ExitOptions, minted *atomic.Int32) {
			e.Ready = func(bool) bool { return false }
			e.Params = func(bool) (string, string, bool) { minted.Add(1); return "pin", "pw", true }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, minted atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1) }))
			t.Cleanup(srv.Close)
			o := NewDefaultWebRTCOptions()
			o.DiscoverySrv = srv.URL
			o.Exit = readyExit()
			tc.setup(o.Exit, &minted)
			fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
			next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{
				"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}, []webrtc.ICEServer(nil), false, false,
			})
			assert.Equal(t, 0, next)
			assert.Zero(t, posts.Load(), "no answer is sent")
			if tc.name == "readiness withdrawn" {
				assert.Zero(t, minted.Load(), "no credentials are minted")
			}
		})
	}
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
	o.Exit.Params = func(bool) (string, string, bool) { minted.Add(1); return "pin", "pw", true }
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{
		"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Pwd: "p"}}, []webrtc.ICEServer{{URLs: []string{stunURL}}}, false, false,
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

// A genesis is personal when it goes to the producer's rooms alone with room tickets: a room slot
// holding tickets, or a producer that doesn't serve the pool. Ready is asked about that kind, and
// an offer answering the genesis carries it on to state 6.
func TestProducerGenesisKind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		roomSlot  bool
		tickets   []string
		servePool bool
		personal  bool
	}{
		{"pool slot", false, nil, true, false},
		{"pool slot with tickets", false, []string{"a.b"}, true, false},
		{"room slot with tickets", true, []string{"a.b"}, true, true},
		{"room slot without tickets", true, nil, true, false},
		{"rooms only with tickets", false, []string{"a.b"}, false, true},
		// A rooms-only genesis without tickets reaches nobody, so it is never personal.
		{"rooms only without tickets", false, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewDefaultWebRTCOptions()
			o.SetRoomTickets(tc.tickets)
			o.SetServePool(tc.servePool)
			o.Exit = readyExit()
			var asked []bool
			o.Exit.Ready = func(personal bool) bool { asked = append(asked, personal); return true }
			offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}
			next, input, sent, _ := producerExitState2Slot(t, o, tc.roomSlot, offerReply(t, offer, nil), nil)
			require.Equal(t, 6, next)
			assert.True(t, sent.DirectExit)
			assert.Equal(t, []bool{tc.personal}, asked)
			assert.Equal(t, tc.personal, input[4])
		})
	}
}

// The kind state 6 gets is the kind of the genesis the offer answers, even if the producer's
// tickets or pool setting changed while the genesis waited for it: an offer answering a pool
// genesis is never served as personal, nor one answering a personal genesis as pool.
func TestProducerOfferKeepsItsGenesisKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(o *WebRTCOptions)
		change   func(o *WebRTCOptions)
		roomSlot bool
		personal bool
	}{
		{"pool genesis, then rooms only", func(o *WebRTCOptions) {}, func(o *WebRTCOptions) {
			o.SetRoomTickets([]string{"a.b"})
			o.SetServePool(false)
		}, true, false},
		{"personal genesis, then tickets gone", func(o *WebRTCOptions) {
			o.SetRoomTickets([]string{"a.b"})
		}, func(o *WebRTCOptions) {
			o.SetRoomTickets(nil)
		}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewDefaultWebRTCOptions()
			tc.setup(o)
			o.Exit = readyExit()
			var asked, minted []bool
			var mu sync.Mutex
			o.Exit.Ready = func(personal bool) bool {
				mu.Lock()
				defer mu.Unlock()
				asked = append(asked, personal)
				return true
			}
			// Refusing the credentials ends state 6 before it signals anything.
			o.Exit.Params = func(personal bool) (string, string, bool) {
				mu.Lock()
				defer mu.Unlock()
				minted = append(minted, personal)
				return "", "", false
			}
			offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}
			next, input, _, _ := producerExitState2Slot(t, o, tc.roomSlot, offerReply(t, offer, nil), func() { tc.change(o) })
			require.Equal(t, 6, next)
			assert.Equal(t, tc.personal, input[4])

			fsm := newProducerWebRTC(o, nil, tc.roomSlot, newVolunteerSignaler(o))
			next, _ = fsm.state[6](context.Background(), fsm.com, input)
			assert.Equal(t, 0, next)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []bool{tc.personal, tc.personal}, asked, "the genesis, then state 6's recheck")
			assert.Equal(t, []bool{tc.personal}, minted)
		})
	}
}

// A pool genesis can still reach a consumer in one of the producer's rooms. Its offer then carries
// the room's pairing, which Freddie stamped on the genesis, and is personal if the pairing is one
// of the producer's own volunteer tickets. Any other claim leaves it pool.
func TestProducerPoolGenesisOfferFromItsRoom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ticket   string
		pairing  string
		personal bool
	}{
		{"matching pairing", volunteerTicket(t, testPairingP, helperticket.RoleVolunteer), testPairingP, true},
		{"another pairing", volunteerTicket(t, testPairingP, helperticket.RoleVolunteer), testPairingQ, false},
		{"no pairing", volunteerTicket(t, testPairingP, helperticket.RoleVolunteer), "", false},
		{"an inviter ticket", volunteerTicket(t, testPairingP, helperticket.RoleInviter), testPairingP, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewDefaultWebRTCOptions()
			o.SetRoomTickets([]string{tc.ticket})
			o.Exit = readyExit()
			var asked, minted []bool
			o.Exit.Ready = func(personal bool) bool { asked = append(asked, personal); return true }
			// Refusing the credentials ends state 6 before it signals anything.
			o.Exit.Params = func(personal bool) (string, string, bool) {
				minted = append(minted, personal)
				return "", "", false
			}
			offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}, Pairing: tc.pairing}
			next, input, _, _ := producerExitState2(t, o, offerReply(t, offer, nil))
			require.Equal(t, 6, next)
			assert.Equal(t, false, input[4], "a pool slot's genesis")

			fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
			next, _ = fsm.state[6](context.Background(), fsm.com, input)
			assert.Equal(t, 0, next)
			assert.Equal(t, []bool{false, tc.personal}, asked, "the genesis, then state 6's recheck")
			assert.Equal(t, []bool{tc.personal}, minted)
		})
	}
}

// State 7 serves the session as the kind state 6 handed it.
func TestProducerServesSessionAsItsKind(t *testing.T) {
	for _, personal := range []bool{false, true} {
		o := NewDefaultWebRTCOptions()
		o.Exit = readyExit()
		served := make(chan bool, 1)
		o.Exit.Serve = func(_ context.Context, _ net.PacketConn, password string, personal bool) {
			assert.Equal(t, "pw", password)
			served <- personal
		}
		s, err := newICESession(o, false, nil, time.Second)
		require.NoError(t, err)
		fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
		next, _ := fsm.state[7](context.Background(), fsm.com, []interface{}{s, common.OfferMsg{}, "session", net.IP(nil), "pw", personal})
		assert.Equal(t, 0, next)
		select {
		case got := <-served:
			assert.Equal(t, personal, got)
		default:
			t.Fatal("the session wasn't served")
		}
	}
}
