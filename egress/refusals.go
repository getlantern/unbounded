package egress

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/broflake/common"
)

// Refusals happen before handleWebsocket creates a session span, so until this
// existed they produced no metric and no trace — only a DEBUG log on a host
// nobody tails. That blindness was not hypothetical: the egress had been refusing
// roughly 10 connections per second since at least 2026-07-26 (59,144 in a
// 100-minute window), every one of them for the same reason, and it went unnoticed
// because the only evidence was journalctl.
//
// Port 9001 is firewalled to localhost and Caddy reverse-proxies /ws to it, so
// those refusals are not internet scan noise — they are clients that actually
// reached the endpoint and were turned away. Whether that is one misdirected
// health check or many broken donors changes what we do about it, and neither the
// count nor the source was observable.

// refusalReason is a distinct type rather than a bare string so a future caller
// cannot pass request-derived data without an explicit, visible conversion.
// These values become metric label content, and a label built from whatever a
// client sends is unbounded cardinality controlled by the caller.
type refusalReason string

const (
	// refusedMissingSubprotocols: no Sec-Websocket-Protocol header at all.
	refusedMissingSubprotocols refusalReason = "missing_subprotocols"
	// refusedMalformedSubprotocols: the magic cookie appears somewhere in the list
	// but the list does not parse — wrong arity, or the cookie not in the leading
	// position. This is *our* protocol with the wrong shape, so it implicates a
	// broflake client: a version skew, a hand-built list, or misordered tokens.
	refusedMalformedSubprotocols refusalReason = "malformed_subprotocols"
	// refusedForeignSubprotocols: the header was present and the magic cookie appears
	// nowhere in it, so the peer shows no sign of speaking this protocol at all.
	//
	// Split out from "malformed" because the two point at completely different
	// owners and the distinction is expensive to get wrong. For ten days the egress
	// refused ~9 connections/second and reported them as "missing subprotocols",
	// which reads as "not a broflake client"; the v2.3.5 deploy reclassified every
	// one as malformed, which reads as "a broken donor". Neither was specific enough
	// to act on, and both were consistent with the same evidence. This label answers
	// the question that actually decides who to go talk to, from the metric alone,
	// with no need to log anything a peer sent.
	refusedForeignSubprotocols refusalReason = "foreign_subprotocols"
	// refusedLegacyTeamClient: a recognized *ex*-broflake client. It sends the team
	// identifier that clients built before 2025-08-22 put in this header, which is
	// where the cookie now goes. Split out from "foreign" because the cause is known
	// and the remedy is specific — those operators need to upgrade — whereas
	// "foreign" means we genuinely do not know who is calling.
	//
	// This accounted for every refusal on unbounded-us when it was added: ~9/s from
	// 9 fixed hosts, unbroken since at least 2026-07-26 and in fact since the
	// handshake changed under them nearly a year earlier.
	refusedLegacyTeamClient   refusalReason = "legacy_team_client"
	refusedBadProtocolVersion refusalReason = "bad_protocol_version"
	refusedMissingCSID        refusalReason = "missing_consumer_session_id"
)

// legacyTeamIDPrefix is what pre-2025-08-22 clients put in Sec-Websocket-Protocol.
// (Go's canonical casing, matching common.SubprotocolsHeader; RFC 6455 spells it
// Sec-WebSocket-Protocol. See that constant for why the difference matters.)
//
// Introduced in 0658b1f ("send teamId from consumer -> egress via websocket protocol
// header", 2025-04-10) as common.TeamIdPrefix, and removed from common in 6561021
// when the team mechanism moved to the QUIC layer. Redeclared here rather than
// restored to common on purpose: nothing in this repo should *emit* it again, and a
// private constant in the one place that still recognizes it says so.
//
// Those clients cannot be served, which is why this only labels them. They predate
// both the consumer session ID (db39eb2, 2025-07-17) and QUIC connection migration
// (7c86b73, 2025-08-06), and the csid exists *as* the migration key. Synthesizing one
// to let them in would register connection state that nothing can ever migrate to,
// and hold it through the migration window on every disconnect — spending resources
// on sessions that cannot resume, to pretend a client speaks a protocol it does not.
const legacyTeamIDPrefix = "unbounded-team:"

// isLegacyTeamClient reports whether the peer is a recognized pre-handshake client.
//
// Prefix rather than equality: the identifier after the colon is the team, and while
// every observed client sends the hardcoded "no_team" placeholder from 0658b1f, a
// build that actually set one would be the same client with the same problem.
func isLegacyTeamClient(parsed []string) bool {
	return len(parsed) == 1 && strings.HasPrefix(parsed[0], legacyTeamIDPrefix)
}

var (
	refusalsMx sync.Mutex
	// refusals is keyed by the constants above only. Entries are never removed:
	// the set is small and fixed, and a reason that stops occurring should keep
	// reporting its running total rather than vanishing from the series.
	refusals = map[refusalReason]*int64{}
)

// recordRefusal counts one refused connection. Monotonic on purpose: this is a
// tally, not a gauge, so consumers can rate() it. Contrast ingress-bytes, which
// the otel callback drains each interval because it measures throughput.
func recordRefusal(reason refusalReason) {
	refusalsMx.Lock()
	c, ok := refusals[reason]
	if !ok {
		c = new(int64)
		refusals[reason] = c
	}
	refusalsMx.Unlock()
	atomic.AddInt64(c, 1)
}

// eachRefusal reports every reason seen so far. Snapshots under the lock so the
// otel callback never holds refusalsMx while observing.
func eachRefusal(f func(reason refusalReason, count int64)) {
	type row struct {
		reason refusalReason
		c      *int64
	}
	refusalsMx.Lock()
	rows := make([]row, 0, len(refusals))
	for reason, c := range refusals {
		rows = append(rows, row{reason, c})
	}
	refusalsMx.Unlock()

	for _, r := range rows {
		f(r.reason, atomic.LoadInt64(r.c))
	}
}

// refusalLogInterval bounds how often any single refusal reason may emit a log
// line. The metric is unaffected and stays exact — this only samples the log.
//
// Needed because the log and the counter want opposite things from a high-rate
// event. Once v2.3.7 named the legacy team clients, the cause was known and the
// per-refusal line stopped carrying new information, but it kept costing: at ~9
// refusals/second those lines were 5,326 of 5,551 journal entries over ten minutes
// on unbounded-us — 96% — which made journalctl useless for reading anything else.
// Three separate greps for unrelated startup lines came back empty because the
// signal was buried, which is how this was noticed.
//
// A minute is short enough that a changing peer set shows up promptly and long
// enough to cut roughly 540 lines to one. Deliberately per-reason rather than
// special-cased to the legacy clients: missing_subprotocols flooded exactly the same
// way before v2.3.5 reclassified it, so the next high-rate reason should not need
// this written again.
const refusalLogInterval = time.Minute

type refusalLogState struct {
	lastLogged time.Time
	suppressed int64
}

var (
	refusalLogMx sync.Mutex
	refusalLogs  = map[refusalReason]*refusalLogState{}
)

// shouldLogRefusal reports whether this refusal should be logged, and how many of
// the same reason went unlogged since the last one that was.
//
// The first occurrence of a reason always logs, so a condition that has never been
// seen is visible immediately rather than up to an interval later — which matters
// most for the reasons that are rare, since those are the ones nobody is watching a
// dashboard for.
//
// now is a parameter rather than read inside so the behavior is testable without
// sleeping.
func shouldLogRefusal(reason refusalReason, now time.Time) (shouldLog bool, suppressed int64) {
	refusalLogMx.Lock()
	defer refusalLogMx.Unlock()

	st, seen := refusalLogs[reason]
	if !seen {
		refusalLogs[reason] = &refusalLogState{lastLogged: now}
		return true, 0
	}
	if now.Sub(st.lastLogged) < refusalLogInterval {
		st.suppressed++
		return false, 0
	}

	// Report the backlog on the line that breaks the silence, so a single log entry
	// says how much it stands for. Without it a throttled line understates a flood by
	// three orders of magnitude and reads like an isolated event.
	suppressed = st.suppressed
	st.suppressed = 0
	st.lastLogged = now
	return true, suppressed
}

// classifySubprotocolRefusal decides which of the three subprotocol refusals
// applies, and whether the peer's values are safe to record.
//
// A function rather than inline branches in handleWebsocket so the safety property
// below is testable directly. The property is easy to state and was in fact broken
// on the first attempt: values may be logged *only* when the magic cookie appears
// nowhere in the list. Checking the leading position instead — the same test the
// parser uses — leaks a real consumer session ID from any client that merely
// misordered its tokens.
//
// raw is the unsplit header lines and parsed is the comma-split, whitespace-trimmed
// list. Absence is judged on raw because "Sec-WebSocket-Protocol: ," parses to zero
// values while plainly having been sent.
func classifySubprotocolRefusal(raw, parsed []string) (reason refusalReason, msg string, logValues bool) {
	switch {
	case len(raw) == 0:
		return refusedMissingSubprotocols, "Refused WebSocket connection, missing subprotocols", false
	case common.SubprotocolsContainMagicCookie(parsed):
		// Our protocol, wrong shape — a version skew, a hand-built list, or tokens in
		// the wrong order. The cookie appearing *anywhere* is what qualifies, not the
		// cookie leading: a misordered list is still our software getting it wrong,
		// and it can still carry a real consumer session ID. Checking only the leading
		// position here would classify that client as foreign and log its CSID, which
		// is the one value deliberately withheld.
		return refusedMalformedSubprotocols, "Refused WebSocket connection, malformed subprotocols", false
	case isLegacyTeamClient(parsed):
		// After the cookie check, not before. This is a subset of the foreign case and
		// cannot overlap the cookie case today — a lone "unbounded-team:..." token is
		// not the cookie, and a csid needs a second element — but the cookie check is
		// the privacy guard, so it wins unconditionally rather than by coincidence.
		// Ordering it first would make the guarantee depend on isLegacyTeamClient
		// staying narrow, which is not a property to leave to a future edit.
		return refusedLegacyTeamClient, "Refused WebSocket connection, legacy team client", true
	default:
		// Not our protocol. Recording the values is what identifies the caller, and it
		// is safe precisely because the cookie is absent: a peer showing no sign of
		// following the format cannot have supplied the session ID that format carries.
		return refusedForeignSubprotocols, "Refused WebSocket connection, foreign subprotocols", true
	}
}

// peerAttrs returns slog attributes identifying who was refused.
//
// RemoteAddr alone is useless here: Caddy terminates TLS on :443 and proxies to
// localhost:9001, so every RemoteAddr is 127.0.0.1 with an ephemeral port. The
// real client is in X-Forwarded-For, which Caddy sets. Both are logged because
// their disagreement is itself information — an X-Forwarded-For present with a
// non-loopback RemoteAddr would mean something is reaching 9001 without going
// through Caddy.
//
// User-Agent is the field that actually discriminates the hypotheses: one
// repeated UA points at a misdirected health check or monitor, many distinct ones
// point at real clients failing the handshake.
func peerAttrs(r *http.Request) []any {
	return []any{
		// RemoteAddr is synthesized by net/http from the accepted socket, not
		// taken from the request, so it needs no bounding.
		"remote_addr", r.RemoteAddr,
		"forwarded_for", truncateForLog(r.Header.Get("X-Forwarded-For")),
		"user_agent", truncateForLog(r.Header.Get("User-Agent")),
	}
}

// maxLoggedValueLen is the hard ceiling on the total bytes any single
// client-controlled value contributes to a log line, marker included. 256 bytes
// comfortably fits a real User-Agent and a long X-Forwarded-For proxy chain, so
// honest values pass through untouched.
const maxLoggedValueLen = 256

// truncationMarker is appended to a shortened value. Its length is reserved out
// of maxLoggedValueLen rather than added on top, so the documented ceiling is the
// actual ceiling — a cap that its own marker can exceed is not a cap.
const truncationMarker = "…(truncated)"

// truncateForLog bounds a client-controlled value before it reaches the log.
//
// Applies to anything the peer chooses: the X-Forwarded-For and User-Agent
// headers, and the protocol version lifted out of the subprotocol list. The
// version was missed on the first pass, which is the whole reason this is named
// for the sink rather than for headers.
//
// Both headers this is applied to are entirely attacker-chosen and effectively
// unbounded in size. Writing them verbatim on a path that is currently refusing
// ~10 connections per second turns a large header into sustained disk pressure on
// the egress host — a client sending a 100 KB User-Agent at that rate writes
// ~1 MB/s of DEBUG logs. This is the same hazard as letting an unbounded header
// become a metric label, just spent on disk instead of cardinality.
//
// Truncation is byte-based for speed, then ToValidUTF8 drops any rune left
// half-copied, so a multi-byte value cannot emit invalid UTF-8 into the log and
// break ingestion downstream. The marker matters: a silently shortened value
// would be indistinguishable from a genuinely short one, and the whole point of
// logging these is to identify the caller.
func truncateForLog(v string) string {
	// Validate even when no truncation is needed. A short value can already be
	// invalid UTF-8 (a lone 0xff, say), and this helper promises callers that
	// nothing invalid reaches slog — a promise the early return was quietly
	// exempting itself from.
	if len(v) <= maxLoggedValueLen {
		return strings.ToValidUTF8(v, "")
	}
	keep := maxLoggedValueLen - len(truncationMarker)
	return strings.ToValidUTF8(v[:keep], "") + truncationMarker
}
