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
