package freddie

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

// received reports whether ch has a message waiting, without blocking.
func received(ch chan string) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Tests on the index itself use a buffered table so a send succeeds without a parked receiver,
// which makes delivery deterministic.
func TestConsumerIndex_BroadcastGenesis(t *testing.T) {
	idx := newConsumerIndex(1)
	ir := idx.AddWithCountry("ir", "IR")
	ru := idx.AddWithCountry("ru", "RU")
	anon := idx.AddWithCountry("anon", "")

	if got := idx.BroadcastGenesis("m", []string{"IR"}); got != genesisMatched {
		t.Fatalf("targeted IR: got %q, want %q", got, genesisMatched)
	}
	if !received(ir) {
		t.Error("IR consumer did not receive a genesis message targeted at IR")
	}
	if received(ru) || received(anon) {
		t.Error("a genesis message targeted at IR reached a non-IR consumer despite a match")
	}

	if got := idx.BroadcastGenesis("m", []string{"CN"}); got != genesisFallback {
		t.Fatalf("targeted CN: got %q, want %q", got, genesisFallback)
	}
	if !received(ir) || !received(ru) || !received(anon) {
		t.Error("fallback broadcast did not reach every consumer")
	}

	if got := idx.BroadcastGenesis("m", nil); got != genesisUntargeted {
		t.Fatalf("untargeted: got %q, want %q", got, genesisUntargeted)
	}
	if !received(ir) || !received(ru) || !received(anon) {
		t.Error("untargeted broadcast did not reach every consumer")
	}

	if got := idx.BroadcastGenesis("m", []string{"CN", "RU"}); got != genesisMatched {
		t.Fatalf("targeted CN,RU: got %q, want %q", got, genesisMatched)
	}
	if !received(ru) || received(ir) || received(anon) {
		t.Error("multi-country targeting should reach only the RU consumer")
	}
}

// A matching consumer that can't take the message right now (unbuffered, not parked) must not
// count as a match, or the producer would sit idle waiting on it.
func TestConsumerIndex_UndeliverableMatchFallsBack(t *testing.T) {
	idx := newConsumerIndex(1)
	ir := idx.AddWithCountry("ir", "IR")
	ru := idx.AddWithCountry("ru", "RU")
	ir <- "already full"

	if got := idx.BroadcastGenesis("m", []string{"IR"}); got != genesisFallback {
		t.Fatalf("got %q, want %q", got, genesisFallback)
	}
	if !received(ru) {
		t.Error("fallback did not reach the RU consumer")
	}
}

func TestConsumerIndex_DeleteCleansIndex(t *testing.T) {
	idx := newConsumerIndex(1)
	idx.AddWithCountry("a", "IR")
	idx.AddWithCountry("b", "IR")
	idx.AddWithCountry("c", "")

	idx.Delete("a")
	if _, ok := idx.byCountry["IR"]["a"]; ok {
		t.Error("deleted user still indexed")
	}
	if len(idx.byCountry["IR"]) != 1 {
		t.Errorf("IR set has %d users, want 1", len(idx.byCountry["IR"]))
	}

	idx.Delete("b")
	if _, ok := idx.byCountry["IR"]; ok {
		t.Error("empty country set not removed")
	}
	idx.Delete("c")
	if idx.Size() != 0 || len(idx.country) != 0 || len(idx.byCountry) != 0 {
		t.Errorf("table not empty after deleting everyone: size=%d country=%d byCountry=%d",
			idx.Size(), len(idx.country), len(idx.byCountry))
	}

	// With the only IR consumer gone, targeting IR falls back
	idx.AddWithCountry("d", "RU")
	if got := idx.BroadcastGenesis("m", []string{"IR"}); got != genesisFallback {
		t.Errorf("got %q, want %q", got, genesisFallback)
	}
}

// testCountryHeader carries a consumer's country to the test hook. Production hooks must derive the
// country server-side; a header is fine here because the test controls every request.
const testCountryHeader = "X-Test-Country"

// testServer is Freddie over HTTP with a fresh consumer table.
type testServer struct {
	t      *testing.T
	f      *Freddie
	url    string
	ctx    context.Context
	cancel context.CancelFunc
}

// startTestServer starts Freddie over HTTP with a fresh consumer table and the given country hook.
// Set s.f.RoomTicket before subscribing to enable rooms.
func startTestServer(t *testing.T, hook func(*http.Request) string) *testServer {
	t.Helper()
	consumerTable = newConsumerIndex(0)

	f, err := New(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	f.ConsumerCountry = hook
	srv := httptest.NewServer(f.srv.Handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &testServer{t: t, f: f, url: srv.URL, ctx: ctx, cancel: cancel}
}

// subscribe opens a consumer's genesis stream and returns the messages it receives. tickets, if
// any, go comma-separated in the room tickets header.
func (s *testServer) subscribe(country, query string, tickets ...string) chan string {
	u := s.url + "/v1/signal"
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set(common.VersionHeader, common.Version)
	if country != "" {
		req.Header.Set(testCountryHeader, country)
	}
	if len(tickets) > 0 {
		req.Header.Set(common.RoomTicketsHeader, strings.Join(tickets, ","))
	}
	msgs := make(chan string, 16)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			msgs <- scanner.Text()
		}
	}()
	return msgs
}

// genesis posts a genesis message with extra's fields added to the required ones.
func (s *testServer) genesis(extra url.Values) {
	s.postGenesis(extra)
}

// postGenesis posts a genesis message with send-to "genesis", unless extra sets send-to, plus
// extra's fields. It returns the response status, or 0 when the request timed out before Freddie
// answered.
func (s *testServer) postGenesis(extra url.Values) int {
	form := url.Values{
		"data":    {"{}"},
		"send-to": {common.SignalSendToGenesis},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	for k, vs := range extra {
		if k == "send-to" {
			form.Del(k)
		}
		for _, v := range vs {
			form.Add(k, v)
		}
	}
	// Nobody answers a genesis message here, so don't wait out its TTL
	pctx, pcancel := context.WithTimeout(s.ctx, 200*time.Millisecond)
	defer pcancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, s.url+"/v1/signal",
		strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	res.Body.Close()
	return res.StatusCode
}

func (s *testServer) expect(name string, ch chan string, want bool) {
	s.t.Helper()
	select {
	case <-ch:
		if !want {
			s.t.Errorf("%s consumer received a genesis message it should not have", name)
		}
	case <-time.After(300 * time.Millisecond):
		if want {
			s.t.Errorf("%s consumer did not receive a genesis message", name)
		}
	}
}

func (s *testServer) waitForConsumers(n int) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for consumerTable.Size() < n {
		if time.Now().After(deadline) {
			s.t.Fatal("consumers never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let each handler park in its select loop, since unbuffered broadcast only reaches parked
	// receivers
	time.Sleep(100 * time.Millisecond)
}

// hangUp ends every open subscription and waits for Freddie to drop them.
func (s *testServer) hangUp() {
	s.t.Helper()
	s.cancel()
	deadline := time.Now().Add(5 * time.Second)
	for consumerTable.Size() > 0 {
		if time.Now().After(deadline) {
			s.t.Fatal("subscriptions never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The handler's span ends after its deferred Delete
	time.Sleep(50 * time.Millisecond)
}

// newTestServer starts Freddie over HTTP with a fresh consumer table and the given hook, and
// returns helpers to subscribe consumers and post genesis messages.
func newTestServer(t *testing.T, hook func(*http.Request) string) (
	subscribe func(country, query string) chan string,
	genesis func(targets ...string),
	expect func(name string, ch chan string, want bool),
	waitForConsumers func(n int),
) {
	t.Helper()
	s := startTestServer(t, hook)
	subscribe = func(country, query string) chan string { return s.subscribe(country, query) }
	genesis = func(targets ...string) {
		form := url.Values{}
		for _, c := range targets {
			form.Add(common.TargetCountryField, c)
		}
		s.genesis(form)
	}
	return subscribe, genesis, s.expect, s.waitForConsumers
}

func headerHook(r *http.Request) string { return r.Header.Get(testCountryHeader) }

// indexed snapshots consumerTable's userID -> country index.
func indexed() map[string]string {
	consumerTable.RLock()
	defer consumerTable.RUnlock()
	out := make(map[string]string, len(consumerTable.country))
	for id, c := range consumerTable.country {
		out[id] = c
	}
	return out
}

// End to end through the HTTP handlers: the hook assigns consumers a country, a producer targets
// one on the genesis POST, and old clients that send no targets still interoperate.
func TestHandleSignal_TargetedGenesis(t *testing.T) {
	subscribe, genesis, expect, waitForConsumers := newTestServer(t, headerHook)

	ir := subscribe("ir", "") // lowercase on purpose: freddie normalizes the hook's result
	ru := subscribe("RU", "")
	unknown := subscribe("", "")
	waitForConsumers(3)

	genesis("IR")
	expect("IR", ir, true)
	expect("RU", ru, false)
	expect("unknown", unknown, false)

	genesis("CN")
	expect("IR", ir, true)
	expect("RU", ru, true)
	expect("unknown", unknown, true)

	genesis()
	expect("IR", ir, true)
	expect("RU", ru, true)
	expect("unknown", unknown, true)
}

// A consumer's ?country= claim must not influence matching, whatever the hook says.
func TestHandleSignal_IgnoresCountryQueryParam(t *testing.T) {
	subscribe, genesis, expect, waitForConsumers := newTestServer(t, headerHook)

	spoofer := subscribe("", url.Values{"country": {"IR"}}.Encode())
	ru := subscribe("RU", url.Values{"country": {"IR"}}.Encode())
	waitForConsumers(2)

	// Nobody is really in IR, so the targeted genesis must fall back to everyone rather than be
	// absorbed by the consumers claiming IR.
	genesis("IR")
	expect("spoofer", spoofer, true)
	expect("RU", ru, true)

	genesis("RU")
	expect("spoofer", spoofer, false)
	expect("RU", ru, true)

	for _, c := range indexed() {
		if c == "IR" {
			t.Error("a consumer is indexed under its claimed country IR")
		}
	}
}

// With no hook, no consumer is indexed by country, so every targeted genesis falls back.
func TestHandleSignal_NilHookLeavesConsumersUntargeted(t *testing.T) {
	subscribe, genesis, expect, waitForConsumers := newTestServer(t, nil)

	a := subscribe("IR", url.Values{"country": {"IR"}}.Encode())
	b := subscribe("", "")
	waitForConsumers(2)

	genesis("IR")
	expect("a", a, true)
	expect("b", b, true)
	if got := indexed(); len(got) != 0 {
		t.Errorf("indexed %v with a nil hook, want nothing", got)
	}
}

// The hook's result is normalized, and a result that isn't a country code counts as unknown.
func TestHandleSignal_HookResultNormalized(t *testing.T) {
	for _, tc := range []struct {
		hookResult string
		want       string
	}{
		{"ir", "IR"},
		{"Ir", "IR"},
		{"", ""},
		{"IRN", ""},
		{"1R", ""},
	} {
		t.Run(tc.hookResult, func(t *testing.T) {
			subscribe, _, _, waitForConsumers := newTestServer(t, func(*http.Request) string {
				return tc.hookResult
			})
			subscribe("", "")
			waitForConsumers(1)

			got := indexed()
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("hook returned %q: indexed %v, want nothing", tc.hookResult, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("hook returned %q: indexed %v, want one consumer", tc.hookResult, got)
			}
			for _, c := range got {
				if c != tc.want {
					t.Errorf("hook returned %q: indexed under %q, want %q", tc.hookResult, c, tc.want)
				}
			}
		})
	}
}

// recv returns the message waiting on ch, or "" without blocking.
func recv(ch chan string) string {
	select {
	case m := <-ch:
		return m
	default:
		return ""
	}
}

// Precedence per genesis message: room, then (only when the producer serves
// the pool) country, then everyone.
func TestConsumerIndex_RoomsFirst(t *testing.T) {
	idx := newConsumerIndex(1)
	paired := idx.AddConsumer("paired", "RU", []string{"room-a"})
	ir := idx.AddConsumer("ir", "IR", nil)
	other := idx.AddWithCountry("other", "")

	matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, true, []string{"IR"})
	if !matched || outcome != "" {
		t.Fatalf("room-a: matched=%v outcome=%q, want a room match and no pool step", matched, outcome)
	}
	if got := recv(paired); got != "r" {
		t.Errorf("paired consumer got %q, want the room message", got)
	}
	if received(ir) || received(other) {
		t.Error("a room match still reached pool consumers")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, true, []string{"IR"})
	if matched || outcome != genesisMatched {
		t.Fatalf("room-b, IR: matched=%v outcome=%q, want the country step", matched, outcome)
	}
	if recv(ir) != "m" || received(paired) || received(other) {
		t.Error("with nobody in room-b the IR consumer, and only it, should get the plain message")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, true, []string{"CN"})
	if matched || outcome != genesisFallback {
		t.Fatalf("room-b, CN: matched=%v outcome=%q, want the fallback", matched, outcome)
	}
	if recv(paired) != "m" || recv(ir) != "m" || recv(other) != "m" {
		t.Error("the fallback must reach everyone with the plain message, room members included")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, false, []string{"IR"})
	if matched || outcome != "" {
		t.Fatalf("pool=0, nobody in room-b: matched=%v outcome=%q, want no delivery", matched, outcome)
	}
	matched, outcome = idx.DeliverGenesis("m", "r", nil, false, nil)
	if matched || outcome != "" {
		t.Fatalf("pool=0, no rooms: matched=%v outcome=%q, want no delivery", matched, outcome)
	}
	if received(paired) || received(ir) || received(other) {
		t.Error("a pool=0 genesis with no room consumer reached someone")
	}

	matched, _ = idx.DeliverGenesis("m", "r", []string{"room-a"}, false, nil)
	if !matched || recv(paired) != "r" {
		t.Error("pool=0 must still reach the room")
	}
}

// A room member that can't take the message right now is not a match: a
// producer serving the pool falls back, and one that doesn't reaches nobody.
func TestConsumerIndex_UndeliverableRoomMatch(t *testing.T) {
	idx := newConsumerIndex(1)
	paired := idx.AddConsumer("paired", "", []string{"room-a"})
	pool := idx.AddWithCountry("pool", "")
	paired <- "already full"

	if matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, true, nil); matched || outcome != genesisUntargeted {
		t.Fatalf("matched=%v outcome=%q, want an untargeted pool broadcast", matched, outcome)
	}
	if recv(pool) != "m" {
		t.Error("the pool consumer did not get the fallback")
	}
	if matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, false, nil); matched || outcome != "" {
		t.Fatalf("pool=0: matched=%v outcome=%q, want nothing", matched, outcome)
	}
	if received(pool) {
		t.Error("a pool=0 genesis reached the pool")
	}
}

// A consumer in several of the producer's rooms gets the message once.
func TestConsumerIndex_RoomMessageOncePerConsumer(t *testing.T) {
	// A buffer of 2 is what makes a duplicate visible: with 1, a second send would be dropped by
	// the non-blocking select and the test would pass even if broadcastToRooms sent twice.
	idx := newConsumerIndex(2)
	ch := idx.AddConsumer("u", "", []string{"room-a", "room-b"})
	if n := idx.broadcastToRooms("r", []string{"room-a", "room-b"}); n != 1 {
		t.Errorf("delivered %d times, want 1", n)
	}
	if len(ch) != 1 {
		t.Errorf("%d messages queued, want 1", len(ch))
	}
}

func TestConsumerIndex_DeleteCleansRooms(t *testing.T) {
	idx := newConsumerIndex(1)
	idx.AddConsumer("a", "IR", []string{"room-a", "room-b"})
	idx.AddConsumer("b", "", []string{"room-a"})

	idx.Delete("a")
	if _, ok := idx.byRoom["room-b"]; ok {
		t.Error("an empty room was not removed")
	}
	if len(idx.byRoom["room-a"]) != 1 {
		t.Errorf("room-a has %d members, want 1", len(idx.byRoom["room-a"]))
	}
	idx.Delete("b")
	if idx.Size() != 0 || len(idx.rooms) != 0 || len(idx.byRoom) != 0 || len(idx.byCountry) != 0 {
		t.Errorf("index not empty after deleting everyone: size=%d rooms=%d byRoom=%d byCountry=%d",
			idx.Size(), len(idx.rooms), len(idx.byRoom), len(idx.byCountry))
	}
}
