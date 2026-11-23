package common

import "testing"

func TestNormalizePreflightFailure(t *testing.T) {
	for in, want := range map[string]string{
		PreflightNoReply:   PreflightNoReply,
		PreflightNoSocket:  PreflightNoSocket,
		PreflightNoServers: PreflightNoServers,
		PreflightOK:        "invalid", // a pass isn't a failure
		"":                 "unreported",
		"bogus":            "invalid",
	} {
		if got := NormalizePreflightFailure(in); got != want {
			t.Errorf("NormalizePreflightFailure(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeVPNState(t *testing.T) {
	for in, want := range map[string]string{
		"connected":    "connected",
		"disconnected": "disconnected",
		"restarting":   "restarting",
		"":             "unknown",
		"Connected":    "invalid",
		"bogus":        "invalid",
	} {
		if got := NormalizeVPNState(in); got != want {
			t.Errorf("NormalizeVPNState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePreflightFailures(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "300": 300, "10000": 10000} {
		if got, ok := ParsePreflightFailures(in); !ok || got != want {
			t.Errorf("ParsePreflightFailures(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "10001", "01", "1.5", "abc"} {
		if _, ok := ParsePreflightFailures(in); ok {
			t.Errorf("ParsePreflightFailures(%q) should be rejected", in)
		}
	}
}
