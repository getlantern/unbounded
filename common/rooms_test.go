package common

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeRoomTickets(t *testing.T) {
	long := strings.Repeat("a", MaxRoomTicketLen)
	got := NormalizeRoomTickets([]string{" a.b ", "", "a.b", "bad ticket", "x,y", "c-d_e.f", long, long + "a"})
	if want := []string{"a.b", "c-d_e.f", long}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if NormalizeRoomTickets(nil) != nil || NormalizeRoomTickets([]string{"", " "}) != nil {
		t.Error("nothing valid must normalize to nil")
	}
	var many []string
	for i := 0; i < MaxRoomTickets+5; i++ {
		many = append(many, fmt.Sprintf("t%d.x", i))
	}
	if got := NormalizeRoomTickets(many); len(got) != MaxRoomTickets || got[0] != "t0.x" {
		t.Errorf("kept %d tickets starting %q; want the first %d", len(got), got[0], MaxRoomTickets)
	}
}

func TestParseRoomTicketsHeader(t *testing.T) {
	got := ParseRoomTicketsHeader([]string{"a.b, c.d", "e.f,,a.b"})
	if want := []string{"a.b", "c.d", "e.f"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if ParseRoomTicketsHeader(nil) != nil {
		t.Error("no header must parse to nil")
	}
}

// ParseRoomTicketsHeader is NormalizeRoomTickets of every comma-separated
// piece, however the pieces are spread over values.
func TestParseRoomTicketsHeaderMatchesNormalize(t *testing.T) {
	var many []string
	for i := 0; i < MaxRoomTickets+5; i++ {
		many = append(many, fmt.Sprintf("t%d.x", i))
	}
	for _, values := range [][]string{
		{""},
		{",,,"},
		{" a.b ,bad ticket, a.b", "c.d"},
		{strings.Join(many, ",")},
		{strings.Join(many[:10], ","), "t0.x,t3.x", strings.Join(many[10:], ",")},
		{strings.Join(append([]string{"bad!", "a.b", "a.b"}, many...), ", ")},
		{strings.Repeat("a.b,", 50), "c.d"},
	} {
		var pieces []string
		for _, v := range values {
			pieces = append(pieces, strings.Split(v, ",")...)
		}
		if got, want := ParseRoomTicketsHeader(values), NormalizeRoomTickets(pieces); !slices.Equal(got, want) {
			t.Errorf("ParseRoomTicketsHeader(%q) = %q, want %q", values, got, want)
		}
	}
}

// Parsing stops at MaxRoomTickets tickets: whatever follows them, however
// long, costs nothing.
func TestParseRoomTicketsHeaderStopsEarly(t *testing.T) {
	var many []string
	for i := 0; i < MaxRoomTickets; i++ {
		many = append(many, fmt.Sprintf("t%d.x", i))
	}
	full := strings.Join(many, ",")
	short := []string{full + strings.Repeat(",junk.x", 10)}
	long := []string{full + strings.Repeat(",junk.x", 100_000), strings.Repeat("more.x,", 100_000)}
	if got := ParseRoomTicketsHeader(long); !slices.Equal(got, many) {
		t.Fatalf("got %d tickets, want the first %d", len(got), MaxRoomTickets)
	}
	allocs := func(values []string) float64 {
		return testing.AllocsPerRun(10, func() { ParseRoomTicketsHeader(values) })
	}
	if s, l := allocs(short), allocs(long); l > s {
		t.Errorf("a long tail after %d tickets cost %v allocations, a short one %v", MaxRoomTickets, l, s)
	}
}

func TestSignalMsgRoom(t *testing.T) {
	plain, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}"})
	if strings.Contains(string(plain), "Room") {
		t.Errorf("a non-room message changed on the wire: %s", plain)
	}
	room, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}", Room: true})
	if !IsRoomGenesis(room) || IsRoomGenesis(plain) {
		t.Error("IsRoomGenesis must report exactly the room message")
	}
	offer, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgOffer, Payload: "{}", Room: true})
	if IsRoomGenesis(offer) || IsRoomGenesis([]byte("not json")) {
		t.Error("only a genesis message can be a room genesis")
	}
	if rt, _, err := DecodeSignalMsg(room); err != nil || rt != "r" {
		t.Errorf("DecodeSignalMsg(room) = %q, %v", rt, err)
	}
	for raw, want := range map[string]bool{string(room): true, string(plain): false, string(offer): false} {
		msg, _, err := DecodeSignal([]byte(raw))
		if err != nil || msg.ReplyTo != "r" || msg.IsRoomGenesis() != want {
			t.Errorf("DecodeSignal(%s) = %+v, %v; want IsRoomGenesis %v", raw, msg, err, want)
		}
	}
	if msg, _, err := DecodeSignal([]byte("not json")); err == nil || msg != (SignalMsg{}) {
		t.Errorf("DecodeSignal(not json) = %+v, %v", msg, err)
	}
}

// Old and new forms must decode both ways: a pre-rooms struct (as an old
// client or Freddie has it) ignores Room, and the new struct reads a message
// without Room as a non-room message.
func TestSignalMsgRoomCompat(t *testing.T) {
	type oldSignalMsg struct {
		ReplyTo string
		Type    SignalMsgType
		Payload string
	}
	oldWire, _ := json.Marshal(oldSignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}"})
	newPlain, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}"})
	if string(oldWire) != string(newPlain) {
		t.Errorf("non-room wire changed: old %s, new %s", oldWire, newPlain)
	}
	var n SignalMsg
	if err := json.Unmarshal(oldWire, &n); err != nil || n.Room || n.ReplyTo != "r" || n.Type != SignalMsgGenesis {
		t.Errorf("new decode of old form = %+v, %v", n, err)
	}
	newRoom, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}", Room: true})
	var o oldSignalMsg
	if err := json.Unmarshal(newRoom, &o); err != nil || o.ReplyTo != "r" || o.Type != SignalMsgGenesis || o.Payload != "{}" {
		t.Errorf("old decode of new form = %+v, %v", o, err)
	}
	if rt, _, err := DecodeSignalMsg(oldWire); err != nil || rt != "r" {
		t.Errorf("DecodeSignalMsg(old) = %q, %v", rt, err)
	}
}
