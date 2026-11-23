package clientcore

import (
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// gather feeds cands to awaitReflexive the way the browser's onicecandidate does, one at a time.
func gather(t *testing.T, timeout time.Duration, cands ...*webrtc.ICECandidate) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ch := make(chan *webrtc.ICECandidate, len(cands))
	for _, c := range cands {
		ch <- c
	}
	return awaitReflexive(ctx, ch)
}

func cand(typ webrtc.ICECandidateType) *webrtc.ICECandidate {
	return &webrtc.ICECandidate{Typ: typ}
}

// The browser volunteers' STUN check: a pass needs a candidate a peer outside the LAN could reach.
func TestAwaitReflexive(t *testing.T) {
	host, srflx := cand(webrtc.ICECandidateTypeHost), cand(webrtc.ICECandidateTypeSrflx)
	for _, tc := range []struct {
		name  string
		cands []*webrtc.ICECandidate
		want  string
	}{
		{"server reflexive after host", []*webrtc.ICECandidate{host, host, srflx}, STUNPreflightOK},
		{"relay", []*webrtc.ICECandidate{host, cand(webrtc.ICECandidateTypeRelay)}, STUNPreflightOK},
		{"gathering ends with host only", []*webrtc.ICECandidate{host, host, nil}, STUNPreflightNoReply},
		{"gathering ends with nothing", []*webrtc.ICECandidate{nil}, STUNPreflightNoReply},
		// A browser's end of gathering can be an empty candidate rather than null.
		{"empty end-of-candidates", []*webrtc.ICECandidate{host, cand(webrtc.ICECandidateTypeUnknown), nil}, STUNPreflightNoReply},
		{"peer reflexive", []*webrtc.ICECandidate{host, cand(webrtc.ICECandidateTypePrflx)}, STUNPreflightOK},
		// A browser that blocks non-proxied UDP gathers no candidates and may never end gathering.
		{"times out on host only", []*webrtc.ICECandidate{host}, STUNPreflightNoReply},
		{"times out on nothing", nil, STUNPreflightNoReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gather(t, 50*time.Millisecond, tc.cands...); got != tc.want {
				t.Errorf("awaitReflexive() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A preflight passes as soon as a server answers, without waiting out the gather or the timeout.
func TestAwaitReflexivePassesOnFirstAnswer(t *testing.T) {
	start := time.Now()
	if got := gather(t, time.Minute, cand(webrtc.ICECandidateTypeSrflx)); got != STUNPreflightOK {
		t.Fatalf("awaitReflexive() = %q, want %q", got, STUNPreflightOK)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v with an answer already in", d)
	}
}
