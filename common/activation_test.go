package common

import (
	"reflect"
	"strings"
	"testing"
)

func TestNewActivationID_ParsesBack(t *testing.T) {
	id := NewActivationID()
	if got := normalizeActivationID(id); got != id {
		t.Fatalf("NewActivationID() = %q, which normalizes to %q", id, got)
	}
	if NewActivationID() == id {
		t.Fatal("two calls returned the same ID")
	}
}

func TestParseSubprotocolsRequestWithActivation_Forms(t *testing.T) {
	id := NewActivationID()
	for _, tc := range []struct {
		name        string
		country, id string
		wantLen     int
	}{
		{"neither", "", "", 3},
		{"country only", "CN", "", 4},
		{"activation only", "", id, 4},
		{"both", "IR", id, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := NewSubprotocolsRequestWithActivation("csid-1", "v2.3.17", tc.country, tc.id)
			if len(in) != tc.wantLen {
				t.Fatalf("%d elements, want %d: %v", len(in), tc.wantLen, in)
			}
			csid, version, country, aid, ok := ParseSubprotocolsRequestWithActivation(in)
			if !ok {
				t.Fatalf("rejected %v", in)
			}
			if csid != "csid-1" || version != "v2.3.17" || country != tc.country || aid != tc.id {
				t.Fatalf("got csid=%q version=%q country=%q activation=%q", csid, version, country, aid)
			}
		})
	}
}

// Without an ID the request must be byte-identical to the country-only
// constructor, so a widget with proxy mode off, or one that has not yet
// minted an ID, looks exactly like every release before this one.
func TestNewSubprotocolsRequestWithActivation_EmptyIsWireIdentical(t *testing.T) {
	for _, id := range []string{"", "not-an-id"} {
		got := NewSubprotocolsRequestWithActivation("csid-1", "v2.3.17", "CN", id)
		want := NewSubprotocolsRequestWithCountry("csid-1", "v2.3.17", "CN")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("id %q changed the wire form:\n got=%v\nwant=%v", id, got, want)
		}
	}
}

// An egress on v2.3.16 or earlier takes a 4-element list and treats the
// fourth element as a country. The activation-only form must land there
// as "no country", not as a refusal, so this pins the old parser's
// behavior on that input.
func TestParseSubprotocolsRequest_ActivationOnlyIsSafeForOldParser(t *testing.T) {
	in := NewSubprotocolsRequestWithActivation("csid-1", "v2.3.17", "", NewActivationID())
	if len(in) != 4 {
		t.Fatalf("%d elements, want 4: %v", len(in), in)
	}
	if got := normalizeCountry(in[3]); got != "" {
		t.Fatalf("old parser would read the activation element as country %q", got)
	}
}

// The ID becomes a key in the egress's seen-set, so anything that is not
// exactly the form NewActivationID produces is dropped rather than
// stored. Dropping it must not refuse the connection.
func TestParseSubprotocolsRequestWithActivation_BoundsUntrustedID(t *testing.T) {
	id := NewActivationID()
	for _, bad := range []string{
		"",
		id[:31],
		id + "0",
		strings.ToUpper(id),
		strings.Repeat("g", 32),
		strings.Repeat("a", 4096),
	} {
		in := []string{subprotocolsMagicCookie, "csid", "v2.3.17", "CN", activationPrefix + bad}
		_, _, country, aid, ok := ParseSubprotocolsRequestWithActivation(in)
		if !ok {
			t.Errorf("ID %.40q refused the connection, want accepted with no ID", bad)
			continue
		}
		if aid != "" {
			t.Errorf("ID %.40q parsed as %q, want dropped", bad, aid)
		}
		if country != "CN" {
			t.Errorf("ID %.40q lost the country: got %q", bad, country)
		}
	}
}

func TestParseSubprotocolsRequestWithActivation_RejectsMalformedLists(t *testing.T) {
	act := activationPrefix + NewActivationID()
	c := subprotocolsMagicCookie
	for _, in := range [][]string{
		nil,
		{c},
		{c, "csid"},
		{"wrong", "csid", "v", act},
		{c, "csid", "v", act, "CN"},
		{c, "csid", "v", act, act},
		{c, "csid", "v", "CN", "IR"},
		{c, "csid", "v", "CN", "IR", act},
	} {
		if _, _, _, _, ok := ParseSubprotocolsRequestWithActivation(in); ok {
			t.Errorf("ParseSubprotocolsRequestWithActivation(%v) = ok, want rejected", in)
		}
	}
}
