//go:build wasm

package clientcore

import (
	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"
)

// applyProducerNet is a no-op in the browser: the WebRTC stack there is the
// browser's, and pion's SettingEngine has no SetNet under js/wasm.
func applyProducerNet(_ *webrtc.SettingEngine, _ transport.Net) bool {
	return false
}
