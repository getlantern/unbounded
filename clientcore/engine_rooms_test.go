package clientcore

import (
	"reflect"
	"testing"
)

// The wasm exports only forward to these; they run natively here.
func TestEngineRoomSetters(t *testing.T) {
	opt := NewDefaultWebRTCOptions()
	b := &BroflakeEngine{rtcOpt: opt}
	b.setRoomTickets([]string{"aa.bb"})
	if got := opt.CurrentRoomTickets(); !reflect.DeepEqual([]string{"aa.bb"}, got) {
		t.Errorf("room tickets: got %v", got)
	}
	b.setServePool(false)
	if opt.ServesPool() {
		t.Error("setServePool(false) must stop serving the pool")
	}
	b.setServePool(true)
	if !opt.ServesPool() {
		t.Error("setServePool(true) must serve the pool")
	}
	b.setRoomTickets(nil)
	if got := opt.CurrentRoomTickets(); len(got) != 0 {
		t.Errorf("setRoomTickets(nil) must clear the tickets, got %v", got)
	}

	idle := &BroflakeEngine{}
	idle.setRoomTickets([]string{"aa.bb"}) // no options yet: a no-op, not a panic
	idle.setServePool(false)
}

// TestServePoolArg pins that setServePool fails closed: only a real JS
// boolean true serves the pool. jsArg hands servePoolArg nil for a missing,
// undefined or null argument, a string for a string and a float64 for a
// number.
func TestServePoolArg(t *testing.T) {
	for _, c := range []struct {
		name string
		v    any
		want bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"missing, undefined or null", nil, false},
		{`"true"`, "true", false},
		{`"false"`, "false", false},
		{`""`, "", false},
		{"1", float64(1), false},
		{"0", float64(0), false},
		{"array", []any{true}, false},
	} {
		if got := servePoolArg(c.v); got != c.want {
			t.Errorf("servePoolArg(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRoomTicketsArg pins setRoomTickets' argument mapping: anything it does
// not recognise clears every room.
func TestRoomTicketsArg(t *testing.T) {
	for _, c := range []struct {
		name string
		v    any
		want []string
	}{
		{"csv", "aa.bb,cc.dd", []string{"aa.bb", "cc.dd"}},
		{"csv with spaces", " aa.bb , cc.dd ", []string{"aa.bb", "cc.dd"}},
		{"csv with empty parts", "aa.bb,, ,", []string{"aa.bb"}},
		{"empty string", "", nil},
		{"array", []any{"aa.bb", " cc.dd "}, []string{"aa.bb", "cc.dd"}},
		{"array with non-strings", []any{"aa.bb", float64(1), nil, true}, []string{"aa.bb"}},
		{"empty array", []any{}, nil},
		{"missing, undefined or null", nil, nil},
		{"number", float64(3), nil},
		{"boolean", true, nil},
	} {
		if got := roomTicketsArg(c.v); !reflect.DeepEqual(got, c.want) {
			t.Errorf("roomTicketsArg(%s) = %#v, want %#v", c.name, got, c.want)
		}
	}
}
