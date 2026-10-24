//go:build wasm

package clientcore

import "github.com/pion/webrtc/v4"

// applyICETiming is a no-op in the browser, whose own ICE agent pion's wasm SettingEngine can't
// tune.
func applyICETiming(_ *webrtc.SettingEngine, _ *WebRTCOptions) bool {
	return false
}
