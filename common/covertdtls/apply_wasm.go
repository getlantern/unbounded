//go:build wasm

package covertdtls

import "github.com/pion/webrtc/v4"

// Apply is a no-op on js/wasm. This is not a temporary shim: browser WebRTC
// performs its own DTLS handshake, so a widget running in a browser has no
// ability to randomize or mimic its ClientHello fingerprint no matter what the
// config asks for.
//
// Worth stating plainly, because the native build uses this to evade the DPI
// filtering described in the package doc: **browser widgets do not get that
// protection**, and a config that sets randomize/mimic is silently ineffective
// there. Callers wanting fingerprint control must run a native producer.
//
// Returns nil rather than an error so a shared config carrying a covertdtls mode
// does not fail widget startup over a capability the platform cannot provide.
func Apply(cfg Config, s *webrtc.SettingEngine) error {
	return nil
}
