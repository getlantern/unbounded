package clientcore

import (
	"context"

	"github.com/pion/webrtc/v4"
)

// awaitReflexive reads a gather's candidates, where nil marks the end of gathering as pion reports
// it, and passes on the first server reflexive, peer reflexive or relay candidate. A gather that
// ends, or runs past ctx, without one means no STUN server answered. Other types don't count: a
// browser can end gathering with an empty candidate rather than null, which pion reports as one of
// type Unknown.
func awaitReflexive(ctx context.Context, cands <-chan *webrtc.ICECandidate) string {
	for {
		select {
		case c := <-cands:
			if c == nil {
				return STUNPreflightNoReply
			}
			switch c.Typ {
			case webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypePrflx, webrtc.ICECandidateTypeRelay:
				return STUNPreflightOK
			}
		case <-ctx.Done():
			return STUNPreflightNoReply
		}
	}
}
