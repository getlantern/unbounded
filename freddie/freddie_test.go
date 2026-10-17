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

// End to end through the HTTP handlers: consumers declare a country on GET, a producer targets
// one on the genesis POST, and old clients that send neither still interoperate.
func TestHandleSignal_TargetedGenesis(t *testing.T) {
	consumerTable = newConsumerIndex(0)

	f, err := New(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.srv.Handler)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subscribe := func(country string) chan string {
		u := srv.URL + "/v1/signal"
		if country != "" {
			u += "?" + url.Values{common.ConsumerCountryParam: {country}}.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(common.VersionHeader, common.Version)
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

	genesis := func(targets ...string) {
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

	expect := func(name string, ch chan string, want bool) {
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

	ir := subscribe("ir") // lowercase on purpose: freddie normalizes
	ru := subscribe("RU")
	legacy := subscribe("")

	deadline := time.Now().Add(5 * time.Second)
	for consumerTable.Size() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("consumers never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let each handler park in its select loop, since unbuffered broadcast only reaches parked
	// receivers
	time.Sleep(100 * time.Millisecond)

	genesis("IR")
	expect("IR", ir, true)
	expect("RU", ru, false)
	expect("legacy", legacy, false)

	genesis("CN")
	expect("IR", ir, true)
	expect("RU", ru, true)
	expect("legacy", legacy, true)

	genesis()
	expect("IR", ir, true)
	expect("RU", ru, true)
	expect("legacy", legacy, true)
}
