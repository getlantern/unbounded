package egress

import (
	"net/http"
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

// Refusal reasons. Values are metric label content, so they are snake_case and
// bounded: never derive one from request data, or the label becomes unbounded
// cardinality controlled by whoever is connecting.
const (
	refusedMissingSubprotocols = "missing_subprotocols"
	refusedBadProtocolVersion  = "bad_protocol_version"
	refusedMissingCSID         = "missing_consumer_session_id"
)

var (
	refusalsMx sync.Mutex
	// refusals is keyed by the constants above only. Entries are never removed;
	// the set is fixed at three, and a reason that stops occurring should keep
	// reporting its total rather than vanishing from the series.
	refusals = map[string]*int64{}
)

// recordRefusal counts one refused connection. Monotonic on purpose: this is a
// tally, not a gauge, so consumers can rate() it. Contrast ingress-bytes, which
// the otel callback drains each interval because it measures throughput.
func recordRefusal(reason string) {
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
func eachRefusal(f func(reason string, count int64)) {
	type row struct {
		reason string
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
		"remote_addr", r.RemoteAddr,
		"forwarded_for", r.Header.Get("X-Forwarded-For"),
		"user_agent", r.Header.Get("User-Agent"),
	}
}
