//go:build !wasm

package clientcore

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common/covertdtls"
)

func TestWebRTCTuningZeroSetsNothing(t *testing.T) {
	var se webrtc.SettingEngine
	if (WebRTCTuning{}).apply(&se) {
		t.Fatal("the zero tuning must leave pion's defaults alone")
	}
}

func allTuning() WebRTCTuning {
	return WebRTCTuning{
		ICEMaxBindingRequests:  12,
		STUNGatherTimeout:      2 * time.Second,
		ICEDisconnectedTimeout: 3 * time.Second,
		SrflxAcceptanceMinWait: time.Millisecond,
		DTLSRetransmitInterval: 200 * time.Millisecond,
		SCTPRTOMax:             3 * time.Second,
		SCTPMaxReceiveBuffer:   4 << 20,
	}
}

func TestWebRTCTuningEachFieldCounts(t *testing.T) {
	for _, tu := range []WebRTCTuning{
		{ICEMaxBindingRequests: 1}, {STUNGatherTimeout: 1}, {ICEDisconnectedTimeout: 1},
		{ICEFailedTimeout: 1}, {ICEKeepaliveInterval: 1}, {SrflxAcceptanceMinWait: 1},
		{DTLSRetransmitInterval: 1}, {SCTPRTOMax: 1}, {SCTPMaxReceiveBuffer: 1},
	} {
		var se webrtc.SettingEngine
		if !tu.apply(&se) {
			t.Errorf("%+v should count as tuned", tu)
		}
	}
}

// Tuned consumer and producer peer connections must still connect to each other, so the
// settings reach a real handshake rather than only a SettingEngine.
func TestTunedPeersConnect(t *testing.T) {
	api, err := newWebRTCAPI(nil, allTuning())
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, allTuning())
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	open := make(chan struct{})
	dc, err := consumer.CreateDataChannel("data", nil)
	if err != nil {
		t.Fatal(err)
	}
	dc.OnOpen(func() { close(open) })

	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(consumer)
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := producer.SetRemoteDescription(*consumer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := producer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered = webrtc.GatheringCompletePromise(producer)
	if err := producer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := consumer.SetRemoteDescription(*producer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-open:
	case <-time.After(15 * time.Second):
		t.Fatal("tuned peers never opened a data channel")
	}
}
