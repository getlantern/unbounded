package freddie

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

func TestRoomGenesisSet_Expires(t *testing.T) {
	now := time.Unix(0, 0)
	s := newRoomGenesisSet(func() time.Time { return now })
	s.add("g")
	if !s.has("g") || s.has("other") {
		t.Fatal("only an added genesis should be remembered")
	}
	// Several room consumers may answer one genesis, so asking must not forget it.
	if !s.has("g") {
		t.Fatal("asking about a genesis must not forget it")
	}
	now = now.Add(roomGenesisTTL - time.Nanosecond)
	if !s.has("g") {
		t.Fatal("a genesis should be remembered for its whole TTL")
	}
	now = now.Add(time.Nanosecond)
	if s.has("g") {
		t.Fatal("an expired genesis must be forgotten")
	}
	s.add("new")
	if _, ok := s.m["g"]; ok || s.order.Len() != 1 {
		t.Fatalf("an expired genesis should be dropped when another is added, %d remain", s.order.Len())
	}
}

func TestRoomGenesisSet_ReAddRefreshes(t *testing.T) {
	now := time.Unix(0, 0)
	s := newRoomGenesisSet(func() time.Time { return now })
	s.add("g")
	now = now.Add(roomGenesisTTL / 2)
	s.add("g")
	now = now.Add(roomGenesisTTL / 2)
	if !s.has("g") || s.order.Len() != 1 {
		t.Fatalf("re-adding a genesis should refresh it and keep one entry, has %d", s.order.Len())
	}
}

func TestRoomGenesisSet_FullEvictsOldest(t *testing.T) {
	now := time.Unix(0, 0)
	s := newRoomGenesisSet(func() time.Time { return now })
	for i := range maxRoomGeneses {
		s.add(strconv.Itoa(i))
	}
	s.add("newest")
	if len(s.m) != maxRoomGeneses || s.order.Len() != maxRoomGeneses {
		t.Fatalf("the set must stay at its cap, has %d entries and %d in order", len(s.m), s.order.Len())
	}
	if s.has("0") {
		t.Fatal("the oldest genesis should have been evicted")
	}
	if !s.has("1") || !s.has("newest") {
		t.Fatal("only the oldest genesis should have been evicted")
	}
}

func TestOutcomeDelivery(t *testing.T) {
	remembered := newOffer()
	for _, tc := range []struct {
		reported bool
		oc       offerConsumer
		room     bool
		want     string
	}{
		{false, remembered, true, "unreported"},
		{true, offerConsumer{}, false, "unknown"},
		{true, remembered, false, "pool"},
		{true, remembered, true, "room"},
	} {
		if got := outcomeDelivery(tc.reported, tc.oc, tc.room); got != tc.want {
			t.Errorf("outcomeDelivery(%v, %+v, %v) = %q, want %q", tc.reported, tc.oc, tc.room, got, tc.want)
		}
	}
}

func TestRecordProducerOutcome_LabelsDelivery(t *testing.T) {
	got := outcomeLabels(t, url.Values{common.PrevOutcomeField: {"failed"}}, common.Version, "delivery")
	if got["unreported"] != 1 || len(got) != 1 {
		t.Fatalf("a report with no offer ID: got %v, want unreported", got)
	}
	got = outcomeLabels(t, url.Values{common.PrevOutcomeField: {"failed"}, common.PrevOfferIDField: {"forgotten"}}, common.Version, "delivery")
	if got["unknown"] != 1 || len(got) != 1 {
		t.Fatalf("a report of an offer Freddie doesn't remember: got %v, want unknown", got)
	}
}

// An offer can reach Freddie before the genesis handler records that a room consumer took the
// genesis. The attempt must still be labeled room, since the producer reports it only later.
func TestAttemptConsumerAttrs_RoomRecordedAfterOffer(t *testing.T) {
	f := &Freddie{offerConsumers: newOfferConsumerCache(time.Now), roomGeneses: newRoomGenesisSet(time.Now)}
	oc := newOffer()
	oc.genesisID = "genesis"
	f.offerConsumers.add("offer", oc)
	f.roomGeneses.add("genesis")
	for _, kv := range f.attemptConsumerAttrs("offer") {
		if kv.Key == "delivery" && kv.Value.AsString() != "room" {
			t.Fatalf("got delivery %q, want room", kv.Value.AsString())
		}
	}
}

// genesisAttempt posts a genesis message with form's fields, waits for consumer to receive it,
// answers it with an offer as a consumer would, and returns the offer's ID as the producer sees it.
func (s *testServer) genesisAttempt(form url.Values, consumer chan string) string {
	s.t.Helper()
	form.Set("data", "{}")
	if form.Get("send-to") == "" {
		form.Set("send-to", common.SignalSendToGenesis)
	}
	form.Set("type", strconv.Itoa(int(common.SignalMsgGenesis)))
	genesisDone := make(chan []byte, 1)
	go func() {
		req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
		if err != nil {
			genesisDone <- nil
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(common.VersionHeader, common.Version)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			genesisDone <- nil
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		genesisDone <- body
	}()

	var genesis common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &genesis); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the genesis never reached the consumer")
	}

	offerDone := make(chan int, 1)
	go func() { offerDone <- s.postOffer(genesis.ReplyTo, "RU", common.Version) }()
	var offer common.SignalMsg
	if err := json.Unmarshal(<-genesisDone, &offer); err != nil {
		s.t.Fatalf("the offer never reached the producer: %v", err)
	}
	// Answer, so the offer request returns rather than waiting out its TTL.
	signalTable.SendOnce(offer.ReplyTo, "{}")
	if status := <-offerDone; status != http.StatusOK {
		s.t.Fatalf("offer status %d", status)
	}
	return offer.ReplyTo
}

// deliveryLabel returns the delivery label for an attempt that answered offerID.
func (s *testServer) deliveryLabel(offerID string) string {
	for _, kv := range s.f.attemptConsumerAttrs(offerID) {
		if kv.Key == "delivery" {
			return kv.Value.AsString()
		}
	}
	return ""
}

func TestSignaling_LabelsRoomAttempt(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)

	offerID := s.genesisAttempt(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil), paired)
	if got := s.deliveryLabel(offerID); got != "room" {
		t.Fatalf("an attempt through a room genesis: got delivery %q, want room", got)
	}
	if got := s.deliveryLabel(offerID); got != "unknown" {
		t.Fatalf("a second report of the same offer must be unknown, got %q", got)
	}
	if got := s.deliveryLabel(""); got != "unreported" {
		t.Fatalf("a producer that sends no offer ID must be unreported, got %q", got)
	}
}

func TestSignaling_LabelsPoolAttempt(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	pool := s.subscribe("RU", "")
	s.waitForConsumers(1)

	for name, form := range map[string]url.Values{
		"no rooms": {},
		// Nobody is in Q's room, so the genesis falls through to the pool.
		"no room consumer": roomGenesisForm([]string{k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, nil),
	} {
		offerID := s.genesisAttempt(form, pool)
		if got := s.deliveryLabel(offerID); got != "pool" {
			t.Errorf("%s: got delivery %q, want pool", name, got)
		}
		s.waitForConsumers(1)
	}
}
