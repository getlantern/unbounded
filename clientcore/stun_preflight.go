package clientcore

import "time"

// STUNPreflightResult is how a producer's STUN preflight went, reported to
// WebRTCOptions.OnSTUNPreflight.
type STUNPreflightResult struct {
	// Outcome is one of the STUNPreflight* values.
	Outcome string
	// Servers is how many STUN servers the preflight asked.
	Servers int
	// Elapsed is how long the preflight took.
	Elapsed time.Duration
}

const (
	// STUNPreflightOK: a STUN server answered with our mapped address.
	STUNPreflightOK = "ok"
	// STUNPreflightNoReply: requests went out, but no server answered before the timeout. Seen when
	// something on the path drops UDP, such as a firewall or a tunnel that doesn't carry it.
	STUNPreflightNoReply = "no_reply"
	// STUNPreflightNoSocket: no UDP socket could be opened, or no request could be sent on it.
	STUNPreflightNoSocket = "no_socket"
	// STUNPreflightNoServers: no server in the cohort could be parsed or resolved.
	STUNPreflightNoServers = "no_servers"
)

const (
	// stunPreflightTimeout bounds one preflight. Requests are sent again halfway through.
	stunPreflightTimeout = 3 * time.Second
	// stunPreflightValidFor is how long a passed preflight vouches for a worker's STUN servers. An
	// attempt that gathers no server reflexive candidate ends it early.
	stunPreflightValidFor   = 10 * time.Minute
	stunPreflightMinBackoff = 5 * time.Second
	stunPreflightMaxBackoff = 5 * time.Minute
)

// preflightState is when a producer worker last confirmed that its STUN servers answer, and how
// long it waits after a failed preflight. A producer only learns that STUN failed after it has
// taken a consumer's offer, so without a preflight a volunteer whose STUN never works spends a
// consumer's attempt on every try, retrying as fast as gathering times out.
type preflightState struct {
	verifiedAt time.Time
	backoff    time.Duration
}

func (p *preflightState) due(now time.Time) bool {
	return p.verifiedAt.IsZero() || now.Sub(p.verifiedAt) > stunPreflightValidFor
}

func (p *preflightState) passed(now time.Time) {
	p.verifiedAt = now
	p.backoff = 0
}

// failed returns how long to wait before the next preflight, doubling with each failure in a row.
func (p *preflightState) failed() time.Duration {
	p.verifiedAt = time.Time{}
	p.backoff = min(max(2*p.backoff, stunPreflightMinBackoff), stunPreflightMaxBackoff)
	return p.backoff
}

// invalidate makes the next attempt run a preflight.
func (p *preflightState) invalidate() {
	p.verifiedAt = time.Time{}
}
