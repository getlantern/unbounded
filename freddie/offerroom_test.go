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
	"github.com/getlantern/broflake/common/helperticket"
)

// postOfferWithTickets posts an offer with payload data to sendTo, carrying tickets in the room
// tickets header, as a consumer answering a room genesis with a direct exit offer does.
func (s *testServer) postOfferWithTickets(sendTo, data string, tickets []string) {
	form := url.Values{
		"data":    {data},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(common.SignalMsgOffer))},
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Error(err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	if len(tickets) > 0 {
		req.Header.Set(common.RoomTicketsHeader, strings.Join(tickets, ","))
	}
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
	}
}

// offerFor posts a genesis with form's fields, waits for it on consumer, answers it with an offer
// carrying data and offerTickets, and returns the offer as the producer got it, answered so the
// offer's request returns.
func (s *testServer) offerFor(form url.Values, consumer chan string, data string, offerTickets []string) common.SignalMsg {
	s.t.Helper()
	resp := s.genesisResponse(form)
	var g common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the genesis never reached the consumer")
	}
	go s.postOfferWithTickets(g.ReplyTo, data, offerTickets)
	select {
	case raw := <-resp:
		if raw == "" {
			s.t.Fatal("the producer got no offer")
		}
		return answerOffer(s.t, raw)
	case <-time.After(10 * time.Second):
		s.t.Fatal("the producer got no offer")
	}
	return common.SignalMsg{}
}

// Freddie stamps an offer with its room's pairing only for a consumer admitted, at offer time, to
// the room it got the genesis through.
func TestOfferPairing_Stamp(t *testing.T) {
	k := newRoomKeys(t)
	other := newRoomKeys(t)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	for _, tc := range []struct {
		name string
		// subscribe are the tickets the answering consumer subscribes with.
		subscribe []string
		// genesis are the producer's volunteer tickets.
		genesis []string
		data    string
		offer   []string
		want    string
	}{
		{"through its room, admitted", []string{inv}, []string{vol}, "{}", []string{inv}, pairingP},
		{"admitted by one of several tickets", []string{inv}, []string{vol}, "{}",
			[]string{k.ticket(pairingQ, helperticket.RoleInviter, ticketOK), inv}, pairingP},
		{"no tickets on the offer", []string{inv}, []string{vol}, "{}", nil, ""},
		{"a pairing forged in the payload", []string{inv}, []string{vol}, `{"Pairing":"` + pairingP + `"}`, nil, ""},
		{"admitted to another room only", []string{inv}, []string{vol}, "{}",
			[]string{k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)}, ""},
		{"its ticket expired since it subscribed", []string{inv}, []string{vol}, "{}",
			[]string{k.ticket(pairingP, helperticket.RoleInviter, -time.Minute)}, ""},
		{"a ticket signed by an untrusted key", []string{inv}, []string{vol}, "{}",
			[]string{other.ticket(pairingP, helperticket.RoleInviter, ticketOK)}, ""},
		{"a volunteer ticket instead of an inviter one", []string{inv}, []string{vol}, "{}", []string{vol}, ""},
		// The pool consumer is the friend itself, answering a pool genesis with its valid ticket.
		{"a pool genesis", nil, nil, "{}", []string{inv}, ""},
		{"a mixed genesis no room consumer was waiting for", nil, []string{vol}, "{}", []string{inv}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startRoomServer(t, k)
			consumer := s.subscribe("", "", tc.subscribe...)
			s.waitForConsumers(1)
			offer := s.offerFor(roomGenesisForm(tc.genesis, nil), consumer, tc.data, tc.offer)
			if offer.Type != common.SignalMsgOffer || offer.Payload != tc.data {
				t.Fatalf("offer: type %v, payload changed: %v", offer.Type, offer.Payload != tc.data)
			}
			if offer.Pairing != tc.want {
				t.Errorf("stamped pairing %q, want %q", offer.Pairing, tc.want)
			}
			s.f.genesisRooms.Lock()
			defer s.f.genesisRooms.Unlock()
			if n := len(s.f.genesisRooms.m); n != 0 {
				t.Errorf("%d geneses' rooms remembered after their requests ended", n)
			}
		})
	}
}

// A consumer's admission is checked when it offers, so a key Freddie stops trusting after the
// consumer subscribed stamps nothing.
func TestOfferPairing_RevokedKey(t *testing.T) {
	k := newRoomKeys(t)
	keys := newKeyring(k)
	s := startTestServer(t, headerHook)
	s.f.RoomTicket = helperticket.RoomFunc(keys.get, time.Now)
	s.f.KnownKeyID = keys.known()
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	consumer := s.subscribe("", "", inv)
	s.waitForConsumers(1)

	resp := s.genesisResponse(roomGenesisForm([]string{vol}, nil))
	var g common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the genesis never reached the consumer")
	}
	if !g.IsRoomGenesis() {
		t.Fatal("the genesis wasn't delivered through the room")
	}
	keys.revokeAll()
	go s.postOfferWithTickets(g.ReplyTo, "{}", []string{inv})
	offer := answerOffer(t, <-resp)
	if offer.Pairing != "" {
		t.Errorf("stamped pairing %q after the key was revoked", offer.Pairing)
	}
}

func TestGenesisRooms(t *testing.T) {
	g := newGenesisRooms()
	if g.has("g") || g.room("g", []string{"a"}) != "" {
		t.Fatal("an unknown genesis has no rooms")
	}
	rooms := []string{"a", "b"}
	g.set("g", rooms)
	rooms[0] = "z"
	if got := g.room("g", []string{"b", "a"}); got != "a" {
		t.Errorf("room %q, want the first of the genesis's rooms the consumer shares, a", got)
	}
	if got := g.room("g", []string{"c"}); got != "" {
		t.Errorf("room %q for a consumer in none of them", got)
	}
	g.drop("g")
	if g.has("g") {
		t.Error("a dropped genesis is still remembered")
	}
}
