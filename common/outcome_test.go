package common

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
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

func TestParseAttemptDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0":      0,
		"15250":  15250 * time.Millisecond,
		"180000": 3 * time.Minute,
	} {
		if got, ok := ParseAttemptDuration(in); !ok || got != want {
			t.Errorf("ParseAttemptDuration(%q) = %v, %v, want %v, true", in, got, ok, want)
		}
	}
	// The last two overflow time.Duration if multiplied first, wrapping to 448384ns and below zero.
	for _, in := range []string{"", "-1", "180001", "0100", "1.5", "abc", "+5", "18446744073710", "9223372036855"} {
		if got, ok := ParseAttemptDuration(in); ok {
			t.Errorf("ParseAttemptDuration(%q) = %v, true, want false", in, got)
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

func TestNormalizeConsumerVersion(t *testing.T) {
	const current = "v2.3.40"
	for in, want := range map[string]string{
		"":             "not_sent",
		"v2.3.40":      "v2.3.40",
		"v2.3.23":      "v2.3.23",
		"v2.3.9":       "v2.3.9",
		"v2.3.8":       "older",
		"v2.3.48":      "v2.3.48",
		"v2.3.49":      "newer",
		"v2.2.99":      "older",
		"v2.4.0":       "newer",
		"v2.999999.0":  "newer",
		"v1.3.40":      "invalid",
		"v3.0.0":       "invalid",
		"v2.3":         "invalid",
		"v2.3.40.1":    "invalid",
		"v2.3.040":     "invalid",
		"v2.3.40-rc.1": "invalid",
		"v2.3.+40":     "invalid",
		"2.3.40":       "invalid",
		"v2.3.-1":      "invalid",
		"(devel)":      "invalid",
	} {
		if got := NormalizeConsumerVersion(in, current); got != want {
			t.Errorf("NormalizeConsumerVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeConsumerVersion("v2.3.0", "v2.3.2"); got != "v2.3.0" {
		t.Errorf("a window reaching below patch 0 got %q, want v2.3.0", got)
	}
	if got := NormalizeConsumerVersion("v2.3.0", "bogus"); got != "invalid" {
		t.Errorf("an unparseable current version got %q, want invalid", got)
	}
}

// TestNormalizeConsumerVersionBounded checks the label's value space stays small however the
// header is chosen.
func TestNormalizeConsumerVersionBounded(t *testing.T) {
	seen := map[string]bool{}
	for minor := 0; minor <= 20; minor++ {
		for patch := 0; patch <= 200; patch++ {
			seen[NormalizeConsumerVersion("v2."+strconv.Itoa(minor)+"."+strconv.Itoa(patch), Version)] = true
		}
	}
	if max := consumerVersionPatchesBehind + consumerVersionPatchesAhead + 1 + 2; len(seen) > max {
		t.Fatalf("%d label values, want at most %d", len(seen), max)
	}
}

// TestNormalizeConsumerVersionDoesNotRetainInput checks a label never aliases the header it came
// from, which Freddie caches for up to a day.
func TestNormalizeConsumerVersionDoesNotRetainInput(t *testing.T) {
	in := strings.Clone(Version)
	got := NormalizeConsumerVersion(in, Version)
	if got != Version {
		t.Fatalf("got %q, want %q", got, Version)
	}
	if unsafe.StringData(got) == unsafe.StringData(in) {
		t.Error("the label shares the header's memory")
	}
}

func TestOutcomeConsumerVersion(t *testing.T) {
	for _, tc := range []struct {
		reported bool
		version  string
		want     string
	}{
		{false, "", "unreported"},
		{false, "v2.3.23", "unreported"},
		{true, "", "unknown"},
		{true, "older", "older"},
		{true, "v2.3.23", "v2.3.23"},
	} {
		if got := OutcomeConsumerVersion(tc.reported, tc.version); got != tc.want {
			t.Errorf("OutcomeConsumerVersion(%v, %q) = %q, want %q", tc.reported, tc.version, got, tc.want)
		}
	}
}

func TestOutcomePath(t *testing.T) {
	for in, want := range map[string]string{"relay": "relay", "direct": "direct", "": "unreported", "bogus": "unreported"} {
		if got := OutcomePath(in); got != want {
			t.Errorf("OutcomePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTURNGatherAndPair(t *testing.T) {
	for in, want := range map[string]string{"": "unreported", TURNOK: TURNOK, TURNMissing: TURNMissing, TURNNone: TURNNone, "bogus": "invalid"} {
		if got := NormalizeTURN(in); got != want {
			t.Errorf("NormalizeTURN(%q) = %q, want %q", in, got, want)
		}
	}
	for _, tc := range []struct{ cut, turn, want string }{
		{GatherCut, TURNMissing, "cut"},
		{"", TURNOK, "complete"},
		{"", "", "unreported"},
	} {
		if got := NormalizeGatherCut(tc.cut, tc.turn); got != tc.want {
			t.Errorf("NormalizeGatherCut(%q, %q) = %q, want %q", tc.cut, tc.turn, got, tc.want)
		}
	}
	for in, want := range map[string]string{
		"":                         "unreported",
		"srflx-relay":              "srflx-relay",
		PairTypes("host", "prflx"): "host-prflx",
		"srflx":                    "invalid",
		"srflx-bogus":              "invalid",
		"relay-relay-x":            "invalid",
	} {
		if got := NormalizePair(in); got != want {
			t.Errorf("NormalizePair(%q) = %q, want %q", in, got, want)
		}
	}
}
