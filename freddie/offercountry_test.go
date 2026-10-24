package freddie

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

func TestOfferCountryCache_TakeOnceAndExpire(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferCountryCache(func() time.Time { return now })

	c.add("a", "RU")
	if got := c.take("a"); got != "RU" {
		t.Fatalf("got %q, want RU", got)
	}
	if got := c.take("a"); got != "" {
		t.Fatalf("an offer must be labeled once, got %q on the second take", got)
	}

	c.add("b", "CN")
	now = now.Add(offerCountryTTL)
	if got := c.take("b"); got != "" {
		t.Fatalf("an expired offer must not be labeled, got %q", got)
	}
}

func TestOfferCountryCache_FullSweepsExpired(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferCountryCache(func() time.Time { return now })
	for i := range maxOfferCountries {
		c.add(strconv.Itoa(i), "US")
	}
	c.add("live-dropped", "RU")
	if got := c.take("live-dropped"); got != "" {
		t.Fatalf("a full cache of live entries must drop new offers, got %q", got)
	}

	now = now.Add(offerCountryTTL)
	c.add("after-expiry", "RU")
	if got := c.take("after-expiry"); got != "RU" {
		t.Fatalf("a full cache must sweep expired entries to make room, got %q", got)
	}
	if len(c.m) != 0 {
		t.Fatalf("the sweep should have removed every expired entry, %d remain", len(c.m))
	}
}

// postOffer posts an offer to sendTo as a consumer in country and returns the response status.
func (s *testServer) postOffer(sendTo, country string) int {
	form := url.Values{
		"data":    {"{}"},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(common.SignalMsgOffer))},
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	req.Header.Set(testCountryHeader, country)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	res.Body.Close()
	return res.StatusCode
}

func TestDeliveredOffer_LabelsProducerOutcomeWithConsumerCountry(t *testing.T) {
	s := startTestServer(t, headerHook)

	genesisID := "producer-genesis"
	producer := signalTable.Add(genesisID)
	defer signalTable.Delete(genesisID)

	done := make(chan int, 1)
	go func() { done <- s.postOffer(genesisID, "ru") }()

	var offer common.SignalMsg
	select {
	case raw := <-producer:
		if err := json.Unmarshal([]byte(raw), &offer); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the offer never reached the producer")
	}
	// Answer, so the offer request returns rather than waiting out its TTL.
	signalTable.SendOnce(offer.ReplyTo, "{}")
	if status := <-done; status != http.StatusOK {
		t.Fatalf("offer status %d", status)
	}

	if got := s.f.attemptConsumerCountry(offer.ReplyTo); got != "RU" {
		t.Fatalf("got %q, want RU", got)
	}
	if got := s.f.attemptConsumerCountry(offer.ReplyTo); got != "unknown" {
		t.Fatalf("a second report of the same offer must not be labeled again, got %q", got)
	}
	if got := s.f.attemptConsumerCountry(""); got != "unreported" {
		t.Fatalf("a producer that sends no offer ID must be unreported, got %q", got)
	}
}

func TestUndeliveredOffer_NotRemembered(t *testing.T) {
	s := startTestServer(t, headerHook)
	if status := s.postOffer("nobody", "RU"); status != http.StatusNotFound {
		t.Fatalf("offer to a missing producer: status %d, want 404", status)
	}
	if n := len(s.f.offerCountries.m); n != 0 {
		t.Fatalf("an undelivered offer must not be remembered, cache has %d", n)
	}
}
