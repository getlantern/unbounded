//go:build wasm

package clientcore

import "github.com/pion/webrtc/v4"

// apply is a no-op in the browser, whose own WebRTC stack can't be tuned.
func (t WebRTCTuning) apply(_ *webrtc.SettingEngine) bool { return false }
