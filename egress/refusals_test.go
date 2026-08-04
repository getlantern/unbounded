package egress

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func resetRefusals(t *testing.T) {
	t.Helper()
	refusalsMx.Lock()
	refusals = map[refusalReason]*int64{}
	refusalsMx.Unlock()
}

// Deliberately takes no *testing.T: it is called from the observer goroutine in
// TestRecordRefusal_NoLostCountsUnderConcurrency, and nothing here needs t.
func collectRefusals() map[refusalReason]int64 {
	out := map[refusalReason]int64{}
	eachRefusal(func(reason refusalReason, count int64) { out[reason] = count })
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
	if got := collectRefusals()[refusedMissingSubprotocols]; got != 3 {
		t.Fatalf("after 3 refusals: got %d, want 3", got)
	}
	// A second observation must report the same total, not zero.
	if got := collectRefusals()[refusedMissingSubprotocols]; got != 3 {
		t.Fatalf("observing drained the tally: got %d, want 3", got)
	}
	recordRefusal(refusedMissingSubprotocols)
	if got := collectRefusals()[refusedMissingSubprotocols]; got != 4 {
		t.Fatalf("after a 4th refusal: got %d, want 4", got)
	}
}

func TestRecordRefusal_SeparatesReasons(t *testing.T) {
	resetRefusals(t)
	recordRefusal(refusedMissingSubprotocols)
	recordRefusal(refusedBadProtocolVersion)
	recordRefusal(refusedBadProtocolVersion)
	recordRefusal(refusedMissingCSID)

	got := collectRefusals()
	for reason, want := range map[refusalReason]int64{
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
				collectRefusals()
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

	if got := collectRefusals()[refusedMissingSubprotocols]; got != writers*per {
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

// The split between "absent" and "malformed" is the whole point of the second
// reason: they implicate different callers, and conflating them sent the original
// investigation of ~10 refusals/second toward the wrong hypothesis. Pin that the
// two constants are distinct and tallied separately, so a future refactor can't
// quietly collapse them back into one.
func TestRefusalReasons_AbsentAndMalformedAreDistinct(t *testing.T) {
	if refusedMissingSubprotocols == refusedMalformedSubprotocols {
		t.Fatal("absent and malformed must be distinct reasons")
	}

	resetRefusals(t)
	recordRefusal(refusedMissingSubprotocols)
	recordRefusal(refusedMalformedSubprotocols)
	recordRefusal(refusedMalformedSubprotocols)

	got := collectRefusals()
	if got[refusedMissingSubprotocols] != 1 {
		t.Errorf("%s = %d, want 1", refusedMissingSubprotocols, got[refusedMissingSubprotocols])
	}
	if got[refusedMalformedSubprotocols] != 2 {
		t.Errorf("%s = %d, want 2", refusedMalformedSubprotocols, got[refusedMalformedSubprotocols])
	}
}

// recordRefusal takes a refusalReason, not a string, so request-derived data
// cannot reach a metric label without an explicit conversion that a reviewer
// would see. This compiles only while that holds.
func TestRecordRefusal_TakesTypedReason(t *testing.T) {
	var f func(refusalReason) = recordRefusal
	_ = f
	// A bare string must not be assignable to the parameter type; if someone
	// widens it back to string, the line above stops compiling.
}

// Both headers peerAttrs logs are entirely client-controlled and unbounded in
// size. On a path refusing ~10 connections/second, writing them verbatim turns a
// large header into sustained disk pressure, so they must be bounded — while
// honest values pass through untouched, or the log stops identifying the caller.
func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
	short := "Mozilla/5.0 (compatible; some-monitor/1.0)"
	if got := truncateForLog(short); got != short {
		t.Errorf("short value was altered: got %q, want %q", got, short)
	}
	atLimit := strings.Repeat("a", maxLoggedValueLen)
	if got := truncateForLog(atLimit); got != atLimit {
		t.Error("a value exactly at the limit must pass through unchanged")
	}

	over := strings.Repeat("a", maxLoggedValueLen+500)
	got := truncateForLog(over)
	// The ceiling must include the marker. A cap its own marker can exceed is
	// not a cap, and this is the contract the comment on maxLoggedValueLen makes.
	if len(got) > maxLoggedValueLen {
		t.Fatalf("result exceeds the documented ceiling: got %d bytes, want <= %d", len(got), maxLoggedValueLen)
	}
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Error("truncation must be marked, or a shortened value is indistinguishable from a short one")
	}

	// A multi-byte value truncated mid-rune must not emit invalid UTF-8, which
	// would break log ingestion downstream.
	multi := strings.Repeat("日", maxLoggedValueLen) // 3 bytes per rune
	if got := truncateForLog(multi); !utf8.ValidString(got) {
		t.Error("truncation produced invalid UTF-8")
	}
}

// A value short enough to skip truncation can still be invalid UTF-8, and the
// helper promises callers that nothing invalid reaches slog.
func TestTruncateForLog_NormalizesShortInvalidUTF8(t *testing.T) {
	for name, in := range map[string]string{
		"lone continuation byte": string([]byte{0xff}),
		"truncated multibyte":    string([]byte{0xe6, 0x97}), // first 2 bytes of 日
		"valid then invalid":     "ok" + string([]byte{0xfe}),
	} {
		got := truncateForLog(in)
		if !utf8.ValidString(got) {
			t.Errorf("%s: truncateForLog(%q) = %q, which is not valid UTF-8", name, in, got)
		}
	}
	// Valid short values must still pass through byte-identical.
	for _, ok := range []string{"", "curl/8.4.0", "日本語"} {
		if got := truncateForLog(ok); got != ok {
			t.Errorf("valid value altered: truncateForLog(%q) = %q", ok, got)
		}
	}
}
