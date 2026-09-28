package egress

import (
	"context"
	"log/slog"
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
// The ID itself goes nowhere but the activation store: not into
// attributes, not into spans, not into logs.

// activationStore remembers which activation IDs are active. It is the
// seam for moving that memory off the egress, to a store shared by
// every egress: the rest of the package only asks whether an ID is
// new.
//
// first runs on the serving path, after createOrMigrate and before the
// stream accept loop starts, so a networked implementation must bound
// its own latency.
type activationStore interface {
	// first marks id as active now and reports whether it was not already active.
	first(ctx context.Context, id string) (bool, error)
}

// activations counts proxy.activations, asking store which IDs are new.
type activations struct {
	store activationStore
}

func newActivations(store activationStore) *activations {
	return &activations{store: store}
}

// record counts id as an activation for a donor in donorCC if it has
// not been counted while active. A nil receiver, as in tests that
// drive handleWebsocket without one, and an empty ID, from a widget
// that does not send one, record nothing.
//
// A store error skips the count. Undercounting during an outage is the
// only failure mode that neither blocks a donor nor counts one
// activation twice.
func (a *activations) record(ctx context.Context, id, donorCC string) {
	if a == nil || id == "" {
		return
	}
	fresh, err := a.store.first(ctx, id)
	if err != nil {
		slog.Warn("activation store failed; not counting", "error", err)
		return
	}
	if fresh {
		recordActivation(donorCC)
	}
}

const (
	activationIdleTTL       = 24 * time.Hour // forget an ID idle this long
	maxActivations          = 1 << 18        // IDs are client-supplied
	activationSweepInterval = time.Minute    // between expiry sweeps
)

// memoryActivations is an activationStore local to one egress process.
// A restart forgets every ID, and a second egress keeps its own.
type memoryActivations struct {
	mu        sync.Mutex
	lastSeen  map[string]time.Time
	lastSweep time.Time
	now       func() time.Time
}

func newMemoryActivations() *memoryActivations {
	return &memoryActivations{lastSeen: map[string]time.Time{}, now: time.Now}
}

// first treats an ID idle for longer than activationIdleTTL as new.
// When the store is full, a new ID is neither stored nor reported new,
// so a flood of IDs cannot inflate the count by evicting real ones.
func (m *memoryActivations) first(_ context.Context, id string) (bool, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if now.Sub(m.lastSweep) >= activationSweepInterval {
		for k, t := range m.lastSeen {
			if now.Sub(t) > activationIdleTTL {
				delete(m.lastSeen, k)
			}
		}
		m.lastSweep = now
	}

	t, ok := m.lastSeen[id]
	fresh := !ok || now.Sub(t) > activationIdleTTL
	if !ok && len(m.lastSeen) >= maxActivations {
		return false, nil
	}
	m.lastSeen[id] = now
	return fresh, nil
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
