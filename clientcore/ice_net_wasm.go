//go:build wasm

package clientcore

import (
	"github.com/pion/webrtc/v4"
)

// iceNetOf is empty in the browser, which ignores Net, ICEUDPMux and
// ICEUDPMuxExternalIP, so a browser peer always uses STUN as usual.
func iceNetOf(*WebRTCOptions) iceNet {
	return iceNet{}
}

// applyICENet is a no-op in the browser: the WebRTC stack there is the
// browser's, and pion's SettingEngine has no SetNet or UDP mux under js/wasm.
func applyICENet(_ *webrtc.SettingEngine, _ iceNet) (bool, error) {
	return false, nil
}
