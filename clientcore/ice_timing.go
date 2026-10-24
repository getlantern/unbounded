//go:build !wasm

package clientcore

import "github.com/pion/webrtc/v4"

// applyICETiming sets the ICE timing knobs from options on se, and reports whether it set any.
// A zero option keeps pion's default, so a client whose config carries none of them behaves
// exactly as before.
func applyICETiming(se *webrtc.SettingEngine, options *WebRTCOptions) bool {
	if options == nil {
		return false
	}
	set := false
	if options.ICEMaxBindingRequests > 0 {
		se.SetICEMaxBindingRequests(options.ICEMaxBindingRequests)
		set = true
	}
	if options.STUNGatherTimeout > 0 {
		se.SetSTUNGatherTimeout(options.STUNGatherTimeout)
		set = true
	}
	return set
}
