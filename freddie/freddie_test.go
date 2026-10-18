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

// newTestServer starts Freddie over HTTP with a fresh consumer table and the given hook, and
// returns helpers to subscribe consumers and post genesis messages.
func newTestServer(t *testing.T, hook func(*http.Request) string) (
	subscribe func(country, query string) chan string,
	genesis func(targets ...string),
	expect func(name string, ch chan string, want bool),
	waitForConsumers func(n int),
) {
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

	subscribe = func(country, query string) chan string {
		u := srv.URL + "/v1/signal"
		if query != "" {
			u += "?" + query
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(common.VersionHeader, common.Version)
		if country != "" {
			req.Header.Set(testCountryHeader, country)
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

	genesis = func(targets ...string) {
		form := url.Values{
			"data":    {"{}"},
			"send-to": {"genesis"},
			"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
		}
		for _, c := range targets {
			form.Add(common.TargetCountryField, c)
		}
		// Nobody answers a genesis message here, so don't wait out its TTL
		pctx, pcancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer pcancel()
		req, err := http.NewRequestWithContext(pctx, http.MethodPost, srv.URL+"/v1/signal",
			strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(common.VersionHeader, common.Version)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}

	expect = func(name string, ch chan string, want bool) {
		t.Helper()
		select {
		case <-ch:
			if !want {
				t.Errorf("%s consumer received a genesis message it should not have", name)
			}
		case <-time.After(300 * time.Millisecond):
			if want {
				t.Errorf("%s consumer did not receive a genesis message", name)
			}
		}
	}

	waitForConsumers = func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for consumerTable.Size() < n {
			if time.Now().After(deadline) {
				t.Fatal("consumers never registered")
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Let each handler park in its select loop, since unbuffered broadcast only reaches parked
		// receivers
		time.Sleep(100 * time.Millisecond)
	}
	return subscribe, genesis, expect, waitForConsumers
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
