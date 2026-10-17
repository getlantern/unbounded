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
