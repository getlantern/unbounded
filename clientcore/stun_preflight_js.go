//go:build wasm

package clientcore

import (
	"context"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"
)

// stunPreflight gathers ICE candidates on a throwaway peer connection using servers, and passes as
// soon as the browser reports one that isn't a host candidate. A page can't open a UDP socket of
// its own, so the browser's ICE agent sends the requests: the same agent, with the same privacy
// settings and proxy, that the volunteer's real attempt will use.
func stunPreflight(ctx context.Context, _ transport.Net, servers []string, timeout time.Duration) STUNPreflightResult {
	start := time.Now()
	res := STUNPreflightResult{Servers: len(servers)}
	if len(servers) == 0 {
		res.Outcome = STUNPreflightNoServers
		return res
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: servers}}})
	if err != nil {
		res.Outcome = STUNPreflightNoSocket
		return res
	}
	defer pc.Close()

	// Buffered, and never blocking, so the browser's callbacks can't stall on a preflight that has
	// already returned.
	cands := make(chan *webrtc.ICECandidate, 64)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		select {
		case cands <- c:
		default:
		}
	})
	// The browser gathers only once an offer has something to negotiate.
	if _, err := pc.CreateDataChannel("preflight", nil); err != nil {
		res.Outcome = STUNPreflightNoSocket
		return res
	}
	offer, err := pc.CreateOffer(nil)
	if err == nil {
		err = pc.SetLocalDescription(offer)
	}
	if err != nil {
		res.Outcome = STUNPreflightNoSocket
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res.Outcome = awaitReflexive(ctx, cands)
	res.Elapsed = time.Since(start)
	return res
}
