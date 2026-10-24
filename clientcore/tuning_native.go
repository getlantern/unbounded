//go:build !wasm

package clientcore

import "github.com/pion/webrtc/v4"

// apply sets t's non-zero fields on se and reports whether it set any.
func (t WebRTCTuning) apply(se *webrtc.SettingEngine) bool {
	set := false
	if t.ICEMaxBindingRequests > 0 {
		se.SetICEMaxBindingRequests(t.ICEMaxBindingRequests)
		set = true
	}
	if t.STUNGatherTimeout > 0 {
		se.SetSTUNGatherTimeout(t.STUNGatherTimeout)
		set = true
	}
	if t.ICEDisconnectedTimeout > 0 || t.ICEFailedTimeout > 0 || t.ICEKeepaliveInterval > 0 {
		se.SetICETimeouts(
			orDefault(t.ICEDisconnectedTimeout, pionICEDisconnectedTimeout),
			orDefault(t.ICEFailedTimeout, pionICEFailedTimeout),
			orDefault(t.ICEKeepaliveInterval, pionICEKeepaliveInterval),
		)
		set = true
	}
	if t.SrflxAcceptanceMinWait > 0 {
		se.SetSrflxAcceptanceMinWait(t.SrflxAcceptanceMinWait)
		set = true
	}
	if t.DTLSRetransmitInterval > 0 {
		se.SetDTLSRetransmissionInterval(t.DTLSRetransmitInterval)
		set = true
	}
	if t.SCTPRTOMax > 0 {
		se.SetSCTPRTOMax(t.SCTPRTOMax)
		set = true
	}
	if t.SCTPMaxReceiveBuffer > 0 {
		se.SetSCTPMaxReceiveBufferSize(t.SCTPMaxReceiveBuffer)
		set = true
	}
	return set
}
