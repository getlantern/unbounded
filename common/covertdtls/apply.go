//go:build !wasm

// Apply lives in its own build-tagged file because the pion SettingEngine hooks
// it installs (SetSRTPProtectionProfiles, SetDTLSClientHelloMessageHook) do not
// exist on js/wasm: in a browser the DTLS handshake belongs to the browser's own
// WebRTC stack, not to pion, so there is no ClientHello for us to shape.
//
// Before this split the package compiled unconditionally and broke the widget's
// wasm build outright. That went unnoticed because no CI job builds the wasm
// widget and ui/public/widget.wasm is a committed prebuilt binary, so nothing
// ever exercised the source path.

package covertdtls

import (
	"errors"

	"github.com/pion/webrtc/v4"
	"github.com/theodorsm/covert-dtls/pkg/mimicry"
	"github.com/theodorsm/covert-dtls/pkg/randomize"
	"github.com/theodorsm/covert-dtls/pkg/utils"
)

// Apply installs the configured ClientHello hook on the given SettingEngine.
// Returns nil if the config has no effect (Enabled() == false).
func Apply(cfg Config, s *webrtc.SettingEngine) error {
	if s == nil {
		return errors.New("covertdtls: nil SettingEngine")
	}
	switch {
	case cfg.Fingerprint != "":
		mimic := &mimicry.MimickedClientHello{}
		if err := mimic.LoadFingerprint(cfg.Fingerprint); err != nil {
			return err
		}
		s.SetSRTPProtectionProfiles(utils.DefaultSRTPProtectionProfiles()...)
		s.SetDTLSClientHelloMessageHook(mimic.Hook)
	case cfg.Mimic:
		mimic := &mimicry.MimickedClientHello{}
		if cfg.Randomize {
			if err := mimic.LoadRandomFingerprint(); err != nil {
				return err
			}
		}
		s.SetSRTPProtectionProfiles(utils.DefaultSRTPProtectionProfiles()...)
		s.SetDTLSClientHelloMessageHook(mimic.Hook)
	case cfg.Randomize:
		rand := randomize.RandomizedMessageClientHello{RandomALPN: true}
		s.SetDTLSClientHelloMessageHook(rand.Hook)
	}
	return nil
}
