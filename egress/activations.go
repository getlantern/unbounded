package egress

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/semconv"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// proxy.activations counts times a widget turned proxy mode on and
// then proxied traffic for somebody. The widget mints an activation ID
// when proxy mode turns on and sends it on every egress WebSocket until
// it turns off (see common.NewActivationID); the egress counts the
// first WebSocket per ID on which createOrMigrate succeeds.
//
// createOrMigrate succeeding is the evidence of proxying. For a new
// connection, the consumer's QUIC handshake has completed through this
// donor; for a migration, the path probe and switch through it have.
// Either way the consumer's packets crossed the donor.
//
// WebSockets are the wrong unit. A widget holds several consumer slots
// and opens a WebSocket each time a consumer joins, leaves, or
// migrates, so one afternoon with the widget on is dozens of
// WebSockets and one activation.
//
// The ID itself goes nowhere but the seen-set: not into attributes,
// not into spans, not into logs.

// activationIdleTTL is how long the egress remembers an ID after its
// last successful WebSocket. A widget left on keeps refreshing its
// entry as consumers churn, so this only has to outlast a quiet
// stretch with no consumers. Too short re-counts a widget left on
// overnight; the cost of too long is only memory, bounded by
// maxActivations. The indicator is quarterly, so a day is well inside
// its resolution.
const activationIdleTTL = 24 * time.Hour

// maxActivations bounds the seen-set, because IDs are client-supplied.
// Each entry costs one successful QUIC handshake through the egress,
// which bounds the insertion rate but not the total. Far above any
// realistic number of widgets on at once.
const maxActivations = 1 << 18

// activationSweepInterval is how often an insert may pay for an
// expiry sweep of the whole set.
const activationSweepInterval = time.Minute

// activationSet remembers recently active activation IDs.
type activationSet struct {
	mu        sync.Mutex
	lastSeen  map[string]time.Time
	lastSweep time.Time
	now       func() time.Time
}

func newActivationSet() *activationSet {
	return &activationSet{lastSeen: map[string]time.Time{}, now: time.Now}
}

// first marks id as active now and reports whether it was not already
// active: never seen, or idle for longer than activationIdleTTL. When
// the set is full, a new ID is neither stored nor counted, so a flood
// of IDs cannot inflate the count by evicting real ones.
func (s *activationSet) first(id string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if now.Sub(s.lastSweep) >= activationSweepInterval {
		for k, t := range s.lastSeen {
			if now.Sub(t) > activationIdleTTL {
				delete(s.lastSeen, k)
			}
		}
		s.lastSweep = now
	}

	t, ok := s.lastSeen[id]
	fresh := !ok || now.Sub(t) > activationIdleTTL
	if !ok && len(s.lastSeen) >= maxActivations {
		return false
	}
	s.lastSeen[id] = now
	return fresh
}

// record counts id as an activation for a donor in donorCC if it has
// not been counted while active. A nil set, as in tests that drive
// handleWebsocket without one, and an empty ID, from a widget that does
// not send one, record nothing.
func (s *activationSet) record(id, donorCC string) {
	if s == nil || id == "" {
		return
	}
	if s.first(id) {
		recordActivation(donorCC)
	}
}

// proxyActivationHandle wraps the counter interface so atomic.Pointer
// has a single concrete type to hold, for the reasons proxyIOHandle
// gives.
type proxyActivationHandle struct{ c metric.Int64Counter }

// proxyActivationCounter is installed by initMetrics. nil until then,
// and for tests that do not stand up a provider; recordActivation drops
// measurements while nil, the contract addProxyIO follows.
var proxyActivationCounter atomic.Pointer[proxyActivationHandle]

// recordActivation increments proxy.activations for a donor in donorCC.
//
// The spellings are load-bearing the same way proxyIOSetsFor's are:
// downstream storage files these under columns named after the keys,
// so a wrong one files the count under NULL instead of failing
// anywhere visible.
func recordActivation(donorCC string) {
	h := proxyActivationCounter.Load()
	if h == nil {
		return
	}
	h.c.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(
		semconv.ProxyProtocolKey.String(proxyProtocol),
		semconv.GeoCountryISOCodeKey.String(donorCC),
	)))
}
