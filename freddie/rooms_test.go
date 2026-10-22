package freddie

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

const (
	pairingP = "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"
	pairingQ = "0b6f3a52-2d4e-4f7a-8c1d-9e0f1a2b3c4d"
	ticketOK = 15 * time.Minute
)

// roomKeys is a ticket signing key standing in for lantern-cloud's.
type roomKeys struct {
	t    *testing.T
	priv ed25519.PrivateKey
}

func newRoomKeys(t *testing.T) *roomKeys {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &roomKeys{t: t, priv: priv}
}

// ticket signs a ticket for pairing in role that expires after ttl; a negative ttl has expired.
func (k *roomKeys) ticket(pairing string, role helperticket.Role, ttl time.Duration) string {
	k.t.Helper()
	tk, err := helperticket.Sign(k.priv, helperticket.Ticket{PairingID: pairing, Role: role, Expires: time.Now().Add(ttl)})
	if err != nil {
		k.t.Fatal(err)
	}
	return tk
}

// startRoomServer is startTestServer with rooms verified against k's key.
func startRoomServer(t *testing.T, k *roomKeys) *testServer {
	t.Helper()
	s := startTestServer(t, headerHook)
	key := helperticket.NewKey(k.priv.Public().(ed25519.PublicKey))
	keys := func() []helperticket.Key { return []helperticket.Key{key} }
	s.f.RoomTicket = helperticket.RoomFunc(keys, time.Now)
	s.f.KnownKeyID = helperticket.KnownKeyFunc(keys)
	return s
}

// roomGenesis posts a genesis message carrying volunteer tickets, with pool=0 unless pool, and
// target countries.
func (s *testServer) roomGenesis(tickets []string, pool bool, targets ...string) {
	form := url.Values{}
	for _, tk := range tickets {
		form.Add(common.RoomTicketField, tk)
	}
	if !pool {
		form.Set(common.PoolField, "0")
	}
	for _, c := range targets {
		form.Add(common.TargetCountryField, c)
	}
	s.genesis(form)
}

// expectFrom is expect for a message that must arrive, through a room (room) or the pool (!room).
func (s *testServer) expectFrom(name string, ch chan string, room bool) {
	s.t.Helper()
	select {
	case raw := <-ch:
		if got := common.IsRoomGenesis([]byte(raw)); got != room {
			s.t.Errorf("%s consumer: room=%v, want %v", name, got, room)
		}
	case <-time.After(300 * time.Millisecond):
		s.t.Errorf("%s consumer did not receive a genesis message", name)
	}
}

// Room, then country, then everyone, per genesis message, for a volunteer that serves the pool.
func TestHandleSignal_RoomPrecedence(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	ir := s.subscribe("IR", "")
	other := s.subscribe("", "")
	s.waitForConsumers(3)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	vQ := k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)

	// The consumer in the volunteer's room takes it, even over a country match.
	s.roomGenesis([]string{vP}, true, "IR")
	s.expectFrom("paired", paired, true)
	s.expect("IR", ir, false)
	s.expect("other", other, false)

	// Nobody waits in room Q, so country targeting applies.
	s.roomGenesis([]string{vQ}, true, "IR")
	s.expectFrom("IR", ir, false)
	s.expect("paired", paired, false)
	s.expect("other", other, false)

	// Nor in the target country, so everyone gets the plain message.
	s.roomGenesis([]string{vQ}, true, "CN")
	s.expectFrom("paired", paired, false)
	s.expectFrom("IR", ir, false)
	s.expectFrom("other", other, false)
}

// pool=0: the room or nobody.
func TestHandleSignal_PoolOffHasNoFallback(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	paired := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	pool := s.subscribe("IR", "")
	s.waitForConsumers(2)

	s.roomGenesis([]string{k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, false, "IR")
	s.expect("paired", paired, false)
	s.expect("pool", pool, false)

	s.roomGenesis(nil, false)
	s.expect("paired", paired, false)
	s.expect("pool", pool, false)

	s.roomGenesis([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, false)
	s.expectFrom("paired", paired, true)
	s.expect("pool", pool, false)
}

// Invalid, expired, wrong-role and foreign tickets are ignored on both sides, without spoiling
// valid ones beside them.
func TestHandleSignal_BadRoomTicketsIgnored(t *testing.T) {
	k := newRoomKeys(t)
	stranger := newRoomKeys(t)
	s := startRoomServer(t, k)
	good := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	valid := s.subscribe("", "", good)
	bad := map[string]chan string{
		"expired":    s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, -time.Minute)),
		"wrong role": s.subscribe("", "", k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)),
		"wrong key":  s.subscribe("", "", stranger.ticket(pairingP, helperticket.RoleInviter, ticketOK)),
		"garbage":    s.subscribe("", "", "not-a-ticket", "f"+good[1:]),
	}
	s.waitForConsumers(5)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)

	s.roomGenesis([]string{vP}, false)
	s.expectFrom("valid", valid, true)
	for name, ch := range bad {
		s.expect(name, ch, false)
	}

	// A volunteer's own bad tickets admit it to no room, so with pool=0 nobody gets its genesis.
	for name, tk := range map[string]string{
		"expired":    k.ticket(pairingP, helperticket.RoleVolunteer, -time.Minute),
		"wrong role": k.ticket(pairingP, helperticket.RoleInviter, ticketOK),
		"wrong key":  stranger.ticket(pairingP, helperticket.RoleVolunteer, ticketOK),
	} {
		s.roomGenesis([]string{tk}, false)
		s.expect("valid, volunteer ticket "+name, valid, false)
	}

	s.roomGenesis([]string{"junk", k.ticket(pairingP, helperticket.RoleVolunteer, -time.Minute), vP}, false)
	s.expectFrom("valid, beside bad tickets", valid, true)
}

// Without a RoomTicket hook tickets are ignored: everyone is in the pool, and a pool=0 producer
// reaches nobody.
func TestHandleSignal_NoRoomHook(t *testing.T) {
	k := newRoomKeys(t)
	s := startTestServer(t, headerHook)
	paired := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	s.roomGenesis([]string{vP}, false)
	s.expect("paired", paired, false)
	s.roomGenesis([]string{vP}, true)
	s.expectFrom("paired", paired, false)
}

func TestHandleSignal_CORSAllowsRoomTickets(t *testing.T) {
	s := startTestServer(t, nil)
	req, err := http.NewRequest(http.MethodOptions, s.url+"/v1/signal", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, common.RoomTicketsHeader) {
		t.Errorf("Access-Control-Allow-Headers %q lacks %s", got, common.RoomTicketsHeader)
	}
}

// No ticket or pairing ID reaches a log, a span or a metric, and the one room metric carries only
// a boolean.
func TestHandleSignal_RoomsLeaveNoTrace(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	prevTP, prevMP, prevLog := otel.GetTracerProvider(), otel.GetMeterProvider(), slog.Default()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
		slog.SetDefault(prevLog)
	})

	k := newRoomKeys(t)
	s := startRoomServer(t, k) // after the providers, so New picks them up
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	expired := k.ticket(pairingP, helperticket.RoleInviter, -time.Minute)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	volQ := k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)
	paired := s.subscribe("", "", inv, expired)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expectFrom("paired", paired, true)
	s.roomGenesis([]string{volQ, "junk"}, true)
	s.expectFrom("paired", paired, false)
	s.hangUp()
	// Each genesis POST waits defaultMsgTTL for an offer nobody sends here, and its span ends
	// only then.
	time.Sleep((defaultMsgTTL + 1) * time.Second)

	secrets := []string{pairingP, pairingQ, inv, expired, vol, volQ}
	check := func(where, text string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains a ticket or pairing ID: %q", where, text)
			}
		}
	}
	ended := spans.Ended()
	if len(ended) == 0 {
		t.Fatal("no spans recorded")
	}
	for _, sp := range ended {
		for _, kv := range sp.Attributes() {
			check("span "+sp.Name()+" attribute "+string(kv.Key), kv.Value.Emit())
		}
		for _, ev := range sp.Events() {
			check("span "+sp.Name()+" event", ev.Name)
			for _, kv := range ev.Attributes {
				check("span "+sp.Name()+" event attribute "+string(kv.Key), kv.Value.Emit())
			}
		}
		check("span "+sp.Name()+" status", sp.Status().Description)
	}
	check("log", logs.String())

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	roomMatches := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					check("metric "+m.Name+" attribute "+string(kv.Key), kv.Value.Emit())
				}
				if m.Name == "freddie.genesis.room_match" {
					roomMatches += int(dp.Value)
					if attrs := dp.Attributes.ToSlice(); len(attrs) != 1 || attrs[0].Key != "matched" {
						t.Errorf("room_match carries %v, want only matched", attrs)
					}
				}
			}
		}
	}
	if roomMatches != 2 {
		t.Errorf("room_match counted %d genesis messages, want 2 (one matched, one not)", roomMatches)
	}
}

// A keyring is a trusted key set that tests can revoke keys from, standing in for Freddie's
// fetched key list.
type keyring struct {
	keys atomic.Pointer[[]helperticket.Key]
}

func newKeyring(ks ...*roomKeys) *keyring {
	r := &keyring{}
	var keys []helperticket.Key
	for _, k := range ks {
		keys = append(keys, helperticket.NewKey(k.priv.Public().(ed25519.PublicKey)))
	}
	r.keys.Store(&keys)
	return r
}

func (r *keyring) get() []helperticket.Key  { return *r.keys.Load() }
func (r *keyring) revokeAll()               { r.keys.Store(&[]helperticket.Key{}) }
func (r *keyring) known() func(string) bool { return helperticket.KnownKeyFunc(r.get) }

// countingHook is the keyring's RoomFunc, counting the verifications it does.
func (r *keyring) countingHook(calls *atomic.Int32) func(string, helperticket.Role) (string, bool) {
	verify := helperticket.RoomFunc(r.get, time.Now)
	return func(tk string, role helperticket.Role) (string, bool) {
		calls.Add(1)
		return verify(tk, role)
	}
}

// countingHook is k's RoomFunc, counting the verifications it does, and k's KnownKeyID.
func countingHook(k *roomKeys, calls *atomic.Int32) (func(string, helperticket.Role) (string, bool), func(string) bool) {
	r := newKeyring(k)
	return r.countingHook(calls), r.known()
}

// fakeClock is a settable time for the ticket cache.
type fakeClock struct{ t atomic.Pointer[time.Time] }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.set(time.Now())
	return c
}

func (c *fakeClock) set(t time.Time)     { c.t.Store(&t) }
func (c *fakeClock) add(d time.Duration) { c.set(c.now().Add(d)) }
func (c *fakeClock) now() time.Time      { return *c.t.Load() }

// A verified ticket is verified once until it expires; a wrong role is refused from the cache;
// failures are never cached.
func TestTicketCache_Hits(t *testing.T) {
	k := newRoomKeys(t)
	var calls atomic.Int32
	verify, known := countingHook(k, &calls)
	clock := newFakeClock()
	c := newTicketCache(10, clock.now)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)

	for i := 0; i < 5; i++ {
		if room, ok := c.room(inv, helperticket.RoleInviter, verify, known); !ok || room != pairingP {
			t.Fatalf("lookup %d: room=%q ok=%v", i, room, ok)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("verified %d times, want 1", n)
	}
	if _, ok := c.room(inv, helperticket.RoleVolunteer, verify, known); ok {
		t.Error("a cached inviter ticket admitted a volunteer")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("a wrong role on a cached ticket was re-verified (%d calls)", n)
	}

	for i := 0; i < 3; i++ {
		if _, ok := c.room("not-a-ticket", helperticket.RoleInviter, verify, known); ok {
			t.Fatal("garbage admitted")
		}
		if _, ok := c.room(k.ticket(pairingQ, helperticket.RoleInviter, -time.Minute), helperticket.RoleInviter, verify, known); ok {
			t.Fatal("an expired ticket admitted")
		}
	}
	if n := calls.Load(); n != 7 {
		t.Errorf("verified %d times, want 7: failures must not be cached", n)
	}
	if len(c.entries) != 1 {
		t.Errorf("%d entries cached, want only the valid ticket", len(c.entries))
	}
}

// A cached ticket is refused once its own expiry has passed, without asking the verifier again.
func TestTicketCache_Expiry(t *testing.T) {
	k := newRoomKeys(t)
	clock := newFakeClock()
	c := newTicketCache(10, clock.now)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	// The verifier accepts anything, so only the cache's own expiry check can refuse it.
	var calls atomic.Int32
	lenient := func(string, helperticket.Role) (string, bool) { calls.Add(1); return pairingP, true }
	known := newKeyring(k).known()

	if _, ok := c.room(inv, helperticket.RoleInviter, lenient, known); !ok {
		t.Fatal("not admitted")
	}
	clock.add(ticketOK - 2*time.Second)
	if _, ok := c.room(inv, helperticket.RoleInviter, lenient, known); !ok || calls.Load() != 1 {
		t.Fatalf("before expiry: ok=%v calls=%d, want a cache hit", ok, calls.Load())
	}
	clock.add(3 * time.Second)
	if _, ok := c.room(inv, helperticket.RoleInviter, func(string, helperticket.Role) (string, bool) { return "", false }, known); ok {
		t.Error("a cached ticket was admitted at its expiry")
	}
	if _, ok := c.entries[ticketCacheKey(inv)]; ok {
		t.Error("an expired entry was not dropped on lookup")
	}
}

// The cache holds at most max entries. When full it sweeps out expired ones and, failing that,
// leaves new tickets uncached rather than evicting good ones; a periodic sweep drops expired
// entries nobody looks up again.
func TestTicketCache_Bound(t *testing.T) {
	k := newRoomKeys(t)
	var calls atomic.Int32
	verify, known := countingHook(k, &calls)
	clock := newFakeClock()
	c := newTicketCache(2, clock.now)

	short := k.ticket(pairingP, helperticket.RoleInviter, 2*time.Minute)
	long := k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)
	extra := k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)
	for _, tk := range []string{short, long} {
		c.room(tk, helperticket.RoleInviter, verify, known)
	}
	if _, ok := c.room(extra, helperticket.RoleVolunteer, verify, known); !ok {
		t.Fatal("a ticket was refused because the cache is full")
	}
	if len(c.entries) != 2 {
		t.Fatalf("%d entries, want the bound of 2", len(c.entries))
	}
	calls.Store(0)
	c.room(short, helperticket.RoleInviter, verify, known)
	c.room(long, helperticket.RoleInviter, verify, known)
	if n := calls.Load(); n != 0 {
		t.Errorf("a full cache evicted good entries (%d re-verifications)", n)
	}
	c.room(extra, helperticket.RoleVolunteer, verify, known)
	if n := calls.Load(); n != 1 {
		t.Errorf("extra was verified %d times on its second lookup, want 1: a full cache must leave it uncached", n)
	}

	// Once short expires, a full cache makes room by sweeping it out.
	clock.add(3 * time.Minute)
	fresh := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	c.room(fresh, helperticket.RoleVolunteer, verify, known)
	if _, ok := c.entries[ticketCacheKey(fresh)]; !ok || len(c.entries) != 2 {
		t.Errorf("after short expired: fresh cached=%v, %d entries; want it cached in short's place", ok, len(c.entries))
	}

	// The periodic sweep drops expired entries even when the cache isn't full. Tickets expire by
	// real time, so the clock, moved on above, comes back to it first.
	clock.set(time.Now())
	c = newTicketCache(100, clock.now)
	soon := k.ticket(pairingP, helperticket.RoleInviter, time.Minute)
	later := k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)
	c.room(soon, helperticket.RoleInviter, verify, known)
	if _, ok := c.entries[ticketCacheKey(soon)]; !ok {
		t.Fatal("the short-lived ticket was not cached")
	}
	clock.add(ticketCacheSweep + time.Minute)
	c.room(later, helperticket.RoleInviter, verify, known)
	if _, ok := c.entries[ticketCacheKey(soon)]; ok {
		t.Error("the periodic sweep left an expired entry in a cache that isn't full")
	}
	if _, ok := c.entries[ticketCacheKey(later)]; !ok || len(c.entries) != 1 {
		t.Errorf("later cached=%v, %d entries; want only later", ok, len(c.entries))
	}
}

// A full cache of live entries is swept at most once per ticketCacheFullSweep, however many new
// tickets miss it, so a flood of misses can't hold the lock for a full sweep each.
func TestTicketCache_FullSweepRateLimited(t *testing.T) {
	k := newRoomKeys(t)
	var calls atomic.Int32
	verify, known := countingHook(k, &calls)
	clock := newFakeClock()
	c := newTicketCache(2, clock.now)
	c.room(k.ticket(pairingP, helperticket.RoleInviter, ticketOK), helperticket.RoleInviter, verify, known)
	c.room(k.ticket(pairingQ, helperticket.RoleInviter, ticketOK), helperticket.RoleInviter, verify, known)

	clock.add(ticketCacheFullSweep + time.Millisecond)
	miss := func() {
		t.Helper()
		if _, ok := c.room(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK+time.Duration(calls.Load())*time.Second),
			helperticket.RoleVolunteer, verify, known); !ok {
			t.Fatal("a ticket was refused because the cache is full")
		}
	}
	miss()
	miss()
	if c.sweeps != 1 {
		t.Errorf("two misses within the floor swept %d times, want 1", c.sweeps)
	}
	clock.add(ticketCacheFullSweep)
	miss()
	if c.sweeps != 2 {
		t.Errorf("a miss after the floor: %d sweeps, want 2", c.sweeps)
	}
	if len(c.entries) != 2 {
		t.Errorf("%d entries, want the bound of 2", len(c.entries))
	}
}

// A consumer that re-subscribes with the same tickets, as it does every consumerTTL, is not
// re-verified.
func TestHandleSignal_TicketsVerifiedOnce(t *testing.T) {
	k := newRoomKeys(t)
	var calls atomic.Int32
	s := startTestServer(t, headerHook)
	s.f.RoomTicket, s.f.KnownKeyID = countingHook(k, &calls)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)

	first := s.subscribe("", "", inv)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expectFrom("first", first, true)
	s.hangUp()

	s.ctx, s.cancel = context.WithCancel(context.Background())
	t.Cleanup(s.cancel)
	second := s.subscribe("", "", inv)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expectFrom("second", second, true)
	if n := calls.Load(); n != 2 {
		t.Errorf("verified %d times, want 2 (each ticket once)", n)
	}
}

// Without a RoomTicket hook freddie counts its requests as served with rooms disabled, so a
// deploy that lacks the verifier shows up; with one, it counts nothing.
func TestHandleSignal_RoomsDisabledMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	disabled := func() int64 {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "freddie.rooms.disabled" {
					continue
				}
				for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
					if dp.Attributes.Len() != 0 {
						t.Errorf("rooms.disabled carries attributes %v", dp.Attributes.ToSlice())
					}
					n += dp.Value
				}
			}
		}
		return n
	}

	// Subtests, so that each server and its handlers are gone before the next starts.
	k := newRoomKeys(t)
	t.Run("no hook", func(t *testing.T) {
		s := startTestServer(t, headerHook)
		s.roomGenesis(nil, true)
		s.roomGenesis([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, false)
		if n := disabled(); n != 2 {
			t.Errorf("without a hook rooms.disabled = %d, want 2", n)
		}
	})
	t.Run("hook", func(t *testing.T) {
		s := startRoomServer(t, k)
		s.roomGenesis([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, false)
		if n := disabled(); n != 2 {
			t.Errorf("with a hook rooms.disabled rose to %d, want it to stay 2", n)
		}
	})
}

// A cached ticket whose key has left the trusted set is refused at once, without re-verifying it,
// and its entry is dropped; presented again, it is verified afresh against the current keys.
func TestTicketCache_KeyRevoked(t *testing.T) {
	k := newRoomKeys(t)
	keys := newKeyring(k)
	var calls atomic.Int32
	verify, known := keys.countingHook(&calls), keys.known()
	c := newTicketCache(10, newFakeClock().now)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)

	if _, ok := c.room(inv, helperticket.RoleInviter, verify, known); !ok {
		t.Fatal("not admitted")
	}
	keys.revokeAll()
	if _, ok := c.room(inv, helperticket.RoleInviter, verify, known); ok {
		t.Error("a cached ticket was admitted after its key was revoked")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("verified %d times, want 1: a revoked key must be caught from the cache", n)
	}
	if len(c.entries) != 0 {
		t.Error("the revoked ticket's entry was not dropped")
	}
	if _, ok := c.room(inv, helperticket.RoleInviter, verify, known); ok || calls.Load() != 2 {
		t.Errorf("presented again: ok=%v after %d verifications, want a fresh, failing one", ok, calls.Load())
	}
	if len(c.entries) != 0 {
		t.Error("a ticket was cached under an untrusted key")
	}
}

// Without KnownKeyID nothing is cached, since a revoked key could not be noticed.
func TestTicketCache_NoKnownKeyNoCache(t *testing.T) {
	k := newRoomKeys(t)
	var calls atomic.Int32
	verify, _ := countingHook(k, &calls)
	c := newTicketCache(10, newFakeClock().now)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	for i := 0; i < 3; i++ {
		if _, ok := c.room(inv, helperticket.RoleInviter, verify, nil); !ok {
			t.Fatal("not admitted")
		}
	}
	if n := calls.Load(); n != 3 || len(c.entries) != 0 {
		t.Errorf("%d verifications, %d entries; want 3 and none", n, len(c.entries))
	}
}

// End to end: once a cached ticket's key is revoked, the next subscribe with it joins no room,
// though the verifier itself (lenient here) would still accept it.
func TestHandleSignal_RevokedKeyDropsCachedTickets(t *testing.T) {
	k := newRoomKeys(t)
	keys := newKeyring(k)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	var invCalls atomic.Int32
	s := startTestServer(t, headerHook)
	s.f.RoomTicket = func(tk string, role helperticket.Role) (string, bool) {
		if tk == inv {
			invCalls.Add(1)
		}
		return pairingP, true
	}
	s.f.KnownKeyID = keys.known()

	first := s.subscribe("", "", inv)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expectFrom("before revocation", first, true)
	s.hangUp()

	keys.revokeAll()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	t.Cleanup(s.cancel)
	second := s.subscribe("", "", inv)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expect("after revocation", second, false)
	if n := invCalls.Load(); n != 1 {
		t.Errorf("the inviter ticket was verified %d times, want 1: revocation must be caught from the cache", n)
	}
}

// FlushRoomTicketCache forgets every cached ticket, so the next presentation is verified afresh.
func TestFreddie_FlushRoomTicketCache(t *testing.T) {
	k := newRoomKeys(t)
	f, err := New(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	f.RoomTicket, f.KnownKeyID = countingHook(k, &calls)
	inv := []string{k.ticket(pairingP, helperticket.RoleInviter, ticketOK)}

	f.verifyRoomTickets(inv, helperticket.RoleInviter)
	f.verifyRoomTickets(inv, helperticket.RoleInviter)
	if n := calls.Load(); n != 1 {
		t.Fatalf("verified %d times before the flush, want 1", n)
	}
	f.FlushRoomTicketCache()
	if rooms := f.verifyRoomTickets(inv, helperticket.RoleInviter); len(rooms) != 1 || rooms[0] != pairingP {
		t.Errorf("after the flush: rooms %v", rooms)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("verified %d times after the flush, want 2", n)
	}
}
