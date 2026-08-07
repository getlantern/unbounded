package egress

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Ingest for the page-side freeze watchdog (ui/src/utils/freezeWatchdog.ts).
//
// This is the second half of a pair. The egress can already count sessions that ended
// with the donor no longer answering keepalive pings (session-teardowns,
// keepalive_timeout), but that signal cannot distinguish the case anyone cares about
// from the cases nobody does: a genuinely frozen page, a suspended laptop, and a tab
// closed without a clean handshake all look identical from here. At ~45% of sessions it
// is plainly dominated by the benign ones, so alerting on it would fire constantly and
// identify nothing.
//
// The watchdog can tell them apart — main_thread_blocked, go_scheduler_wedged,
// js_starved, page_died are four distinct diagnoses — and until now its reports had
// nowhere to go: a console line, a window global, and a beacon with no endpoint. Every
// freeze it correctly diagnosed died in the browser that diagnosed it.
//
// Three properties this endpoint has to hold, because it is unauthenticated and
// reachable by any browser on any site that embeds the widget:
//
//   - Only `kind` becomes a metric label. url, userAgent and longestTaskName are all
//     peer-chosen and unbounded; any of them as a label is unbounded cardinality
//     controlled by a stranger. kind is validated against a fixed set of five.
//   - The body is capped before it is read, not after. An unbounded POST to a Go
//     handler is a memory-allocation primitive for whoever sends it.
//   - The log is throttled and every peer-supplied field is truncated. This path can be
//     driven as fast as a script can loop, and the refusal log already demonstrated
//     what an unthrottled high-rate DEBUG line does to a journal.
//
// What it deliberately does not do is authenticate or rate-limit per IP. A determined
// caller can inflate the counter, which is a data-integrity problem rather than a
// resource one: the work per request is O(1), the body is bounded, the labels are
// bounded, and the log is throttled. Worth knowing before trusting the absolute numbers
// — treat them as "at least this many" and watch the shape rather than the total.

// freezeReportPath is where the widget beacons to. Caddy must proxy it; the egress mux
// serves nothing it is not routed.
const freezeReportPath = "/freeze"

// maxFreezeReportBytes caps the request body. A real report is a few hundred bytes; the
// slack is for a long User-Agent and a long embedder URL. Enforced with
// http.MaxBytesReader so an oversized body is refused during the read rather than
// buffered first.
const maxFreezeReportBytes = 8 << 10

// freezeReportLogInterval throttles the log per freeze kind. Longer than the refusal
// interval because these are supposed to be rare — if they are not, the counter says so
// and the log does not need to.
const freezeReportLogInterval = 5 * time.Minute

var freezeReportLogs = newLogThrottle(freezeReportLogInterval)

// freezeKind mirrors FreezeKind in freezeWatchdog.ts. Validated against this set rather
// than trusted, because it is the one field that becomes a metric label.
type freezeKind string

const (
	freezeMainThreadBlocked freezeKind = "main_thread_blocked"
	freezeGoSchedulerWedged freezeKind = "go_scheduler_wedged"
	freezeJSStarved         freezeKind = "js_starved"
	freezePageDied          freezeKind = "page_died"
	// freezeInvalid is recorded for anything that did not parse or carried an
	// unrecognized kind. A distinct label rather than a silent drop: a widget release
	// that changes the payload shape should show up as a visible category, not as
	// reports quietly ceasing.
	freezeInvalid freezeKind = "invalid"
)

func knownFreezeKind(k freezeKind) bool {
	switch k {
	case freezeMainThreadBlocked, freezeGoSchedulerWedged, freezeJSStarved, freezePageDied:
		return true
	}
	return false
}

var freezeReports = newLabeledTally()

func recordFreezeReport(kind freezeKind) {
	freezeReports.add(string(kind))
}

func eachFreezeReport(f func(kind freezeKind, count int64)) {
	freezeReports.each(func(label string, count int64) {
		f(freezeKind(label), count)
	})
}

// freezeReport is the subset of FreezeReport this endpoint reads. Unknown fields are
// ignored rather than rejected, so a widget that adds one keeps working against an
// older egress — the same forward-compatibility the subprotocol handshake lacked, which
// is why a "drive by" field reuse silently broke every client built before it.
type freezeReport struct {
	Kind      freezeKind `json:"kind"`
	Recovered bool       `json:"recovered"`
	GapMs     int64      `json:"gapMs"`
	// Pointers because null is meaningful and distinct from zero: a null goStaleMs
	// means the wasm binary published no heartbeat, while 0 would claim it was current.
	GoStaleMs       *int64  `json:"goStaleMs"`
	ClockSkewMs     *int64  `json:"clockSkewMs"`
	LongestTaskMs   *int64  `json:"longestTaskMs"`
	LongestTaskName *string `json:"longestTaskName"`
	Hidden          bool    `json:"hidden"`
	Sharing         bool    `json:"sharing"`
	// URL is the embedding site's address, which is the field that makes a report
	// actionable: it says which page froze. It is also peer-chosen, so it is truncated
	// and never becomes a label.
	URL       string `json:"url"`
	UserAgent string `json:"userAgent"`
}

// handleFreezeReport ingests one beacon.
//
// Always answers 204 with an empty body. navigator.sendBeacon ignores the response
// entirely, so there is nothing to say and no reason to spend bytes saying it — and
// returning 4xx for a malformed report would only teach a broken widget to retry.
// Rejection is recorded as freezeInvalid instead.
func (l proxyListener) handleFreezeReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Cap before reading. sendBeacon sends text/plain for a string body, so the
	// content type is not checked — it is not a signal worth acting on and browsers
	// vary.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFreezeReportBytes))
	if err != nil {
		recordFreezeReport(freezeInvalid)
		l.logFreezeReport(freezeInvalid, nil, r, "oversized or unreadable freeze report")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var report freezeReport
	if err := json.Unmarshal(body, &report); err != nil || !knownFreezeKind(report.Kind) {
		recordFreezeReport(freezeInvalid)
		l.logFreezeReport(freezeInvalid, nil, r, "unparseable or unknown-kind freeze report")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	recordFreezeReport(report.Kind)
	l.logFreezeReport(report.Kind, &report, r, "Freeze report from widget")
	w.WriteHeader(http.StatusNoContent)
}

// logFreezeReport emits at most one line per kind per interval, with every peer-supplied
// value bounded.
func (l proxyListener) logFreezeReport(kind freezeKind, report *freezeReport, r *http.Request, msg string) {
	shouldLog, suppressed := freezeReportLogs.allow(string(kind), time.Now())
	if !shouldLog {
		return
	}

	attrs := append(peerAttrs(r), "kind", string(kind))
	if suppressed > 0 {
		attrs = append(attrs, "suppressed_since_last", suppressed)
	}
	if report != nil {
		attrs = append(attrs,
			// The embedder URL, truncated. This is the field that says which page
			// froze, and the reason the payload carries it at all.
			"page_url", truncateForLog(report.URL),
			"page_user_agent", truncateForLog(report.UserAgent),
			// recovered separates a freeze the page survived from one it did not.
			// Only page_died is unrecovered, and it is the severe case: a hiccup
			// annoys a user, a death silently removes a donor from the network.
			"recovered", report.Recovered,
			"gap_ms", report.GapMs,
			"hidden", report.Hidden,
			"sharing", report.Sharing,
		)
		if report.GoStaleMs != nil {
			attrs = append(attrs, "go_stale_ms", *report.GoStaleMs)
		}
		if report.LongestTaskMs != nil {
			attrs = append(attrs, "longest_task_ms", *report.LongestTaskMs)
		}
		// The long-task attribution: the only field that says *what* froze rather
		// than merely that something did.
		if report.LongestTaskName != nil {
			attrs = append(attrs, "longest_task", truncateForLog(*report.LongestTaskName))
		}
	}

	slog.Debug(msg, attrs...)
}
