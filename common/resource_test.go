package common

import (
	"reflect"
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
