//go:build !wasm

package clientcore

import (
	"time"

	"github.com/pion/webrtc/v4"
)

// sctpRTT is the peer connection's smoothed SCTP round trip, or zero if pion hasn't measured one.
func sctpRTT(pc *webrtc.PeerConnection) time.Duration {
	for _, s := range pc.GetStats() {
		if st, ok := s.(webrtc.SCTPTransportStats); ok && st.SmoothedRoundTripTime > 0 {
			return time.Duration(st.SmoothedRoundTripTime * float64(time.Second))
		}
	}
	return 0
}
