package egress

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/getlantern/broflake/common"
)

func resetRefusals(t *testing.T) {
	t.Helper()
	refusals = newLabeledTally()
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

// The three subprotocol refusals have three different owners, and collapsing them
// is what made ~9 refusals/second unactionable for ten days: "missing" reads as
// "not a broflake client", "malformed" reads as "a broken donor", and the evidence
// was equally consistent with both.
func TestClassifySubprotocolRefusal(t *testing.T) {
	cookie := common.NewSubprotocolsResponse()[0] // the magic cookie, whatever it is

	for name, tc := range map[string]struct {
		raw        []string
		parsed     []string
		wantReason refusalReason
		wantLog    bool
	}{
		"absent header": {
			raw: nil, parsed: nil,
			wantReason: refusedMissingSubprotocols, wantLog: false,
		},
		// Present but empty: the header was plainly sent, so it must not be reported
		// as absent even though it parses to zero values.
		"present but empty": {
			raw: []string{","}, parsed: nil,
			wantReason: refusedForeignSubprotocols, wantLog: true,
		},
		// Our protocol, wrong arity — the shape the live refusals actually have.
		"cookie alone": {
			raw: []string{cookie}, parsed: []string{cookie},
			wantReason: refusedMalformedSubprotocols, wantLog: false,
		},
		"cookie plus one": {
			raw: []string{cookie + ",csid"}, parsed: []string{cookie, "csid"},
			wantReason: refusedMalformedSubprotocols, wantLog: false,
		},
		"cookie plus too many": {
			raw:        []string{"x"},
			parsed:     []string{cookie, "csid", "v2.3.5", "CN", "extra"},
			wantReason: refusedMalformedSubprotocols, wantLog: false,
		},
		"foreign single token": {
			raw: []string{"chat"}, parsed: []string{"chat"},
			wantReason: refusedForeignSubprotocols, wantLog: true,
		},
		"foreign multi token": {
			raw: []string{"graphql-ws,mqtt"}, parsed: []string{"graphql-ws", "mqtt"},
			wantReason: refusedForeignSubprotocols, wantLog: true,
		},
		// Wrong order. The parser requires the cookie to lead, so this does not parse
		// — but it is still recognizably our software getting it wrong, and it can
		// still carry a real CSID. So: malformed, and values withheld. Classifying it
		// foreign (as a leading-position check does) would log that CSID.
		"cookie not first": {
			raw: []string{"csid," + cookie}, parsed: []string{"csid", cookie},
			wantReason: refusedMalformedSubprotocols, wantLog: false,
		},
		"cookie last": {
			raw: []string{"a,b," + cookie}, parsed: []string{"a", "b", cookie},
			wantReason: refusedMalformedSubprotocols, wantLog: false,
		},
	} {
		reason, msg, logValues := classifySubprotocolRefusal(tc.raw, tc.parsed)
		if reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", name, reason, tc.wantReason)
		}
		if logValues != tc.wantLog {
			t.Errorf("%s: logValues = %v, want %v", name, logValues, tc.wantLog)
		}
		if msg == "" {
			t.Errorf("%s: empty message", name)
		}
	}
}

// The safety property, stated as its own test because it is the reason the values
// are logged at all: a peer that got the magic cookie right is plausibly a real
// client, so one of its values is plausibly a real consumer session ID. Values may
// be recorded only when the cookie did NOT match, which is precisely when the peer
// cannot have supplied one.
func TestClassifySubprotocolRefusal_NeverLogsValuesWhenCookieMatched(t *testing.T) {
	cookie := common.NewSubprotocolsResponse()[0]
	realCSID := "9f8c1e2a-secret-session-id"

	// Every shape that fails to parse while still carrying the cookie somewhere —
	// including the misordered ones, which a leading-position check misses. Those are
	// the cases that leaked: the CSID sits right next to a cookie that is present but
	// not first.
	for _, parsed := range [][]string{
		{cookie},
		{cookie, realCSID},
		{cookie, realCSID, "v2.3.5", "CN", "surplus"},
		{cookie, realCSID, "v2.3.5", "CN", "surplus", "more"},
		{realCSID, cookie},           // cookie second
		{realCSID, cookie, "v2.3.5"}, // right tokens, wrong order
		{"v2.3.5", realCSID, cookie}, // cookie last
		{"", cookie, realCSID},       // empty leading token
	} {
		reason, _, logValues := classifySubprotocolRefusal([]string{"raw"}, parsed)
		if logValues {
			t.Errorf("would log values for a cookie-matching peer: %v", parsed)
		}
		if reason != refusedMalformedSubprotocols {
			t.Errorf("parsed=%v reason = %q, want %q", parsed, reason, refusedMalformedSubprotocols)
		}
	}
}

// The legacy team client is the known cause of every refusal observed on
// unbounded-us: pre-2025-08-22 builds put a team identifier in the header that now
// carries the magic cookie. It gets its own label because the remedy is specific —
// those operators must upgrade — while "foreign" means we do not know who is calling.
func TestClassifySubprotocolRefusal_LegacyTeamClient(t *testing.T) {
	// Exactly what production sends, from 0658b1f's hardcoded placeholder.
	reason, msg, logValues := classifySubprotocolRefusal(
		[]string{"unbounded-team:no_team"}, []string{"unbounded-team:no_team"})
	if reason != refusedLegacyTeamClient {
		t.Errorf("reason = %q, want %q", reason, refusedLegacyTeamClient)
	}
	if !logValues {
		t.Error("values must be loggable: no cookie means no session ID to leak")
	}
	if msg == "" {
		t.Error("empty message")
	}

	// A build that actually set a team is the same client with the same problem, so
	// the match is on the prefix rather than the placeholder.
	if r, _, _ := classifySubprotocolRefusal(
		[]string{"unbounded-team:acme"}, []string{"unbounded-team:acme"}); r != refusedLegacyTeamClient {
		t.Errorf("a real team id should still classify as legacy, got %q", r)
	}
}

// The label must stay narrow: anything that merely mentions the prefix, or pairs it
// with other tokens, is not the legacy client and should not be reported as one — the
// point of the label is that it names a known cause.
func TestIsLegacyTeamClient_StaysNarrow(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []string
		want bool
	}{
		"exact placeholder":  {[]string{"unbounded-team:no_team"}, true},
		"real team":          {[]string{"unbounded-team:x"}, true},
		"empty team":         {[]string{"unbounded-team:"}, true},
		"nil":                {nil, false},
		"prefix absent":      {[]string{"chat"}, false},
		"prefix not leading": {[]string{"chat", "unbounded-team:x"}, false},
		"extra token":        {[]string{"unbounded-team:x", "extra"}, false},
		// Substring rather than prefix: not the legacy client.
		"prefix embedded": {[]string{"x-unbounded-team:x"}, false},
	} {
		if got := isLegacyTeamClient(tc.in); got != tc.want {
			t.Errorf("%s: isLegacyTeamClient(%q) = %v, want %v", name, tc.in, got, tc.want)
		}
	}
}

// The cookie check must win over the legacy check. It cannot overlap today, but the
// cookie check is the privacy guard and the guarantee should not rest on
// isLegacyTeamClient happening to stay narrow.
func TestClassifySubprotocolRefusal_CookieBeatsLegacy(t *testing.T) {
	cookie := common.NewSubprotocolsResponse()[0]
	realCSID := "9f8c1e2a-secret-session-id"

	reason, _, logValues := classifySubprotocolRefusal(
		[]string{"raw"}, []string{"unbounded-team:x", cookie, realCSID})
	if reason != refusedMalformedSubprotocols {
		t.Errorf("reason = %q, want %q", reason, refusedMalformedSubprotocols)
	}
	if logValues {
		t.Error("a cookie-carrying list must never have its values logged, legacy prefix or not")
	}
}

func resetRefusalLogs(t *testing.T) {
	t.Helper()
	refusalLogMx.Lock()
	refusalLogs = map[refusalReason]*refusalLogState{}
	refusalLogMx.Unlock()
}

// The first occurrence of a reason must log immediately. A condition nobody has
// seen before is exactly the one nobody is watching a dashboard for, so waiting an
// interval to mention it is the wrong default.
func TestShouldLogRefusal_FirstOccurrenceAlwaysLogs(t *testing.T) {
	resetRefusalLogs(t)
	t0 := time.Now()

	for _, reason := range []refusalReason{
		refusedLegacyTeamClient, refusedForeignSubprotocols, refusedMissingCSID,
	} {
		shouldLog, suppressed := shouldLogRefusal(reason, t0)
		if !shouldLog {
			t.Errorf("%s: first occurrence did not log", reason)
		}
		if suppressed != 0 {
			t.Errorf("%s: first occurrence reported %d suppressed, want 0", reason, suppressed)
		}
	}
}

// The point of the change: a flood collapses to one line per interval. At the
// observed ~9/s this is the difference between 96% of the journal and a rounding
// error.
func TestShouldLogRefusal_ThrottlesAFlood(t *testing.T) {
	resetRefusalLogs(t)
	t0 := time.Now()

	logged := 0
	// Five minutes of refusals at 9/s, stepping the clock rather than sleeping.
	for i := 0; i < 5*60*9; i++ {
		now := t0.Add(time.Duration(i) * (time.Second / 9))
		if shouldLog, _ := shouldLogRefusal(refusedLegacyTeamClient, now); shouldLog {
			logged++
		}
	}
	// One immediately, then one per minute across five minutes.
	if logged < 4 || logged > 7 {
		t.Errorf("logged %d lines for 2700 refusals over 5 minutes, want ~5", logged)
	}
}

// A throttled line must say how much it stands for, or it understates a flood by
// three orders of magnitude and reads like an isolated event.
func TestShouldLogRefusal_ReportsSuppressedCount(t *testing.T) {
	resetRefusalLogs(t)
	t0 := time.Now()

	shouldLogRefusal(refusedLegacyTeamClient, t0) // first, logs
	const hidden = 500
	for i := 0; i < hidden; i++ {
		if shouldLog, _ := shouldLogRefusal(refusedLegacyTeamClient, t0.Add(time.Second)); shouldLog {
			t.Fatal("logged inside the interval")
		}
	}

	shouldLog, suppressed := shouldLogRefusal(refusedLegacyTeamClient, t0.Add(refusalLogInterval))
	if !shouldLog {
		t.Fatal("did not log after the interval elapsed")
	}
	if suppressed != hidden {
		t.Errorf("suppressed = %d, want %d", suppressed, hidden)
	}

	// The backlog resets, so the next line does not double-count it.
	for i := 0; i < 3; i++ {
		shouldLogRefusal(refusedLegacyTeamClient, t0.Add(refusalLogInterval+time.Second))
	}
	_, suppressed = shouldLogRefusal(refusedLegacyTeamClient, t0.Add(2*refusalLogInterval))
	if suppressed != 3 {
		t.Errorf("second window reported %d suppressed, want 3 — backlog did not reset", suppressed)
	}
}

// Throttling is per-reason. A flood of one reason must not silence a different one,
// which is the whole reason this is keyed rather than global.
func TestShouldLogRefusal_PerReason(t *testing.T) {
	resetRefusalLogs(t)
	t0 := time.Now()

	shouldLogRefusal(refusedLegacyTeamClient, t0)
	for i := 0; i < 1000; i++ {
		shouldLogRefusal(refusedLegacyTeamClient, t0.Add(time.Second))
	}

	// A different reason, seen for the first time mid-flood, still logs.
	if shouldLog, _ := shouldLogRefusal(refusedBadProtocolVersion, t0.Add(time.Second)); !shouldLog {
		t.Error("a flood of one reason silenced the first occurrence of another")
	}
}

// The counter is the record and must stay exact regardless of what the log does.
// Throttling the log while also dropping counts would trade one blind spot for
// another.
func TestShouldLogRefusal_DoesNotAffectTheCounter(t *testing.T) {
	resetRefusals(t)
	resetRefusalLogs(t)
	t0 := time.Now()

	const n = 1000
	for i := 0; i < n; i++ {
		recordRefusal(refusedLegacyTeamClient)
		shouldLogRefusal(refusedLegacyTeamClient, t0.Add(time.Second))
	}
	if got := collectRefusals()[refusedLegacyTeamClient]; got != n {
		t.Errorf("counter = %d, want %d — throttling must not drop counts", got, n)
	}
}

// Refusals arrive concurrently from the HTTP handler, so the throttle state is
// shared mutable state on a hot path.
func TestShouldLogRefusal_Concurrent(t *testing.T) {
	resetRefusalLogs(t)
	t0 := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				shouldLogRefusal(refusedLegacyTeamClient, t0.Add(time.Second))
			}
		}()
	}
	wg.Wait()

	// 4000 concurrent attempts: the first creates the entry and logs, the other 3999
	// are suppressed and must all be counted. The probe is offset by the same second
	// the goroutines used, since that is when the entry's window started — probing at
	// t0+interval would land inside the window and suppress instead of reporting.
	const want = 8*500 - 1
	_, suppressed := shouldLogRefusal(refusedLegacyTeamClient, t0.Add(time.Second+refusalLogInterval))
	if suppressed != want {
		t.Errorf("suppressed = %d, want %d — a concurrent increment was lost", suppressed, want)
	}
}
