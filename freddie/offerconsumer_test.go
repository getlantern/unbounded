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

func TestOfferConsumerCache_TakeOnceAndExpire(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })

	c.add("a", offerConsumer{country: "RU"})
	if got := c.take("a").country; got != "RU" {
		t.Fatalf("got %q, want RU", got)
	}
	if got := c.take("a").country; got != "" {
		t.Fatalf("an offer must be labeled once, got %q on the second take", got)
	}

	c.add("b", offerConsumer{country: "CN"})
	now = now.Add(offerConsumerTTL)
	if got := c.take("b").country; got != "" {
		t.Fatalf("an expired offer must not be labeled, got %q", got)
	}
}

func TestOfferConsumerCache_FullEvictsOldest(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })
	for i := range maxOfferConsumers {
		c.add(strconv.Itoa(i), offerConsumer{country: "US"})
	}
	c.add("newest", offerConsumer{country: "RU"})
	if len(c.m) != maxOfferConsumers || c.order.Len() != maxOfferConsumers {
		t.Fatalf("the cache must stay at its cap, has %d entries and %d in order", len(c.m), c.order.Len())
	}
	if got := c.take("0").country; got != "" {
		t.Fatalf("the oldest offer should have been evicted, got %q", got)
	}
	if got := c.take("1").country; got != "US" {
		t.Fatalf("only the oldest offer should have been evicted, got %q", got)
	}
	if got := c.take("newest").country; got != "RU" {
		t.Fatalf("a full cache must still remember new offers, got %q", got)
	}
}

func TestOfferConsumerCache_DropsExpiredOnAdd(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })
	c.add("old", offerConsumer{country: "US"})
	now = now.Add(offerConsumerTTL)
	c.add("new", offerConsumer{country: "RU"})
	if _, ok := c.m["old"]; ok || c.order.Len() != 1 {
		t.Fatalf("an expired offer should be dropped when another is added, %d remain", c.order.Len())
	}
}

func TestOfferConsumerCache_SurvivesLongSession(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })
	c.add("a", offerConsumer{country: "IR"})
	// A connected attempt is reported only when its session ends.
	now = now.Add(3 * time.Hour)
	if got := c.take("a").country; got != "IR" {
		t.Fatalf("a long session's outcome should keep its country, got %q", got)
	}
}

// postOffer posts an offer to sendTo as a consumer in country linking pion/dtls version dtls, and
// returns the response status.
func (s *testServer) postOffer(sendTo, country, dtls string) int {
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
	if dtls != "" {
		req.Header.Set(common.PionDTLSHeader, dtls)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	res.Body.Close()
	return res.StatusCode
}

// deliverOffer posts an offer from a consumer in country linking pion/dtls version dtls to a
// waiting producer, answers it, and returns the offer's ID as the producer sees it.
func (s *testServer) deliverOffer(country, dtls string) string {
	s.t.Helper()
	genesisID := "producer-genesis"
	producer := signalTable.Add(genesisID)
	defer signalTable.Delete(genesisID)

	done := make(chan int, 1)
	go func() { done <- s.postOffer(genesisID, country, dtls) }()

	var offer common.SignalMsg
	select {
	case raw := <-producer:
		if err := json.Unmarshal([]byte(raw), &offer); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the offer never reached the producer")
	}
	// Answer, so the offer request returns rather than waiting out its TTL.
	signalTable.SendOnce(offer.ReplyTo, "{}")
	if status := <-done; status != http.StatusOK {
		s.t.Fatalf("offer status %d", status)
	}
	return offer.ReplyTo
}

// consumerLabels returns the consumer_country and consumer_dtls labels for an attempt that
// answered offerID.
func (s *testServer) consumerLabels(offerID string) (country, dtls string) {
	for _, kv := range s.f.attemptConsumerAttrs(offerID) {
		switch kv.Key {
		case "consumer_country":
			country = kv.Value.AsString()
		case "consumer_dtls":
			dtls = kv.Value.AsString()
		}
	}
	return country, dtls
}

func TestDeliveredOffer_LabelsProducerOutcomeWithConsumer(t *testing.T) {
	s := startTestServer(t, headerHook)
	offerID := s.deliverOffer("ru", "v3.1.15")

	if c, d := s.consumerLabels(offerID); c != "RU" || d != "v3.1.15" {
		t.Fatalf("got %q, %q, want RU, v3.1.15", c, d)
	}
	if c, d := s.consumerLabels(offerID); c != "unknown" || d != "unknown" {
		t.Fatalf("a second report of the same offer must not be labeled again, got %q, %q", c, d)
	}
	if c, d := s.consumerLabels(""); c != "unreported" || d != "unreported" {
		t.Fatalf("a producer that sends no offer ID must be unreported, got %q, %q", c, d)
	}
}

func TestDeliveredOffer_ConsumerWithoutDTLSHeader(t *testing.T) {
	s := startTestServer(t, headerHook)
	if c, d := s.consumerLabels(s.deliverOffer("cn", "")); c != "CN" || d != "not_sent" {
		t.Fatalf("got %q, %q, want CN, not_sent", c, d)
	}
}

func TestDeliveredOffer_LabelsDTLSWithoutCountryHook(t *testing.T) {
	s := startTestServer(t, nil)
	if c, d := s.consumerLabels(s.deliverOffer("ru", "v3.1.2")); c != "unknown" || d != "v3.1.2" {
		t.Fatalf("got %q, %q, want unknown, v3.1.2", c, d)
	}
}

func TestUndeliveredOffer_NotRemembered(t *testing.T) {
	s := startTestServer(t, headerHook)
	if status := s.postOffer("nobody", "RU", "v3.1.15"); status != http.StatusNotFound {
		t.Fatalf("offer to a missing producer: status %d, want 404", status)
	}
	if n := len(s.f.offerConsumers.m); n != 0 {
		t.Fatalf("an undelivered offer must not be remembered, cache has %d", n)
	}
}
