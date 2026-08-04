package egress

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// refusedMalformedSubprotocols: the header was present but did not parse —
	// wrong element count, or the magic cookie did not match. Kept separate from
	// "missing" because the two implicate completely different callers: absent
	// means something that is not a broflake client at all, malformed means
	// something that tried and got the handshake wrong.
	refusedMalformedSubprotocols refusalReason = "malformed_subprotocols"
	refusedBadProtocolVersion    refusalReason = "bad_protocol_version"
	refusedMissingCSID           refusalReason = "missing_consumer_session_id"
)

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
