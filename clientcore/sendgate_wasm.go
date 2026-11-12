//go:build wasm

package clientcore

import (
	"time"

	"github.com/pion/webrtc/v4"
)

// sctpRTT is zero in the browser, whose WebRTC stack doesn't report SCTP's round trip; the send
// gate falls back to gateFallbackRTT.
func sctpRTT(*webrtc.PeerConnection) time.Duration { return 0 }
