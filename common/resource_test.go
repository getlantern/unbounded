package common

import (
	"encoding/json"
	"slices"

	"github.com/pion/webrtc/v4"
	"reflect"
	"strings"
	"testing"
)

// The 3-element form is what every release before the consumer-country field
// emits, so a new egress must keep parsing it. This is the compatibility
// direction that actually matters in production: donors (browser widgets) are
// upgraded independently of, and typically later than, the egress fleet.
func TestParseSubprotocolsRequest_ThreeElementFormStillParses(t *testing.T) {
	in := NewSubprotocolsRequest("csid-abc", "v2.3.1")
	if len(in) != 3 {
		t.Fatalf("NewSubprotocolsRequest must stay 3 elements on the wire, got %d: %v", len(in), in)
	}

	csid, version, ok := ParseSubprotocolsRequest(in)
	if !ok {
		t.Fatal("ParseSubprotocolsRequest rejected the legacy 3-element form")
	}
	if csid != "csid-abc" || version != "v2.3.1" {
		t.Fatalf("got csid=%q version=%q", csid, version)
	}

	csid, version, country, ok := ParseSubprotocolsRequestWithCountry(in)
	if !ok || csid != "csid-abc" || version != "v2.3.1" {
		t.Fatalf("got csid=%q version=%q ok=%v", csid, version, ok)
	}
	if country != "" {
		t.Fatalf("legacy form must yield an empty country, got %q", country)
	}
}

func TestParseSubprotocolsRequest_FourElementFormCarriesCountry(t *testing.T) {
	in := NewSubprotocolsRequestWithCountry("csid-xyz", "v2.3.1", "CN")
	if len(in) != 4 {
		t.Fatalf("expected 4 elements, got %d: %v", len(in), in)
	}

	csid, version, country, ok := ParseSubprotocolsRequestWithCountry(in)
	if !ok {
		t.Fatal("ParseSubprotocolsRequestWithCountry rejected the 4-element form")
	}
	if csid != "csid-xyz" || version != "v2.3.1" || country != "CN" {
		t.Fatalf("got csid=%q version=%q country=%q", csid, version, country)
	}

	// The 3-return shim must tolerate the longer form too, so callers that don't
	// care about country keep working against new donors.
	csid, version, ok = ParseSubprotocolsRequest(in)
	if !ok || csid != "csid-xyz" || version != "v2.3.1" {
		t.Fatalf("3-return shim mishandled the 4-element form: csid=%q version=%q ok=%v", csid, version, ok)
	}
}

// An empty country must produce a byte-identical request to the legacy
// constructor. Otherwise "decline to disclose a country" would still be
// distinguishable on the wire from "an old client", which defeats the point.
func TestNewSubprotocolsRequestWithCountry_EmptyCountryIsWireIdentical(t *testing.T) {
	legacy := NewSubprotocolsRequest("csid-1", "v2.3.1")
	withEmpty := NewSubprotocolsRequestWithCountry("csid-1", "v2.3.1", "")
	if !reflect.DeepEqual(legacy, withEmpty) {
		t.Fatalf("empty country changed the wire form:\n legacy=%v\n  empty=%v", legacy, withEmpty)
	}
}

// A correctly-sized list bearing the wrong cookie must be rejected here rather
// than passed through to fail later inside websocket.Accept with a vaguer error.
func TestParseSubprotocolsRequest_RejectsWrongMagicCookie(t *testing.T) {
	for _, in := range [][]string{
		{"not-the-cookie", "csid", "v2.3.1"},
		{"not-the-cookie", "csid", "v2.3.1", "CN"},
		{"", "csid", "v2.3.1"},
	} {
		if _, _, ok := ParseSubprotocolsRequest(in); ok {
			t.Errorf("ParseSubprotocolsRequest(%v) = ok, want rejected on cookie mismatch", in)
		}
		if _, _, _, ok := ParseSubprotocolsRequestWithCountry(in); ok {
			t.Errorf("ParseSubprotocolsRequestWithCountry(%v) = ok, want rejected on cookie mismatch", in)
		}
	}

	// And the constructors' own output must still be accepted.
	if _, _, ok := ParseSubprotocolsRequest(NewSubprotocolsRequest("csid", "v2.3.1")); !ok {
		t.Error("rejected our own 3-element request")
	}
	if _, _, _, ok := ParseSubprotocolsRequestWithCountry(NewSubprotocolsRequestWithCountry("csid", "v2.3.1", "CN")); !ok {
		t.Error("rejected our own 4-element request")
	}
}

// The country element is client-controlled and flows into span attributes, so an
// unbounded value would be a cardinality/payload attack on the tracing backend
// rather than just bad data. Anything that isn't a 2-letter ASCII code must come
// back as "not supplied" rather than truncated, since a truncated garbage value
// is indistinguishable from a real code.
func TestParseSubprotocolsRequestWithCountry_BoundsUntrustedCountry(t *testing.T) {
	cookie := NewSubprotocolsRequest("csid", "v2.3.1")[0]

	for _, tc := range []struct{ in, want string }{
		{"CN", "CN"},
		{"cn", "CN"},                    // normalized
		{"Cn", "CN"},                    // normalized
		{"", ""},                        // absent
		{"C", ""},                       // too short
		{"CHN", ""},                     // too long
		{"C1", ""},                      // digit
		{"C-", ""},                      // punctuation
		{"日本", ""},                      // multibyte: 2 runes but 6 bytes
		{strings.Repeat("A", 4096), ""}, // payload amplification attempt
		{"cn\nX-Injected: 1", ""},       // header-injection shaped
	} {
		_, _, got, ok := ParseSubprotocolsRequestWithCountry([]string{cookie, "csid", "v2.3.1", tc.in})
		if !ok {
			t.Errorf("country %q: parse failed outright; the request should still be accepted with no country", tc.in)
			continue
		}
		if got != tc.want {
			t.Errorf("country %q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseSubprotocolsRequest_RejectsWrongArity(t *testing.T) {
	for _, in := range [][]string{
		nil,
		{},
		{"magic"},
		{"magic", "csid"},
		{"magic", "csid", "version", "CN", "extra"},
	} {
		if _, _, ok := ParseSubprotocolsRequest(in); ok {
			t.Errorf("ParseSubprotocolsRequest(%v) = ok, want rejected", in)
		}
		if _, _, _, ok := ParseSubprotocolsRequestWithCountry(in); ok {
			t.Errorf("ParseSubprotocolsRequestWithCountry(%v) = ok, want rejected", in)
		}
	}
}

// SubprotocolsContainMagicCookie answers "is this peer speaking our protocol at all",
// independent of whether it got the rest right. The egress uses it to decide both
// which refusal to report and what is safe to log, so both halves matter.
func TestSubprotocolsContainMagicCookie(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []string
		want bool
	}{
		"nil":                     {nil, false},
		"empty":                   {[]string{}, false},
		"valid 3-element request": {NewSubprotocolsRequest("csid", "v2.3.5"), true},
		"valid 4-element request": {NewSubprotocolsRequestWithCountry("csid", "v2.3.5", "CN"), true},
		// The server's own response format. Exactly one element, which is the shape
		// the live refusals have, so this case is load-bearing rather than academic.
		"response form (cookie alone)": {NewSubprotocolsResponse(), true},
		"foreign single token":         {[]string{"chat"}, false},
		"foreign multi token":          {[]string{"graphql-ws", "mqtt"}, false},
		// Position deliberately does NOT matter here, unlike in the parser. This
		// question is "might a session ID be in there", and a misordered list from a
		// skewed client carries one just the same. Answering it positionally leaked
		// exactly that value.
		"cookie not first": {[]string{"csid", NewSubprotocolsResponse()[0]}, true},
		"cookie last":      {[]string{"a", "b", NewSubprotocolsResponse()[0]}, true},
		"empty first":      {[]string{"", NewSubprotocolsResponse()[0]}, true},
		// Case-sensitive: the cookie is a byte-for-byte constant, not a token to
		// normalize.
		"wrong case": {[]string{"UN80UND3D", "csid", "v2.3.5"}, false},
	} {
		if got := SubprotocolsContainMagicCookie(tc.in); got != tc.want {
			t.Errorf("%s: SubprotocolsContainMagicCookie(%q) = %v, want %v", name, tc.in, got, tc.want)
		}
	}
}

// Anything the parser accepts must also be recognized as our protocol. If these
// disagreed, a refusal could be labeled "foreign" for a peer that in fact speaks
// this protocol correctly — which is the exact misattribution the label exists to
// prevent. Note the converse does not hold, on purpose: this check is deliberately
// broader than the parser, because "might contain a session ID" must not be a
// narrower question than "parses".
func TestSubprotocolsContainMagicCookie_AgreesWithParser(t *testing.T) {
	for _, in := range [][]string{
		NewSubprotocolsRequest("csid", "v2.3.5"),
		NewSubprotocolsRequestWithCountry("csid", "v2.3.5", "CN"),
		NewSubprotocolsRequestWithCountry("csid", "v2.3.5", ""),
	} {
		if _, _, _, ok := ParseSubprotocolsRequestWithCountry(in); !ok {
			t.Fatalf("precondition: parser rejected %q", in)
		}
		if !SubprotocolsContainMagicCookie(in) {
			t.Errorf("parser accepted %q but cookie check rejected it", in)
		}
	}
}

// The emit side normalizes with the same function the parse side applies, so the
// wire never carries a value the receiver is guaranteed to discard.
func TestNewSubprotocolsRequestWithCountry_Normalizes(t *testing.T) {
	for name, tc := range map[string]struct {
		in      string
		wantLen int
		wantCC  string
	}{
		"already canonical": {"CN", 4, "CN"},
		"lowercase":         {"cn", 4, "CN"},
		"mixed case":        {"Ir", 4, "IR"},
		"empty declines":    {"", 3, ""},
		"too long":          {"USA", 3, ""},
		"too short":         {"U", 3, ""},
		"not letters":       {"1!", 3, ""},
		"whitespace":        {"  ", 3, ""},
	} {
		got := NewSubprotocolsRequestWithCountry("csid", "v2.3.7", tc.in)
		if len(got) != tc.wantLen {
			t.Errorf("%s: NewSubprotocolsRequestWithCountry(%q) produced %d elements, want %d (%v)",
				name, tc.in, len(got), tc.wantLen, got)
			continue
		}
		// Whatever we emit must round-trip through the parser to the same value.
		_, _, cc, ok := ParseSubprotocolsRequestWithCountry(got)
		if !ok {
			t.Errorf("%s: own output failed to parse: %v", name, got)
			continue
		}
		if cc != tc.wantCC {
			t.Errorf("%s: round-tripped country = %q, want %q", name, cc, tc.wantCC)
		}
	}
}

// A consumer that declines must be byte-identical on the wire to one that predates
// the field, or "declining" would itself be a distinguishing signal.
func TestNewSubprotocolsRequestWithCountry_DeclineIsIndistinguishable(t *testing.T) {
	plain := NewSubprotocolsRequest("csid", "v2.3.7")
	for _, decline := range []string{"", "USA", "1!", " "} {
		got := NewSubprotocolsRequestWithCountry("csid", "v2.3.7", decline)
		if !slices.Equal(got, plain) {
			t.Errorf("country %q produced %v, want the 3-element form %v", decline, got, plain)
		}
	}
}

// OfferMsg and ConsumerInfo gained a field, and the wire format is JSON, so both
// directions of skew must be non-breaking: an old peer omits it, a new peer adds it.
func TestOfferMsgCountry_WireCompatibleBothWays(t *testing.T) {
	// Old consumer -> new producer: field absent, reads as "".
	var fromOld OfferMsg
	if err := json.Unmarshal([]byte(`{"SDP":{"type":"offer","sdp":"x"},"Tag":"t"}`), &fromOld); err != nil {
		t.Fatalf("new producer cannot decode an old consumer's offer: %v", err)
	}
	if fromOld.Country != "" {
		t.Errorf("absent Country decoded as %q, want empty", fromOld.Country)
	}
	if fromOld.Tag != "t" {
		t.Errorf("Tag = %q, want t — adding a field must not disturb existing ones", fromOld.Tag)
	}

	// New consumer -> old producer: the extra field is ignored rather than fatal.
	// oldOfferMsg is what OfferMsg looked like before Country existed.
	type oldOfferMsg struct {
		SDP webrtc.SessionDescription
		Tag string
	}
	// A real SDP type: webrtc.SessionDescription has a custom unmarshaller that
	// rejects the zero value as "unknown", so a bare OfferMsg{} would fail the round
	// trip for reasons unrelated to the new field.
	raw, err := json.Marshal(OfferMsg{
		SDP:     webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "x"},
		Tag:     "t",
		Country: "CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	var toOld oldOfferMsg
	if err := json.Unmarshal(raw, &toOld); err != nil {
		t.Fatalf("an old producer cannot decode a new consumer's offer: %v", err)
	}
	if toOld.Tag != "t" {
		t.Errorf("old decode lost Tag: %q", toOld.Tag)
	}
}

// Nil() must ignore Country. jit_egress_consumer keys "has a consumer claimed this
// slot" on it, so a ConsumerInfo carrying only a country must still read as empty —
// otherwise the slot would wedge waiting for a session that does not exist.
func TestConsumerInfoNil_IgnoresCountry(t *testing.T) {
	if !(ConsumerInfo{Country: "CN"}).Nil() {
		t.Error("a ConsumerInfo with only a Country must be Nil()")
	}
	if (ConsumerInfo{SessionID: "csid", Country: "CN"}).Nil() {
		t.Error("a ConsumerInfo with a SessionID must not be Nil()")
	}
}

// Codes meaning "no country" must decline rather than become a label. The egress
// records unresolvable traffic as the literal "unknown", so accepting ZZ would split
// one concept across two labels.
func TestNormalizeCountry_DeclinesNonCountryCodes(t *testing.T) {
	for _, in := range []string{"ZZ", "zz", "Zz", "AA", "aa"} {
		if got := normalizeCountry(in); got != "" {
			t.Errorf("normalizeCountry(%q) = %q, want \"\" — user-assigned codes are not countries", in, got)
		}
		// And the emitted form must be the 3-element one, indistinguishable from a
		// consumer that declined outright.
		if got := NewSubprotocolsRequestWithCountry("csid", "v2.3.7", in); len(got) != 3 {
			t.Errorf("country %q produced %d elements, want 3: %v", in, len(got), got)
		}
	}

	// Codes our own geolocation can emit must survive. MaxMind uses XK for Kosovo,
	// which is not an official ISO assignment — an allowlist would discard it and lose
	// real signal, which is why this is a denylist of non-countries instead.
	for _, in := range []string{"XK", "CN", "IR", "RU", "US", "GB"} {
		if got := normalizeCountry(in); got != in {
			t.Errorf("normalizeCountry(%q) = %q, want it preserved", in, got)
		}
	}
}
