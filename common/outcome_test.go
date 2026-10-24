package common

import "testing"

func TestNormalizeProducerOutcome(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		OutcomeConnected:    OutcomeConnected,
		OutcomeNATTimeout:   OutcomeNATTimeout,
		"something_made_up": "invalid",
		"NAT_TIMEOUT":       "invalid",
	} {
		if got := NormalizeProducerOutcome(in); got != want {
			t.Errorf("NormalizeProducerOutcome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeStates(t *testing.T) {
	if got := NormalizeICEState("checking"); got != "checking" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeICEState("connecting"); got != "invalid" {
		t.Errorf("connecting is a peer connection state, not an ICE state; got %q", got)
	}
	if got := NormalizePCState("connecting"); got != "connecting" {
		t.Errorf("got %q", got)
	}
	if got := NormalizePCState("<script>"); got != "invalid" {
		t.Errorf("got %q", got)
	}
}
