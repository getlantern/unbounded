package clientcore

import (
	"slices"
	"sync"
	"testing"
)

func TestWebRTCOptions_TargetCountries(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	if got := o.targetCountries(); got != nil {
		t.Fatalf("default target countries = %q, want none", got)
	}

	o.SetTargetCountries([]string{"ir", "", "bogus", "IR", "ru"})
	if got := o.targetCountries(); !slices.Equal(got, []string{"IR", "RU"}) {
		t.Errorf("got %q, want [IR RU]", got)
	}

	// strings.Split("", ",") is what the wasm bridge passes to clear the preference
	o.SetTargetCountries([]string{""})
	if got := o.targetCountries(); got != nil {
		t.Errorf("after clearing got %q, want none", got)
	}

	// Set directly on the struct, as a native embedder would before NewBroflake
	o.TargetCountries = []string{"cn"}
	if got := o.targetCountries(); !slices.Equal(got, []string{"CN"}) {
		t.Errorf("got %q, want [CN]", got)
	}
}

// Producer workers read the targets while the UI may set them; run with -race.
func TestWebRTCOptions_TargetCountriesConcurrent(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.SetTargetCountries([]string{"IR"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.targetCountries()
			}
		}()
	}
	wg.Wait()
}

func TestWebRTCOptions_RoomTickets(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	if got := o.CurrentRoomTickets(); got != nil {
		t.Fatalf("default room tickets = %q, want none", got)
	}
	if !o.ServesPool() {
		t.Fatal("a new producer must serve the pool")
	}
	var zero WebRTCOptions
	if !zero.ServesPool() {
		t.Fatal("the zero value must serve the pool, as every release before rooms did")
	}

	o.SetRoomTickets([]string{"a.b", "", "a.b", "bad ticket", "c.d"})
	if got := o.CurrentRoomTickets(); !slices.Equal(got, []string{"a.b", "c.d"}) {
		t.Errorf("got %q, want [a.b c.d]", got)
	}
	o.SetServePool(false)
	if o.ServesPool() {
		t.Error("SetServePool(false) left the pool on")
	}
	o.SetServePool(true)
	if !o.ServesPool() {
		t.Error("SetServePool(true) left the pool off")
	}

	// A source, as lantern-box injects for a consumer, wins over the set value.
	o.RoomTicketSource = func() []string { return []string{"e.f", "e.f", ""} }
	if got := o.CurrentRoomTickets(); !slices.Equal(got, []string{"e.f"}) {
		t.Errorf("with a source got %q, want [e.f]", got)
	}
}

// Workers read the room settings while radiance sets them; run with -race.
func TestWebRTCOptions_RoomTicketsConcurrent(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.SetRoomTickets([]string{"a.b"})
				o.SetServePool(j%2 == 0)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.CurrentRoomTickets()
				_ = o.ServesPool()
			}
		}()
	}
	wg.Wait()
}
