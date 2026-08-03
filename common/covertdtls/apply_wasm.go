//go:build wasm

package covertdtls

import (
	"errors"

	"github.com/pion/webrtc/v4"
)

// Apply is a no-op on js/wasm. This is not a temporary shim: browser WebRTC
// performs its own DTLS handshake, so a widget running in a browser has no
// ability to randomize or mimic its ClientHello fingerprint no matter what the
// config asks for. See the package doc for what that means for coverage.
//
// Returns nil for every configured mode rather than an error, so a shared config
// carrying a covertdtls mode does not fail widget startup over a capability the
// platform cannot provide.
//
// The nil-SettingEngine check is kept identical to the native implementation on
// purpose. A nil engine is a caller bug rather than a platform limitation, and
// validation that fires under only one build tag means such a bug is caught on
// native and passes silently in the widget. That divergence costs more than the
// three lines it takes to avoid.
func Apply(cfg Config, s *webrtc.SettingEngine) error {
	if s == nil {
		return errors.New("covertdtls: nil SettingEngine")
	}
	return nil
}
