//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
