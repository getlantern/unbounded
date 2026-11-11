package clientcore

import (
	"context"

	"github.com/pion/webrtc/v4"
)

// awaitReflexive reads a gather's candidates, where nil marks the end of gathering as pion reports
// it, and passes on the first candidate that isn't a host candidate: the same test a producer
// applies after taking an offer (see hasNonHostCandidate). A gather that ends, or runs past ctx,
// with only host candidates means no STUN server answered.
func awaitReflexive(ctx context.Context, cands <-chan *webrtc.ICECandidate) string {
	for {
		select {
		case c := <-cands:
			if c == nil {
				return STUNPreflightNoReply
			}
			if c.Typ != webrtc.ICECandidateTypeHost {
				return STUNPreflightOK
			}
		case <-ctx.Done():
			return STUNPreflightNoReply
		}
	}
}
