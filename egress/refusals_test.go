package egress

import (
	"net/http/httptest"
	"sync"
	"testing"
)

func resetRefusals(t *testing.T) {
	t.Helper()
	refusalsMx.Lock()
	refusals = map[string]*int64{}
	refusalsMx.Unlock()
}

func collectRefusals(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	eachRefusal(func(reason string, count int64) { out[reason] = count })
	return out
}

// The tally must be monotonic, unlike ingress-bytes which the otel callback
// drains each interval. Observing it must not reset it, or every rate() over the
// metric would be wrong.
func TestRecordRefusal_IsMonotonicAcrossObservations(t *testing.T) {
	resetRefusals(t)
	for i := 0; i < 3; i++ {
		recordRefusal(refusedMissingSubprotocols)
	}
	if got := collectRefusals(t)[refusedMissingSubprotocols]; got != 3 {
		t.Fatalf("after 3 refusals: got %d, want 3", got)
	}
	// A second observation must report the same total, not zero.
	if got := collectRefusals(t)[refusedMissingSubprotocols]; got != 3 {
		t.Fatalf("observing drained the tally: got %d, want 3", got)
	}
	recordRefusal(refusedMissingSubprotocols)
	if got := collectRefusals(t)[refusedMissingSubprotocols]; got != 4 {
		t.Fatalf("after a 4th refusal: got %d, want 4", got)
	}
}

func TestRecordRefusal_SeparatesReasons(t *testing.T) {
	resetRefusals(t)
	recordRefusal(refusedMissingSubprotocols)
	recordRefusal(refusedBadProtocolVersion)
	recordRefusal(refusedBadProtocolVersion)
	recordRefusal(refusedMissingCSID)

	got := collectRefusals(t)
	for reason, want := range map[string]int64{
		refusedMissingSubprotocols: 1,
		refusedBadProtocolVersion:  2,
		refusedMissingCSID:         1,
	} {
		if got[reason] != want {
			t.Errorf("%s = %d, want %d", reason, got[reason], want)
		}
	}
	// Only reasons actually seen should appear; an unseen reason must not
	// materialize a zero series.
	if len(got) != 3 {
		t.Errorf("reported %d reasons, want 3: %v", len(got), got)
	}
}

// Refusals arrive concurrently from the HTTP handler while the otel callback
// observes. No increment may be lost.
func TestRecordRefusal_NoLostCountsUnderConcurrency(t *testing.T) {
	resetRefusals(t)
	const writers, per = 8, 500

	stop := make(chan struct{})
	var obsWg sync.WaitGroup
	obsWg.Add(1)
	go func() {
		defer obsWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				collectRefusals(t)
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				recordRefusal(refusedMissingSubprotocols)
			}
		}()
	}
	wg.Wait()
	close(stop)
	obsWg.Wait()

	if got := collectRefusals(t)[refusedMissingSubprotocols]; got != writers*per {
		t.Fatalf("counted %d, want %d", got, writers*per)
	}
}

// peerAttrs exists to discriminate "one misdirected monitor" from "many broken
// clients", so it must surface the forwarded address and User-Agent — RemoteAddr
// alone is always Caddy's loopback and tells us nothing.
func TestPeerAttrs_SurfacesForwardedAddressAndUserAgent(t *testing.T) {
	r := httptest.NewRequest("GET", "/ws", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("User-Agent", "some-monitor/1.0")

	attrs := peerAttrs(r)
	if len(attrs)%2 != 0 {
		t.Fatalf("peerAttrs must return key/value pairs, got odd length %d", len(attrs))
	}
	kv := map[string]any{}
	for i := 0; i < len(attrs); i += 2 {
		k, ok := attrs[i].(string)
		if !ok {
			t.Fatalf("attr key %d is not a string: %#v", i, attrs[i])
		}
		kv[k] = attrs[i+1]
	}
	for k, want := range map[string]string{
		"remote_addr":   "127.0.0.1:54321",
		"forwarded_for": "203.0.113.7",
		"user_agent":    "some-monitor/1.0",
	} {
		if kv[k] != want {
			t.Errorf("%s = %v, want %q", k, kv[k], want)
		}
	}
}

// Absent headers must yield empty strings rather than panicking or omitting keys,
// since a client that reaches 9001 without Caddy would have no X-Forwarded-For —
// and that absence is itself the signal.
func TestPeerAttrs_HandlesMissingHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/ws", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Del("User-Agent")

	kv := map[string]any{}
	attrs := peerAttrs(r)
	for i := 0; i < len(attrs); i += 2 {
		kv[attrs[i].(string)] = attrs[i+1]
	}
	if kv["forwarded_for"] != "" {
		t.Errorf("forwarded_for = %v, want empty", kv["forwarded_for"])
	}
	if _, present := kv["user_agent"]; !present {
		t.Error("user_agent key must be present even when the header is absent")
	}
}
