//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

func genesisSignal(t *testing.T, replyTo string, directExit bool) common.SignalMsg {
	t.Helper()
	p, err := json.Marshal(common.GenesisMsg{DirectExit: directExit})
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
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		form = r.Form
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
	require.NotNil(t, form)
	assert.Equal(t, "g", form["send-to"][0])
	assert.Equal(t, "1", form["type"][0], "SignalMsgOffer")
	// Read as the producer reads it: an ICE offer's zero SDP doesn't survive a plain Unmarshal.
	raw, err := json.Marshal(common.SignalMsg{ReplyTo: "c", Type: common.SignalMsgOffer, Payload: form["data"][0]})
	require.NoError(t, err)
	_, decoded, err := common.DecodeSignalMsg(raw)
	require.NoError(t, err)
	offer := decoded.(common.OfferMsg)
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
	var iceMsgs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.Form.Get("type") != strconv.Itoa(int(common.SignalMsgOffer)) {
			iceMsgs.Add(1)
			return
		}
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
	assert.Zero(t, iceMsgs.Load(), "no ICE message is sent")
}
