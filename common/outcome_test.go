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

func TestNormalizeNATTimeout(t *testing.T) {
	for in, want := range map[string]string{
		"":    "unreported",
		"5":   "5",
		"15":  "15",
		"60":  "60",
		"0":   "invalid",
		"61":  "invalid",
		"-5":  "invalid",
		"05":  "invalid",
		"1.5": "invalid",
		"abc": "invalid",
	} {
		if got := NormalizeNATTimeout(in); got != want {
			t.Errorf("NormalizeNATTimeout(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutcomeConsumerCountry(t *testing.T) {
	for _, tc := range []struct {
		reported bool
		country  string
		want     string
	}{
		{false, "", "unreported"},
		{false, "RU", "unreported"},
		{true, "", "unknown"},
		{true, "RU", "RU"},
		{true, "CN", "CN"},
		{true, "IR", "IR"},
		{true, "US", "US"},
		{true, "DE", "other"},
	} {
		if got := OutcomeConsumerCountry(tc.reported, tc.country); got != tc.want {
			t.Errorf("OutcomeConsumerCountry(%v, %q) = %q, want %q", tc.reported, tc.country, got, tc.want)
		}
	}
}
