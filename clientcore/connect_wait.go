package clientcore

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// awaitDataChannel waits for established to deliver the opened data channel, giving up natFail
// after it starts. If ICE connects first, it waits at least grace from that moment instead: DTLS
// and the data channel's SCTP setup still have to run over the new path, and a deadline that ICE
// nearly used up would cut them off. ok is false when it gave up or ctx ended.
func awaitDataChannel(
	ctx context.Context,
	pc *webrtc.PeerConnection,
	established <-chan *webrtc.DataChannel,
	natFail, grace time.Duration,
) (d *webrtc.DataChannel, ok bool) {
	start := time.Now()

	// Register before reading the current state, so a change in between is still seen.
	iceConnected := make(chan struct{})
	var once sync.Once
	signal := func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected || s == webrtc.ICEConnectionStateCompleted {
			once.Do(func() { close(iceConnected) })
		}
	}
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		slog.Debug("ICE connection state change", "state", s.String())
		signal(s)
	})
	signal(pc.ICEConnectionState())

	deadline := time.NewTimer(natFail)
	defer deadline.Stop()
	for {
		select {
		case d := <-established:
			return d, true
		case <-ctx.Done():
			return nil, false
		case <-iceConnected:
			iceConnected = nil
			if natFail-time.Since(start) < grace {
				deadline.Reset(grace)
			}
		case <-deadline.C:
			return nil, false
		}
	}
}
