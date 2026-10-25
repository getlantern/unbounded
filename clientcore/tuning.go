package clientcore

import "time"

// WebRTCTuning overrides pion's transport defaults on native consumers and producers, so they can
// be tuned from server config rather than by release. A zero field keeps pion's default, so the
// zero value changes nothing. A browser's WebRTC stack can't be tuned, so the widget ignores it.
type WebRTCTuning struct {
	// ICEMaxBindingRequests caps the connectivity checks sent on a candidate pair before it is
	// marked failed. pion's default is 7.
	ICEMaxBindingRequests uint16
	// STUNGatherTimeout bounds how long gathering waits on each STUN server. pion's default is 5s,
	// so one unresponsive server in a batch delays every handshake by that much.
	STUNGatherTimeout time.Duration
	// ICEDisconnectedTimeout, ICEFailedTimeout and ICEKeepaliveInterval are pion's ICE liveness
	// timers: 5s, 25s and 2s by default. Setting any one sets all three, the rest at their defaults.
	ICEDisconnectedTimeout time.Duration
	ICEFailedTimeout       time.Duration
	ICEKeepaliveInterval   time.Duration
	// SrflxAcceptanceMinWait is how long the controlling agent waits before nominating a server
	// reflexive pair, which is the usual pair between a consumer and a volunteer. pion's default is
	// 500ms; zero can't express "don't wait", so use 1ms for that.
	SrflxAcceptanceMinWait time.Duration
	// DTLSRetransmitInterval is the initial DTLS handshake retransmission interval. pion's default
	// is 1s, doubling after each loss, so a few lost handshake packets use up most of
	// NATFailTimeout on a lossy path.
	DTLSRetransmitInterval time.Duration
	// SCTPRTOMax caps SCTP's retransmission timeout, which backs off on loss. pion's default is
	// 60s, long enough to stall a data channel after a burst of loss.
	SCTPRTOMax time.Duration
	// SCTPMaxReceiveBuffer is the SCTP receive buffer in bytes, which bounds a reliable channel's
	// window. pion's default is 1MiB.
	SCTPMaxReceiveBuffer uint32
}

// pion's ICE liveness defaults, for filling in the timers SetICETimeouts needs together.
const (
	pionICEDisconnectedTimeout = 5 * time.Second
	pionICEFailedTimeout       = 25 * time.Second
	pionICEKeepaliveInterval   = 2 * time.Second
)

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
