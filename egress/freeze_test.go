package egress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func resetFreezeReports(t *testing.T) {
	t.Helper()
	freezeReports = newLabeledTally()
	freezeReportLogs = newLogThrottle(freezeReportLogInterval)
}

func collectFreezeReports() map[freezeKind]int64 {
	out := map[freezeKind]int64{}
	eachFreezeReport(func(kind freezeKind, count int64) { out[kind] = count })
	return out
}

func postFreeze(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	// text/plain is what navigator.sendBeacon sends for a string body.
	r := httptest.NewRequest(http.MethodPost, freezeReportPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.RemoteAddr = "[::1]:40000"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	proxyListener{}.handleFreezeReport(w, r)
	return w
}

func validReport(kind freezeKind) string {
	b, _ := json.Marshal(freezeReport{
		Kind: kind, Recovered: true, GapMs: 12000,
		URL: "https://example.org/page", UserAgent: "Mozilla/5.0",
	})
	return string(b)
}

// Each of the four diagnoses must be counted under its own label — that distinction is
// the entire value of the watchdog over the server-side keepalive signal, which cannot
// tell a freeze from a sleeping laptop.
func TestHandleFreezeReport_CountsEachKind(t *testing.T) {
	resetFreezeReports(t)
	for _, k := range []freezeKind{
		freezeMainThreadBlocked, freezeGoSchedulerWedged, freezeJSStarved, freezePageDied,
	} {
		if got := postFreeze(t, validReport(k)).Code; got != http.StatusNoContent {
			t.Errorf("%s: status %d, want 204", k, got)
		}
	}
	got := collectFreezeReports()
	for _, k := range []freezeKind{
		freezeMainThreadBlocked, freezeGoSchedulerWedged, freezeJSStarved, freezePageDied,
	} {
		if got[k] != 1 {
			t.Errorf("%s = %d, want 1", k, got[k])
		}
	}
	if got[freezeInvalid] != 0 {
		t.Errorf("valid reports produced %d invalid", got[freezeInvalid])
	}
}

// A kind is the only field that becomes a metric label, so an unrecognized one must be
// bucketed rather than trusted — otherwise a stranger chooses our label set.
func TestHandleFreezeReport_RejectsUnknownKind(t *testing.T) {
	resetFreezeReports(t)
	for _, body := range []string{
		`{"kind":"totally_made_up"}`,
		`{"kind":""}`,
		`{"kind":"MAIN_THREAD_BLOCKED"}`, // case-sensitive on purpose
		`{"kind":"main_thread_blocked ";}`,
		`not json at all`,
		``,
	} {
		w := postFreeze(t, body)
		if w.Code != http.StatusNoContent {
			t.Errorf("body %q: status %d, want 204 regardless", body, w.Code)
		}
	}
	got := collectFreezeReports()
	if got[freezeInvalid] != 6 {
		t.Errorf("invalid = %d, want 6: %v", got[freezeInvalid], got)
	}
	if len(got) != 1 {
		t.Errorf("unknown kinds leaked into the label set: %v", got)
	}
}

// An unbounded POST to a Go handler is a memory-allocation primitive for whoever sends
// it, so the body is capped during the read rather than buffered first.
func TestHandleFreezeReport_CapsBodySize(t *testing.T) {
	resetFreezeReports(t)

	huge := `{"kind":"page_died","url":"` + strings.Repeat("A", maxFreezeReportBytes*2) + `"}`
	if got := postFreeze(t, huge).Code; got != http.StatusNoContent {
		t.Errorf("status %d, want 204", got)
	}
	if got := collectFreezeReports()[freezeInvalid]; got != 1 {
		t.Errorf("oversized body recorded as %v, want one invalid", collectFreezeReports())
	}

	// Just under the cap still works, so the limit is not so tight that real reports
	// with a long embedder URL and User-Agent are rejected.
	resetFreezeReports(t)
	ok := `{"kind":"page_died","url":"https://example.org/` + strings.Repeat("b", 2000) + `"}`
	if len(ok) >= maxFreezeReportBytes {
		t.Fatalf("test fixture is %d bytes, not under the %d cap", len(ok), maxFreezeReportBytes)
	}
	postFreeze(t, ok)
	if got := collectFreezeReports()[freezePageDied]; got != 1 {
		t.Errorf("a realistic long report was rejected: %v", collectFreezeReports())
	}
}

// Only POST. A GET that counted would let any crawler inflate the numbers.
func TestHandleFreezeReport_PostOnly(t *testing.T) {
	resetFreezeReports(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, freezeReportPath, nil)
		w := httptest.NewRecorder()
		proxyListener{}.handleFreezeReport(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, w.Code)
		}
		if w.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow = %q, want POST", method, w.Header().Get("Allow"))
		}
	}
	if len(collectFreezeReports()) != 0 {
		t.Errorf("a non-POST was counted: %v", collectFreezeReports())
	}
}

// This path can be driven as fast as a script can loop, and the refusal log already
// demonstrated what an unthrottled high-rate DEBUG line does to a journal.
func TestHandleFreezeReport_ThrottlesTheLog(t *testing.T) {
	resetFreezeReports(t)
	t0 := time.Now()

	logged := 0
	for i := 0; i < 500; i++ {
		if shouldLog, _ := freezeReportLogs.allow(string(freezePageDied), t0); shouldLog {
			logged++
		}
	}
	if logged != 1 {
		t.Errorf("logged %d of 500 within one interval, want 1", logged)
	}

	shouldLog, suppressed := freezeReportLogs.allow(string(freezePageDied), t0.Add(freezeReportLogInterval))
	if !shouldLog {
		t.Error("did not log after the interval elapsed")
	}
	if suppressed != 499 {
		t.Errorf("suppressed = %d, want 499", suppressed)
	}
}

// The counter must see every report even while the log sees one, or throttling would
// trade one blind spot for another.
func TestHandleFreezeReport_ThrottlingDoesNotAffectTheCounter(t *testing.T) {
	resetFreezeReports(t)
	const n = 200
	for i := 0; i < n; i++ {
		postFreeze(t, validReport(freezePageDied))
	}
	if got := collectFreezeReports()[freezePageDied]; got != n {
		t.Errorf("counter = %d, want %d", got, n)
	}
}

// The response body must stay empty. sendBeacon ignores it, so bytes spent here are
// bytes spent for nobody — and a 4xx would teach a broken widget to retry.
func TestHandleFreezeReport_AlwaysEmpty204(t *testing.T) {
	resetFreezeReports(t)
	for _, body := range []string{validReport(freezePageDied), `garbage`} {
		w := postFreeze(t, body)
		if w.Code != http.StatusNoContent {
			t.Errorf("status %d, want 204", w.Code)
		}
		if w.Body.Len() != 0 {
			t.Errorf("response body = %q, want empty", w.Body.String())
		}
	}
}

// Unknown fields must be ignored, not rejected, so a widget that adds one keeps working
// against an older egress. The subprotocol handshake lacked exactly this and a field
// reuse silently broke every client built before it.
func TestHandleFreezeReport_ToleratesUnknownFields(t *testing.T) {
	resetFreezeReports(t)
	body := `{"kind":"page_died","url":"https://example.org","somethingNew":42,"nested":{"a":1}}`
	postFreeze(t, body)
	if got := collectFreezeReports()[freezePageDied]; got != 1 {
		t.Errorf("a report with unknown fields was rejected: %v", collectFreezeReports())
	}
}

// MaxBytesReader must be handed nil, not the ResponseWriter.
//
// Given a real one it calls requestTooLarge() on exceed, which sets closeAfterReply and
// adds "Connection: close" — so an oversized report would get a different response than
// a normal one and drop the connection, for a request whose response nobody reads.
//
// Asserted by reading the source, because the behavior is genuinely untestable from this
// package. requestTooLarge() is an *unexported* method of net/http, so the interface
// MaxBytesReader probes for can only be satisfied by a type in net/http; a same-named
// method on a test type here does not satisfy it. My first attempt at this test did
// exactly that and passed with either nil or w, proving nothing. Same approach as
// TestMetricCallback_ObservesOnlyDeclaredInstruments: when the failure is a wrong
// argument in one call, check the call.
func TestHandleFreezeReport_MaxBytesReaderGetsNil(t *testing.T) {
	src, err := os.ReadFile("freeze.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := regexp.MustCompile(`http\.MaxBytesReader\(\s*(\w+)`).FindAllStringSubmatch(string(src), -1)
	if len(calls) == 0 {
		t.Fatal("no MaxBytesReader call found; this test needs updating")
	}
	for _, c := range calls {
		if c[1] != "nil" {
			t.Errorf("MaxBytesReader called with %q, want nil — a real ResponseWriter lets an "+
				"oversized body add Connection: close and drop the connection", c[1])
		}
	}
}

// The oversized path must still produce the documented empty 204 and be counted.
func TestHandleFreezeReport_OversizedIsCountedAndQuiet(t *testing.T) {
	resetFreezeReports(t)
	huge := `{"kind":"page_died","url":"` + strings.Repeat("A", maxFreezeReportBytes*2) + `"}`
	w := postFreeze(t, huge)
	if w.Code != http.StatusNoContent {
		t.Errorf("status %d, want 204", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", w.Body.String())
	}
	if got := collectFreezeReports()[freezeInvalid]; got != 1 {
		t.Errorf("counted %v, want one invalid", collectFreezeReports())
	}
}
