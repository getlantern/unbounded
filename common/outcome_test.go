package common

import (
	"strconv"
	"testing"
)

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

func TestNormalizePionDTLS(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "not_sent",
		"v3.1.15":                             "v3.1.15",
		"v3.1.2":                              "v3.1.2",
		"v3.0.0":                              "v3.0.0",
		"v3.3.31":                             "v3.3.31",
		"v3.1.32":                             "v3.1.32+",
		"v3.1.999999":                         "v3.1.32+",
		"v3.4.0":                              "v3.4+",
		"v3.999999.0":                         "v3.4+",
		"v3.1.16-0.20261020120000-abcdef0123": "v3.1.16",
		"v3.2.0-rc.1":                         "v3.2.0",
		PionDTLSBrowser:                       PionDTLSBrowser,
		PionDTLSNoBuildInfo:                   PionDTLSNoBuildInfo,
		"v3.01.2":                             "invalid",
		"v3.1.02":                             "invalid",
		"v3.1.-1":                             "invalid",
		"v3.1.+1":                             "invalid",
		"v3.1":                                "invalid",
		"v3.1.2.3":                            "invalid",
		"v3.1.2-":                             "invalid",
		"v3.1.99999999999999999999":           "invalid",
		"v2.2.7":                              "invalid",
		"3.1.15":                              "invalid",
		"(devel)":                             "invalid",
	} {
		if got := NormalizePionDTLS(in); got != want {
			t.Errorf("NormalizePionDTLS(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizePionDTLSBounded checks the label's value space stays small however the header is
// chosen.
func TestNormalizePionDTLSBounded(t *testing.T) {
	seen := map[string]bool{}
	for minor := 0; minor <= 50; minor++ {
		for patch := 0; patch <= 100; patch++ {
			v := "v3." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
			seen[NormalizePionDTLS(v)] = true
			seen[NormalizePionDTLS(v+"-0.20261020120000-abcdef012345")] = true
		}
	}
	if want := (maxPionDTLSMinor+1)*(maxPionDTLSPatch+2) + 1; len(seen) != want {
		t.Fatalf("%d label values, want %d", len(seen), want)
	}
}

func TestOutcomeConsumerDTLS(t *testing.T) {
	for _, tc := range []struct {
		reported bool
		dtls     string
		want     string
	}{
		{false, "", "unreported"},
		{false, "v3.1.15", "unreported"},
		{true, "", "unknown"},
		{true, "not_sent", "not_sent"},
		{true, "v3.1.15", "v3.1.15"},
	} {
		if got := OutcomeConsumerDTLS(tc.reported, tc.dtls); got != tc.want {
			t.Errorf("OutcomeConsumerDTLS(%v, %q) = %q, want %q", tc.reported, tc.dtls, got, tc.want)
		}
	}
}
