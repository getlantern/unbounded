//go:build !wasm

package clientcore

import (
	"github.com/pion/webrtc/v4"
)

// applyProducerNet gives the producer's SettingEngine the caller's transport,
// UDP mux and the mux's external address, and reports whether it set any. A
// zero producerNet keeps pion's defaults, which is what every release before
// these options used.
func applyProducerNet(se *webrtc.SettingEngine, pn producerNet) (bool, error) {
	set := false
	if pn.net != nil {
		se.SetNet(pn.net)
		set = true
	}
	if pn.mux == nil {
		return set, nil
	}
	se.SetICEUDPMux(pn.mux)
	if pn.externalIP != "" {
		// A host candidate rather than a server-reflexive one: pion gathers
		// rewritten srflx candidates on fresh sockets, off the mux and so off
		// the mapped port. Appended, so LAN peers keep the local address.
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{pn.externalIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode:            webrtc.ICEAddressRewriteAppend,
			Networks:        []webrtc.NetworkType{webrtc.NetworkTypeUDP4},
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}
