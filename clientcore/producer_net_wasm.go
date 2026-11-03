//go:build wasm

package clientcore

import (
	"github.com/pion/webrtc/v4"
)

// producerNetOf is empty in the browser, which ignores Net, ICEUDPMux and
// ICEUDPMuxExternalIP, so a browser producer always uses STUN as usual.
func producerNetOf(*WebRTCOptions) producerNet {
	return producerNet{}
}

// applyProducerNet is a no-op in the browser: the WebRTC stack there is the
// browser's, and pion's SettingEngine has no SetNet or UDP mux under js/wasm.
func applyProducerNet(_ *webrtc.SettingEngine, _ producerNet) (bool, error) {
	return false, nil
}
