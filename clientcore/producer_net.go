//go:build !wasm

package clientcore

import (
	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"
)

// applyProducerNet gives the producer's SettingEngine the caller's transport
// and reports whether it did. A nil Net keeps pion's default, which is what
// every release before this one used.
func applyProducerNet(se *webrtc.SettingEngine, net transport.Net) bool {
	if net == nil {
		return false
	}
	se.SetNet(net)
	return true
}
