//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func genesisSignal(t *testing.T, replyTo string, directExit bool) common.SignalMsg {
	t.Helper()
	return genesisSignalOf(t, replyTo, common.GenesisMsg{DirectExitObfs: directExit})
}

func genesisSignalOf(t *testing.T, replyTo string, g common.GenesisMsg) common.SignalMsg {
	t.Helper()
	p, err := json.Marshal(g)
	require.NoError(t, err)
	return common.SignalMsg{ReplyTo: replyTo, Type: common.SignalMsgGenesis, Payload: string(p)}
}

func TestConsumerAnswersDirectExitGenesisWithICE(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", true))
	o.OnExitPath = func(ExitPath) {}
	pc, _ := newTestPeerConnection(t)
	next, input := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	require.Equal(t, 6, next)
	assert.Equal(t, "g", input[0])
	assert.Equal(t, false, input[2], "a pool genesis has no TURN servers")
	assert.Equal(t, webrtc.PeerConnectionStateClosed, pc.ConnectionState())
}

func TestConsumerWithoutExitPathOffersSDP(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", true))
	pc, _ := newTestPeerConnection(t)
	next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	assert.Equal(t, 2, next, "an embedder that can't take a direct exit gets today's path")
}

// While TakeExit reports false, a direct exit genesis gets an SDP offer, so the embedder can fall
// back to today's path after direct exits keep failing.
func TestConsumerTakeExitGatesDirectExit(t *testing.T) {
	for _, take := range []bool{false, true} {
		o := streamingFreddie(t, genesisSignal(t, "g", true))
		o.OnExitPath = func(ExitPath) {}
		o.TakeExit = func() bool { return take }
		pc, _ := newTestPeerConnection(t)
		next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
		if take {
			assert.Equal(t, 6, next)
		} else {
			assert.Equal(t, 2, next)
		}
	}
}

// An old producer's genesis offers an unobfuscated exit, which a new consumer can't use: it gets an
// SDP offer, as from a producer without an exit.
func TestConsumerOffersSDPToLegacyDirectExit(t *testing.T) {
	o := streamingFreddie(t, genesisSignalOf(t, "g", common.GenesisMsg{DirectExit: true}))
	o.OnExitPath = func(ExitPath) {}
	pc, _ := newTestPeerConnection(t)
	next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	assert.Equal(t, 2, next)
}

func TestConsumerOffersSDPToOldProducers(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", false))
	o.OnExitPath = func(ExitPath) {}
	pc, _ := newTestPeerConnection(t)
	next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	assert.Equal(t, 2, next)
}

// State 6 offers the consumer's ICE credentials as an OfferMsg with ICE set and no SDP, keeping the
// consumer's tag and country. A Freddie that refuses it sends the worker back to state 0.
func TestConsumerICEOffer(t *testing.T) {
	forms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the offer POST: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		select {
		case forms <- r.Form:
		default:
			t.Error("more than one offer")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Tag = "tag"
	o.ConsumerCountry = "IR"
	o.OnExitPath = func(ExitPath) {}
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{"g", []webrtc.ICEServer(nil), false})
	assert.Equal(t, 0, next)
	var form url.Values
	select {
	case form = <-forms:
	default:
		t.Fatal("state 6 posted no offer")
	}
	assert.Equal(t, "g", form["send-to"][0])
	assert.Equal(t, "1", form["type"][0], "SignalMsgOffer")
	// Read as the producer reads it: an ICE offer's zero SDP doesn't survive a plain Unmarshal.
	raw, err := json.Marshal(common.SignalMsg{ReplyTo: "c", Type: common.SignalMsgOffer, Payload: form["data"][0]})
	require.NoError(t, err)
	_, decoded, err := common.DecodeSignalMsg(raw)
	require.NoError(t, err)
	offer := decoded.(common.OfferMsg)
	// The decoder drops an ICE offer's SDP, so check the wire form for it too.
	var wire struct {
		SDP *struct {
			SDP string `json:"sdp"`
		} `json:"sdp"`
	}
	require.NoError(t, json.Unmarshal([]byte(form["data"][0]), &wire))
	assert.True(t, wire.SDP == nil || wire.SDP.SDP == "", "an ICE offer carries no SDP")
	require.NotNil(t, offer.ICE)
	assert.NotEmpty(t, offer.ICE.Ufrag)
	assert.NotEmpty(t, offer.ICE.Pwd)
	assert.Empty(t, offer.SDP.SDP)
	assert.Equal(t, "tag", offer.Tag)
	assert.Equal(t, "IR", offer.Country)
}

// An answer missing any of its credentials, or with no candidates, ends the session before the
// consumer sends its own candidates.
func TestConsumerRejectsIncompleteAnswer(t *testing.T) {
	cn, _, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	var iceMsgs, answered atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing a POST: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("type") != strconv.Itoa(int(common.SignalMsgOffer)) {
			iceMsgs.Add(1)
			return
		}
		answered.Add(1)
		p, _ := json.Marshal(common.ICEAnswerMsg{ICE: &common.ICEAnswer{
			Ufrag: "u", Pwd: "p", Password: "pw",
			Candidates: []string{"2 1 udp 1694498815 1.2.3.20 6000 typ srflx raddr 10.0.2.2 rport 5000"},
		}})
		b, _ := json.Marshal(common.SignalMsg{ReplyTo: "answer-id", Type: common.SignalMsgAnswer, Payload: string(p)})
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Net = cn
	o.NATFailTimeout = time.Second
	o.ErrorBackoff = 10 * time.Millisecond
	o.OnExitPath = func(ExitPath) {}
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{"g", []webrtc.ICEServer{{URLs: []string{stunURL}}}, false})
	assert.Equal(t, 0, next)
	require.Equal(t, int32(1), answered.Load(), "the incomplete answer was served")
	assert.Zero(t, iceMsgs.Load(), "no ICE message is sent")
}

// State 7 tells the embedder the session's kind, from the producer's answer, so it sends a pool
// session only what the pool's policy admits.
func TestConsumerExitPathCarriesKind(t *testing.T) {
	for _, personal := range []bool{false, true} {
		o := NewDefaultWebRTCOptions()
		paths := make(chan ExitPath, 1)
		o.OnExitPath = func(p ExitPath) { paths <- p }
		s, err := newICESession(o, true, nil, time.Second)
		require.NoError(t, err)
		fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
		// The session never connects, so it is served until ctx ends.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		next, _ := fsm.state[7](ctx, fsm.com, []interface{}{s, "pin", "pw", personal})
		cancel()
		assert.Equal(t, 0, next)
		select {
		case p := <-paths:
			assert.Equal(t, personal, p.Personal)
			assert.Equal(t, "pin", p.CertPin)
			assert.Equal(t, "pw", p.Password)
		default:
			t.Fatal("no exit path")
		}
	}
}

// A consumer answering a room genesis with a direct exit offer shows Freddie its room tickets, so
// Freddie can vouch for its admission to the producer. Answering a pool genesis, it shows none.
func TestConsumerICEOfferCarriesTicketsForRoomGenesis(t *testing.T) {
	ticket := volunteerTicket(t, testPairingP, helperticket.RoleInviter)
	for _, room := range []bool{false, true} {
		g := genesisSignal(t, "g", true)
		if room {
			g.Room, g.Pairing = true, testPairingP
		}
		raw, err := json.Marshal(g)
		require.NoError(t, err)
		headers := make(chan string, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write(append(raw, '\n'))
				return
			}
			headers <- r.Header.Get(common.RoomTicketsHeader)
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		o := NewDefaultWebRTCOptions()
		o.DiscoverySrv = srv.URL
		o.Patience = 50 * time.Millisecond
		o.SetRoomTickets([]string{ticket})
		o.OnExitPath = func(ExitPath) {}
		fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
		pc, _ := newTestPeerConnection(t)
		next, input := consumerState1(t, fsm, pc)
		require.Equal(t, 6, next)
		next, _ = fsm.state[6](context.Background(), fsm.com, input)
		assert.Equal(t, 0, next)
		select {
		case h := <-headers:
			if room {
				assert.Equal(t, ticket, h)
			} else {
				assert.Empty(t, h, "no tickets for a pool genesis")
			}
		default:
			t.Fatal("no offer was posted")
		}
	}
}

func TestIsRelaySlot(t *testing.T) {
	for _, c := range []struct {
		i, size int
		want    bool
	}{
		{0, 1, false},
		{0, 2, false},
		{1, 2, true},
		{0, 5, false},
		{3, 5, false},
		{4, 5, true},
	} {
		assert.Equal(t, c.want, isRelaySlot(c.i, c.size), "isRelaySlot(%d, %d)", c.i, c.size)
	}
}

// A relay slot answers a genesis that offers a direct exit with an SDP offer, so the consumer keeps
// a relayed path, while any other slot takes the exit.
func TestRelaySlotNeverTakesDirectExit(t *testing.T) {
	for _, relayOnly := range []bool{false, true} {
		o := streamingFreddie(t, genesisSignal(t, "g", true))
		o.OnExitPath = func(ExitPath) {}
		fsm := newConsumerWebRTCRelaySlot(o, nil, newRoomPreference(time.Now), nil, relayOnly)
		pc, _ := newTestPeerConnection(t)
		next, _ := consumerState1(t, fsm, pc)
		if relayOnly {
			assert.Equal(t, 2, next, "the relay slot offers SDP")
		} else {
			assert.Equal(t, 6, next, "another slot takes the direct exit")
		}
	}
}
