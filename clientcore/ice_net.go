//go:build !wasm

package clientcore

import (
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

func iceNetOf(options *WebRTCOptions) iceNet {
	return iceNet{net: options.Net, mux: options.ICEUDPMux, externalIP: options.ICEUDPMuxExternalIP}
}

// applyICENet gives a peer connection's SettingEngine the caller's
// transport, UDP mux and the mux's external address, and reports whether it
// set any, which it always does: the transport, the caller's or else the
// host's network stack as pion would use by default, is wrapped so a TCP dial
// to a TURN server gives up (see turnDialTimeout). A zero iceNet otherwise
// keeps pion's defaults, which is what every release before these options used.
func applyICENet(se *webrtc.SettingEngine, pn iceNet) (bool, error) {
	n := pn.net
	if n == nil {
		var err error
		if n, err = stdnet.NewNet(); err != nil {
			return false, err
		}
	}
	se.SetNet(boundedDialNet{n})
	if pn.mux == nil {
		return true, nil
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
