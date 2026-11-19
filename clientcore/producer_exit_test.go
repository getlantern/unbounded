//go:build !wasm

package clientcore

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
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
	assert.False(t, sent.DirectExitObfs, "a producer without Exit, such as a widget")
	assert.False(t, sent.DirectExit)

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	o.Exit.Ready = func(bool) bool { return false }
	_, _, sent, _ = producerExitState2(t, o, nil)
	assert.False(t, sent.DirectExitObfs, "a producer that can't exit now")
	assert.False(t, sent.DirectExit)

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	_, _, sent, _ = producerExitState2(t, o, nil)
	assert.True(t, sent.DirectExitObfs)
	assert.False(t, sent.DirectExit, "a new producer never sends the legacy unobfuscated signal")
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
	assert.Len(t, input, 4, "the session's kind comes from the offer, in state 6")
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
				"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}, []webrtc.ICEServer(nil), false,
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

// A genesis offers a direct exit while the producer can serve a kind of session that a consumer
// reaching it could get: a rooms-only genesis with tickets reaches only the producer's rooms, so
// personal; one reaching the pool alone, pool; one reaching both, either.
func TestProducerGenesisAdvertisesExit(t *testing.T) {
	type readiness struct{ pool, personal bool }
	for _, route := range []struct {
		name      string
		roomSlot  bool
		tickets   []string
		servePool bool
		// offered maps the exit's readiness to whether the genesis offers a direct exit.
		offered func(r readiness) bool
	}{
		{"pool slot", false, nil, true, func(r readiness) bool { return r.pool }},
		{"pool slot with tickets (mixed)", false, []string{"a.b"}, true, func(r readiness) bool { return r.pool || r.personal }},
		{"room slot with tickets", true, []string{"a.b"}, true, func(r readiness) bool { return r.personal }},
		{"room slot without tickets", true, nil, true, func(r readiness) bool { return r.pool }},
		{"rooms only with tickets", false, []string{"a.b"}, false, func(r readiness) bool { return r.personal }},
		// A rooms-only genesis without tickets reaches nobody, and is counted as pool.
		{"rooms only without tickets", false, nil, false, func(r readiness) bool { return r.pool }},
	} {
		for _, r := range []readiness{{false, false}, {true, false}, {false, true}, {true, true}} {
			t.Run(fmt.Sprintf("%s/pool=%v,personal=%v", route.name, r.pool, r.personal), func(t *testing.T) {
				o := NewDefaultWebRTCOptions()
				o.SetRoomTickets(route.tickets)
				o.SetServePool(route.servePool)
				o.Exit = readyExit()
				o.Exit.Ready = func(personal bool) bool {
					if personal {
						return r.personal
					}
					return r.pool
				}
				_, _, sent, _ := producerExitState2Slot(t, o, route.roomSlot, nil, nil)
				assert.Equal(t, route.offered(r), sent.DirectExitObfs)
				assert.False(t, sent.DirectExit)
			})
		}
	}
}

// producerExitAnswer runs producer state 6 on offer, with o's exit, against a Freddie that records
// the producer's answer and hangs up on it, and returns the answer, if any, and the next state.
func producerExitAnswer(t *testing.T, o *WebRTCOptions, offer common.OfferMsg) (*common.ICEAnswer, int) {
	t.Helper()
	cn, _, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	answers := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() == nil {
			answers <- r.PostForm.Get("data")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	o.Net = cn
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	next, _ := fsm.state[6](ctx, fsm.com, []interface{}{
		"offer-id", offer, []webrtc.ICEServer{{URLs: []string{stunURL}}}, false,
	})
	select {
	case data := <-answers:
		var msg common.ICEAnswerMsg
		require.NoError(t, json.Unmarshal([]byte(data), &msg))
		require.NotNil(t, msg.ICE)
		return msg.ICE, next
	default:
		return nil, next
	}
}

// A producer serving both its rooms and the pool decides each direct exit session's kind by its
// offer: an offer claiming the pairing of one of the producer's volunteer tickets is personal,
// any other is pool. The kind reaches the exit's credentials and the consumer, in the answer.
func TestProducerOfferKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tickets  []string
		pairing  string
		personal bool
	}{
		{"the producer's own pairing", []string{volunteerTicket(t, testPairingP, helperticket.RoleVolunteer)}, testPairingP, true},
		{"another of its pairings", []string{
			volunteerTicket(t, testPairingQ, helperticket.RoleVolunteer),
			volunteerTicket(t, testPairingP, helperticket.RoleVolunteer),
		}, testPairingP, true},
		{"an unknown pairing", []string{volunteerTicket(t, testPairingP, helperticket.RoleVolunteer)}, testPairingQ, false},
		{"no pairing", []string{volunteerTicket(t, testPairingP, helperticket.RoleVolunteer)}, "", false},
		{"a pairing the producer holds no volunteer ticket for", []string{volunteerTicket(t, testPairingP, helperticket.RoleInviter)}, testPairingP, false},
		{"a pairing, but no tickets", nil, testPairingP, false},
		// A consumer whose access was revoked still knows the pairing ID, and a producer may still
		// hold its expired ticket.
		{"the producer's pairing, but only an expired ticket for it", []string{
			volunteerTicketExpiring(t, testPairingP, time.Now().Add(-time.Minute)),
		}, testPairingP, false},
		{"the producer's pairing, with an expired and an unexpired ticket", []string{
			volunteerTicketExpiring(t, testPairingP, time.Now().Add(-time.Minute)),
			volunteerTicketExpiring(t, testPairingP, time.Now().Add(time.Hour)),
		}, testPairingP, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewDefaultWebRTCOptions()
			o.SetRoomTickets(tc.tickets)
			o.SetServePool(true)
			o.Exit = readyExit()
			var mu sync.Mutex
			var asked, minted []bool
			o.Exit.Ready = func(personal bool) bool {
				mu.Lock()
				defer mu.Unlock()
				asked = append(asked, personal)
				return true
			}
			o.Exit.Params = func(personal bool) (string, string, bool) {
				mu.Lock()
				defer mu.Unlock()
				minted = append(minted, personal)
				return "pin", "pw", true
			}
			answer, next := producerExitAnswer(t, o, common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}, Pairing: tc.pairing})
			assert.Equal(t, 0, next, "Freddie hung up on the answer")
			require.NotNil(t, answer, "the offer was answered")
			assert.Equal(t, tc.personal, answer.Personal)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []bool{tc.personal}, asked)
			assert.Equal(t, []bool{tc.personal}, minted)
		})
	}
}

// volunteerTicketExpiring is a signed volunteer ticket for pairing that expires at expires.
func volunteerTicketExpiring(t *testing.T, pairing string, expires time.Time) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	tk, err := helperticket.Sign(priv, helperticket.Ticket{PairingID: pairing, Role: helperticket.RoleVolunteer, Expires: expires})
	require.NoError(t, err)
	return tk
}

// A session's kind takes only an unexpired ticket's pairing, while a session's pairing label
// (offerPairing) still names one the producer holds an expired ticket for.
func TestPersonalPairingNeedsUnexpiredTicket(t *testing.T) {
	now := time.Now()
	o := NewDefaultWebRTCOptions()
	o.SetRoomTickets([]string{volunteerTicketExpiring(t, testPairingP, now.Add(time.Minute))})
	assert.True(t, personalPairing(o, testPairingP, now))
	assert.False(t, personalPairing(o, testPairingP, now.Add(time.Minute)), "expired at its expiry")
	assert.False(t, personalPairing(o, testPairingP, now.Add(2*time.Minute)))
	assert.Equal(t, testPairingP, offerPairing(o, testPairingP), "the label is unchanged")
	assert.False(t, personalPairing(o, testPairingQ, now))
	assert.False(t, personalPairing(o, "", now))
}

// The answer marks a pool session by leaving Personal out, so its JSON is what it was before
// sessions had kinds.
func TestICEAnswerOmitsPoolKind(t *testing.T) {
	b, err := json.Marshal(common.ICEAnswer{Ufrag: "u"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "Personal")
	b, err = json.Marshal(common.ICEAnswer{Ufrag: "u", Personal: true})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"Personal":true`)
}

// A mixed genesis offers a direct exit while the producer can serve either kind, so an offer may
// come for the kind it can't serve. That offer is dropped at once, unanswered and with no
// credentials minted, as one that comes after the producer stopped being able to exit: the worker
// goes back to state 0 for its next genesis, and the consumer moves on when Freddie gives up on
// the answer.
func TestProducerDropsOfferForKindNotReady(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readyPool bool
		pairing   string
		personal  bool
	}{
		{"a stranger, with only personal sessions ready", false, "", false},
		{"a friend, with only pool sessions ready", true, testPairingP, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewDefaultWebRTCOptions()
			o.SetRoomTickets([]string{volunteerTicket(t, testPairingP, helperticket.RoleVolunteer)})
			o.SetServePool(true)
			o.Exit = readyExit()
			o.Exit.Ready = func(personal bool) bool { return personal != tc.readyPool }
			var minted atomic.Int32
			o.Exit.Params = func(bool) (string, string, bool) { minted.Add(1); return "pin", "pw", true }

			// The genesis offers a direct exit: a consumer reaching it could get the ready kind.
			offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}, Pairing: tc.pairing}
			next, input, sent, _ := producerExitState2(t, o, offerReply(t, offer, nil))
			assert.True(t, sent.DirectExitObfs)
			require.Equal(t, 6, next)

			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1) }))
			t.Cleanup(srv.Close)
			o.DiscoverySrv = srv.URL
			fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
			done := make(chan int, 1)
			go func() {
				next, _ := fsm.state[6](context.Background(), fsm.com, input)
				done <- next
			}()
			select {
			case next = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("state 6 hung on an offer for a kind it can't serve")
			}
			assert.Equal(t, 0, next)
			assert.Zero(t, posts.Load(), "no answer is sent")
			assert.Zero(t, minted.Load(), "no credentials are minted")
			assert.Equal(t, tc.personal, sessionPersonal(o, offer))
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
