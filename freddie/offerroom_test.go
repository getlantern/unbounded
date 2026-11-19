package freddie

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

// postOfferWithToken posts an offer with payload data to sendTo, echoing token in the delivery
// token field and carrying tickets, if any, in the room tickets header.
func (s *testServer) postOfferWithToken(sendTo, data, token string, tickets []string) {
	form := url.Values{
		"data":    {data},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(common.SignalMsgOffer))},
	}
	if token != "" {
		form.Set(common.DeliveryTokenField, token)
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

// receiveGenesis waits for a genesis on consumer and decodes it.
func (s *testServer) receiveGenesis(consumer chan string) common.SignalMsg {
	s.t.Helper()
	var g common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the genesis never reached the consumer")
	}
	return g
}

// offerReceived returns the offer the producer got on resp, answered so the offer's request
// returns.
func (s *testServer) offerReceived(resp chan string) common.SignalMsg {
	s.t.Helper()
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

// Freddie stamps an offer with its room's pairing only for the consumer it delivered the genesis
// to through that room, echoing that delivery's token, while the ticket that admitted it is valid.
func TestOfferPairing_Stamp(t *testing.T) {
	k := newRoomKeys(t)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	invQ := k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)
	for _, tc := range []struct {
		name string
		// subscribe are the tickets the answering consumer subscribes with.
		subscribe []string
		// genesis are the producer's volunteer tickets.
		genesis []string
		// token returns the token the offer echoes, given the delivered one.
		token func(delivered string) string
		data  string
		// offerTickets go on the offer's POST, which Freddie ignores.
		offerTickets []string
		room         bool
		want         string
	}{
		{"through its room, echoing its token", []string{inv}, []string{vol}, same, "{}", nil, true, pairingP},
		{"no token", []string{inv}, []string{vol}, none, "{}", []string{inv}, true, ""},
		{"an unknown token", []string{inv}, []string{vol}, func(string) string { return "00112233445566778899aabbccddeeff" }, "{}", nil, true, ""},
		{"a pairing forged in the payload", []string{inv}, []string{vol}, none, `{"Pairing":"` + pairingP + `"}`, nil, true, ""},
		// The consumer got the genesis through room P; another room's ticket can't make it Q's.
		{"room P's delivery, with a room Q ticket", []string{inv}, []string{vol, k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, same, "{}", []string{invQ}, true, pairingP},
		// The friend answering a pool genesis with its valid ticket gets no token, so no stamp.
		{"a pool genesis", nil, nil, same, "{}", []string{inv}, false, ""},
		{"a mixed genesis no room consumer was waiting for", nil, []string{vol}, same, "{}", []string{inv}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startRoomServer(t, k)
			consumer := s.subscribe("", "", tc.subscribe...)
			s.waitForConsumers(1)
			resp := s.genesisResponse(roomGenesisForm(tc.genesis, nil))
			g := s.receiveGenesis(consumer)
			if g.IsRoomGenesis() != tc.room || (g.DeliveryToken != "") != tc.room {
				t.Fatalf("room delivery %v with token %v, want %v", g.IsRoomGenesis(), g.DeliveryToken != "", tc.room)
			}
			go s.postOfferWithToken(g.ReplyTo, tc.data, tc.token(g.DeliveryToken), tc.offerTickets)
			offer := s.offerReceived(resp)
			if offer.Type != common.SignalMsgOffer || offer.Payload != tc.data {
				t.Fatalf("offer: type %v, payload changed: %v", offer.Type, offer.Payload != tc.data)
			}
			if offer.DeliveryToken != "" {
				t.Error("the producer saw the delivery token")
			}
			if offer.Pairing != tc.want {
				t.Errorf("stamped pairing %q, want %q", offer.Pairing, tc.want)
			}
		})
	}
}

func same(token string) string { return token }
func none(string) string       { return "" }

// Each consumer a room genesis is delivered to gets its own token.
func TestOfferPairing_TokenPerConsumer(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	a := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	b := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(2)
	resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
	ga, gb := s.receiveGenesis(a), s.receiveGenesis(b)
	if ga.DeliveryToken == "" || gb.DeliveryToken == "" || ga.DeliveryToken == gb.DeliveryToken {
		t.Fatal("each consumer should get its own token")
	}
	go s.postOfferWithToken(gb.ReplyTo, "{}", gb.DeliveryToken, nil)
	if offer := s.offerReceived(resp); offer.Pairing != pairingP {
		t.Errorf("stamped pairing %q, want the room's", offer.Pairing)
	}
}

// A consumer's admission is checked again when it offers: a ticket that has expired since
// delivery, or whose key Freddie has stopped trusting, stamps nothing.
func TestOfferPairing_AdmissionEndedBeforeOffer(t *testing.T) {
	for _, tc := range []string{"expired", "revoked"} {
		t.Run(tc, func(t *testing.T) {
			k := newRoomKeys(t)
			keys := newKeyring(k)
			clock := newFakeClock()
			s := startTestServer(t, headerHook)
			s.f.tickets = newTicketCache(maxCachedTickets, clock.now)
			s.f.RoomTicket = helperticket.RoomFunc(keys.get, clock.now)
			s.f.KnownKeyID = keys.known()
			inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
			consumer := s.subscribe("", "", inv)
			s.waitForConsumers(1)
			resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
			g := s.receiveGenesis(consumer)
			if g.DeliveryToken == "" {
				t.Fatal("the room delivery has no token")
			}
			if tc == "expired" {
				clock.add(ticketOK + time.Minute)
			} else {
				keys.revokeAll()
			}
			go s.postOfferWithToken(g.ReplyTo, "{}", g.DeliveryToken, nil)
			if offer := s.offerReceived(resp); offer.Pairing != "" {
				t.Errorf("stamped pairing %q after the admission ended", offer.Pairing)
			}
		})
	}
}

// A token is redeemed once, and only for the genesis it was issued for.
func TestOfferPairing_TokenBinding(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	issue := func(genesisID string) string {
		return s.f.deliveries.issue(delivery{genesisID: genesisID, room: pairingP, ticket: inv})
	}
	tok := issue("g1")
	if got := s.f.offerPairing("g2", tok); got != "" {
		t.Errorf("a token stamped another genesis's offer: %q", got)
	}
	if got := s.f.offerPairing("g1", tok); got != "" {
		t.Errorf("a token redeemed by another genesis's offer was redeemed again: %q", got)
	}
	tok = issue("g1")
	if got := s.f.offerPairing("g1", tok); got != pairingP {
		t.Errorf("stamped %q, want the room's", got)
	}
	if got := s.f.offerPairing("g1", tok); got != "" {
		t.Errorf("a used token was redeemed again: %q", got)
	}
	s.f.RoomTicket = nil
	if got := s.f.offerPairing("g1", issue("g1")); got != "" {
		t.Errorf("stamped %q with rooms disabled", got)
	}
}

func TestDeliveryTokens(t *testing.T) {
	now := time.Unix(0, 0)
	s := newDeliveryTokens(func() time.Time { return now })
	d := delivery{genesisID: "g", room: "r", ticket: "t"}
	a, b := s.issue(d), s.issue(d)
	if len(a) != 32 || a == b {
		t.Fatalf("tokens should be distinct 128-bit hex strings: %d chars", len(a))
	}
	if got, ok := s.take(a); !ok || got != d {
		t.Fatal("an issued token should be redeemed")
	}
	if _, ok := s.take(a); ok {
		t.Fatal("a token should be redeemed once")
	}
	s.revoke(b)
	if _, ok := s.take(b); ok {
		t.Fatal("a revoked token should not be redeemed")
	}
	c := s.issue(d)
	now = now.Add(deliveryTokenTTL)
	if _, ok := s.take(c); ok {
		t.Fatal("an expired token should not be redeemed")
	}
	for range maxDeliveryTokens + 10 {
		s.issue(d)
	}
	if n := s.order.Len(); n > maxDeliveryTokens || len(s.m) != n {
		t.Fatalf("the store holds %d tokens, %d indexed, over its bound %d", n, len(s.m), maxDeliveryTokens)
	}
}

func TestDeliveryTokens_Concurrent(t *testing.T) {
	s := newDeliveryTokens(time.Now)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				tok := s.issue(delivery{genesisID: strconv.Itoa(i)})
				if i%2 == 0 {
					s.revoke(tok)
				} else if _, ok := s.take(tok); !ok {
					t.Error("a fresh token wasn't redeemed")
				}
			}
		}()
	}
	wg.Wait()
	if len(s.m) != 0 || s.order.Len() != 0 {
		t.Errorf("%d tokens left", len(s.m))
	}
}

// Racing redemptions of one token: exactly one wins.
func TestDeliveryTokens_RacingTake(t *testing.T) {
	s := newDeliveryTokens(time.Now)
	for range 50 {
		tok := s.issue(delivery{genesisID: "g"})
		start := make(chan struct{})
		var won atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, ok := s.take(tok); ok {
					won.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if n := won.Load(); n != 1 {
			t.Fatalf("%d redemptions of one token succeeded, want 1", n)
		}
	}
}

// A held genesis taken by a subscribing room consumer carries a token too, and its offer is
// stamped.
func TestOfferPairing_HeldGenesis(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	if err != nil {
		t.Fatal(err)
	}
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)
	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	g, ok := recvGenesis(t, consumer, time.Second)
	if !ok || g.DeliveryToken == "" {
		t.Fatal("the held genesis came without a token")
	}
	go s.postOfferWithToken(g.ReplyTo, "{}", g.DeliveryToken, nil)
	var offer common.SignalMsg
	if err := json.Unmarshal([]byte(vol.reply().Body), &offer); err != nil {
		t.Fatal(err)
	}
	if offer.Pairing != pairingP {
		t.Errorf("stamped pairing %q, want the room's", offer.Pairing)
	}
}

// The ticket kept for a room's delivery tokens is its own copy, not a slice of the request's whole
// room tickets header, which could otherwise be padded to keep a large header alive.
func TestVerifyRoomTicketsBy_CopiesTicket(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	tickets := common.ParseRoomTicketsHeader([]string{inv + ",x"})
	if len(tickets) == 0 || tickets[0] != inv {
		t.Fatal("the header should parse to the ticket")
	}
	rooms, ticketOf := s.f.verifyRoomTicketsBy(tickets, helperticket.RoleInviter)
	if len(rooms) != 1 || ticketOf[pairingP] != inv {
		t.Fatal("the ticket should admit to its room")
	}
	if unsafe.StringData(ticketOf[pairingP]) == unsafe.StringData(tickets[0]) {
		t.Error("the kept ticket shares the header's memory")
	}
}
