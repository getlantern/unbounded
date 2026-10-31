package clientcore

import (
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/getlantern/broflake/common"
)

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

// STUN preflight outcomes; see common's Preflight* for what each means.
const (
	STUNPreflightOK        = common.PreflightOK
	STUNPreflightNoReply   = common.PreflightNoReply
	STUNPreflightNoSocket  = common.PreflightNoSocket
	STUNPreflightNoServers = common.PreflightNoServers
)

const (
	// stunPreflightTimeout bounds one preflight. Requests are sent again halfway through.
	stunPreflightTimeout = 3 * time.Second
	// stunPreflightValidFor is how long a passed preflight vouches for the cohort it checked. A new
	// cohort, or an attempt that gathers no server reflexive candidate, ends it early.
	stunPreflightValidFor   = 10 * time.Minute
	stunPreflightMinBackoff = 5 * time.Second
	stunPreflightMaxBackoff = 5 * time.Minute
)

// preflightNow is the producer's clock for preflight validity, replaceable in tests.
var preflightNow = time.Now

// preflightState is which STUN cohort a producer worker last confirmed answers, and when, and how
// long it waits after a failed preflight. A producer only learns that STUN failed after it has
// taken a consumer's offer, so without a preflight a volunteer whose STUN never works spends a
// consumer's attempt on every try, retrying as fast as gathering times out.
type preflightState struct {
	cohort     []string
	verifiedAt time.Time
	backoff    time.Duration
}

func (p *preflightState) due(now time.Time, cohort []string) bool {
	return p.verifiedAt.IsZero() || now.Sub(p.verifiedAt) > stunPreflightValidFor ||
		!slices.Equal(p.cohort, cohort)
}

func (p *preflightState) passed(now time.Time, cohort []string) {
	p.cohort = slices.Clone(cohort)
	p.verifiedAt = now
	p.backoff = 0
}

// expired reports whether a pass has run out. Unlike due, it's false for a worker that hasn't
// passed yet, which state 0 handles.
func (p *preflightState) expired(now time.Time) bool {
	return !p.verifiedAt.IsZero() && now.Sub(p.verifiedAt) > stunPreflightValidFor
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

// preflightReport is what a producer worker tells Freddie about its preflights on the genesis that
// follows the next one to pass: a worker that fails doesn't send genesis, so failures wait for it.
type preflightReport struct {
	// passed is set when a preflight has passed since the last report.
	passed      bool
	passVPN     string
	failures    int
	lastFailure string
	failureVPN  string
}

func (r *preflightReport) record(outcome, vpn string) {
	if outcome == STUNPreflightOK {
		r.passed = true
		r.passVPN = vpn
		return
	}
	r.failures++
	r.lastFailure = outcome
	r.failureVPN = vpn
}

// addTo puts the report on a genesis form once a preflight has passed. It stays pending until
// delivered, since the first genesis after a run of failures is the one most likely to fail too.
func (r *preflightReport) addTo(form url.Values) {
	if !r.passed {
		return
	}
	form.Set(common.PreflightVPNField, r.passVPN)
	if r.failures > 0 {
		form.Set(common.PrevPreflightFailuresField, strconv.Itoa(r.failures))
		form.Set(common.PrevPreflightOutcomeField, r.lastFailure)
		form.Set(common.PrevPreflightVPNField, r.failureVPN)
	}
}

// delivered clears the report once Freddie has accepted a genesis that carried it.
func (r *preflightReport) delivered() {
	*r = preflightReport{}
}

func vpnStatus(options *WebRTCOptions) string {
	if options.VPNStatus == nil {
		return ""
	}
	return options.VPNStatus()
}
