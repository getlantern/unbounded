package freddie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

// heldGenesisForm is a rooms-only genesis asking to be held, from a volunteer with ticket.
func heldGenesisForm(ticket string) url.Values {
	form := signalForm(common.SignalSendToGenesisRooms, common.SignalMsgGenesis, "{}")
	form.Set(common.RoomTicketField, ticket)
	form.Set(common.HoldField, "1")
	return form
}

func (s *testServer) waitHeld(n int) {
	s.t.Helper()
	require.Eventually(s.t, func() bool { return s.f.heldGeneses.size() == n }, 2*time.Second, 10*time.Millisecond,
		"want %d held geneses, have %d", n, s.f.heldGeneses.size())
}

func recvGenesis(t *testing.T, ch chan string, within time.Duration) (common.SignalMsg, bool) {
	t.Helper()
	select {
	case raw := <-ch:
		var msg common.SignalMsg
		require.NoError(t, json.Unmarshal([]byte(raw), &msg))
		return msg, true
	case <-time.After(within):
		return common.SignalMsg{}, false
	}
}

// A room consumer that subscribes after its volunteer's genesis gets it at once, and its offer
// reaches the volunteer.
func TestHeldGenesis_TakenOnSubscribe(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	turn := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}}}
	s.f.RoomICEServers = func(context.Context) []webrtc.ICEServer { return turn }
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)

	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)

	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	genesis, ok := recvGenesis(t, consumer, time.Second)
	require.True(t, ok, "the room consumer didn't get the held genesis on subscribing")
	assert.True(t, genesis.Room)
	assert.Equal(t, pairingP, genesis.Pairing)
	s.waitHeld(0)

	answered := make(chan []byte, 1)
	go func() {
		_, body := s.post(genesis.ReplyTo, common.SignalMsgOffer, "offer-sdp", nil)
		answered <- body
	}()
	r := vol.reply()
	assert.Equal(t, uint64(1), r.ID)
	var offer common.SignalMsg
	require.NoError(t, json.Unmarshal([]byte(r.Body), &offer))
	assert.Equal(t, "offer-sdp", offer.Payload)
	assert.Equal(t, turn, offer.ICEServers, "a taken genesis's offer carries its room's TURN servers")

	vol.send(2, signalForm(offer.ReplyTo, common.SignalMsgAnswer, "answer-sdp"))
	select {
	case body := <-answered:
		var answer common.SignalMsg
		require.NoError(t, json.Unmarshal(body, &answer))
		assert.Equal(t, "answer-sdp", answer.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the consumer")
	}
	assert.True(t, s.f.roomGeneses.has(genesis.ReplyTo), "a held genesis a room consumer took is a room attempt")
}

// A held genesis goes to one consumer: the next to subscribe to its room gets nothing.
func TestHeldGenesis_OneConsumer(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)

	inviter := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	_, ok := recvGenesis(t, s.subscribe("", "", inviter), time.Second)
	require.True(t, ok)
	_, ok = recvGenesis(t, s.subscribe("", "", inviter), 300*time.Millisecond)
	assert.False(t, ok, "a second consumer got the same held genesis")
}

// A consumer of another room doesn't get it.
func TestHeldGenesis_OtherRoom(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)

	_, ok := recvGenesis(t, s.subscribe("", "", k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)), 300*time.Millisecond)
	assert.False(t, ok)
	_, ok = recvGenesis(t, s.subscribe("", ""), 300*time.Millisecond)
	assert.False(t, ok, "a pool consumer got a rooms-only genesis")
	s.waitHeld(1)
}

// A genesis whose room consumer is already waiting is delivered as before, not held.
func TestHeldGenesis_DeliveredWhenConsumerWaits(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	_, ok := recvGenesis(t, consumer, time.Second)
	require.True(t, ok)
	assert.Zero(t, s.f.heldGeneses.size())
}

// Freddie holds only what asked to be held, over the socket, and serves its rooms alone.
func TestHeldGenesis_OnlyRoomsOnlyOverSocket(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	ticket := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)

	// A POST can't be held.
	posted := make(chan struct{})
	go func() {
		defer close(posted)
		s.genesis(url.Values{
			"send-to":              {common.SignalSendToGenesisRooms},
			common.RoomTicketField: {ticket},
			common.HoldField:       {"1"},
		})
	}()
	// Nor a genesis that also serves the pool, nor one that didn't ask.
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	pool := heldGenesisForm(ticket)
	pool.Set("send-to", common.SignalSendToGenesis)
	vol.send(1, pool)
	unasked := heldGenesisForm(ticket)
	unasked.Del(common.HoldField)
	vol.send(2, unasked)

	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, s.f.heldGeneses.size())
	<-posted
}

// A held genesis no one takes is answered empty once heldGenesisTTL ends, and stops being held.
func TestHeldGenesis_Expires(t *testing.T) {
	ttl := heldGenesisTTL
	heldGenesisTTL = 300 * time.Millisecond
	t.Cleanup(func() { heldGenesisTTL = ttl })
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	r := vol.reply()
	assert.Equal(t, http.StatusOK, r.Status)
	assert.Empty(t, r.Body)
	assert.Zero(t, s.f.heldGeneses.size())
}

// A held genesis whose volunteer's socket closes stops being held, so no consumer is handed it.
func TestHeldGenesis_ReleasedWhenSocketCloses(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)
	vol.c.CloseNow()
	s.waitHeld(0)
}

// A volunteer that gives up on a held genesis cancels it, and Freddie stops holding it at once.
func TestHeldGenesis_ReleasedOnCancel(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)
	b, err := json.Marshal(common.VolunteerRequest{ID: 1, Cancel: true})
	require.NoError(t, err)
	require.NoError(t, vol.c.Write(context.Background(), websocket.MessageText, b))
	s.waitHeld(0)
}

// A subscribing consumer takes the newest held genesis, the likeliest to have a volunteer behind
// it.
func TestHeldGenesis_NewestFirst(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	ticket := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	vol.send(1, heldGenesisForm(ticket))
	s.waitHeld(1)
	vol.send(2, heldGenesisForm(ticket))
	s.waitHeld(2)

	genesis, ok := recvGenesis(t, s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK)), time.Second)
	require.True(t, ok)
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		s.post(genesis.ReplyTo, common.SignalMsgOffer, "offer-sdp", nil)
	}()
	r := vol.reply()
	assert.Equal(t, uint64(2), r.ID)
	var offer common.SignalMsg
	require.NoError(t, json.Unmarshal([]byte(r.Body), &offer))
	vol.send(3, signalForm(offer.ReplyTo, common.SignalMsgAnswer, "answer-sdp"))
	<-answered
}

// A genesis held while a room consumer was subscribed but not receiving, which delivery to its
// unbuffered channel misses, still reaches it within heldRecheck.
func TestHeldGenesis_SubscribedConsumerRechecks(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)

	msg, err := json.Marshal(common.SignalMsg{ReplyTo: "held-1", Type: common.SignalMsgGenesis, Room: true, Pairing: pairingP})
	require.NoError(t, err)
	g := &heldGenesis{id: "held-1", rooms: []roomGenesis{{room: pairingP, msg: string(msg)}}, taken: make(chan struct{})}
	require.False(t, s.f.heldGeneses.deliverOrHold(g, func() bool { return false }))

	genesis, ok := recvGenesis(t, consumer, 2*heldRecheck)
	require.True(t, ok, "the subscribed consumer never picked up the held genesis")
	assert.Equal(t, "held-1", genesis.ReplyTo)
	s.waitHeld(0)
}

// A consumer in several rooms takes the newest genesis held for any of them.
func TestHeldGenesis_NewestAcrossRooms(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)
	vol.send(2, heldGenesisForm(k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(2)

	genesis, ok := recvGenesis(t, s.subscribe("", "",
		k.ticket(pairingP, helperticket.RoleInviter, ticketOK),
		k.ticket(pairingQ, helperticket.RoleInviter, ticketOK)), time.Second)
	require.True(t, ok)
	assert.Equal(t, pairingQ, genesis.Pairing, "took the older genesis of the first room")
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		s.post(genesis.ReplyTo, common.SignalMsgOffer, "offer-sdp", nil)
	}()
	r := vol.reply()
	assert.Equal(t, uint64(2), r.ID)
	var offer common.SignalMsg
	require.NoError(t, json.Unmarshal([]byte(r.Body), &offer))
	vol.send(3, signalForm(offer.ReplyTo, common.SignalMsgAnswer, "answer-sdp"))
	<-answered
}

// A consumer that has a genesis stops looking for held ones: it answers one per subscribe, and
// another it took would be stranded when another of its inviter's workers could answer it.
func TestHeldGenesis_ConsumerTakesOne(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)
	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	_, ok := recvGenesis(t, consumer, time.Second)
	require.True(t, ok)

	// One held while the consumer wasn't receiving, as the recheck exists to catch.
	msg, err := json.Marshal(common.SignalMsg{ReplyTo: "held-2", Type: common.SignalMsgGenesis, Room: true, Pairing: pairingP})
	require.NoError(t, err)
	g := &heldGenesis{id: "held-2", rooms: []roomGenesis{{room: pairingP, msg: string(msg)}}, taken: make(chan struct{})}
	require.False(t, s.f.heldGeneses.deliverOrHold(g, func() bool { return false }))
	_, ok = recvGenesis(t, consumer, 3*heldRecheck)
	assert.False(t, ok, "the consumer took a second held genesis")
	assert.Equal(t, 1, s.f.heldGeneses.size())
}

// A held genesis's span ends as an unanswered genesis's does, at defaultMsgTTL with the timeout
// event, however long the hold lasts, so neither its length nor its events single out a paired
// volunteer.
func TestHeldGenesis_SpanLooksUnanswered(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })

	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, heldGenesisForm(k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)))
	s.waitHeld(1)

	var held sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool {
		for _, sp := range spans.Ended() {
			if sp.Name() == "handleVolunteerSignal" {
				held = sp
				return true
			}
		}
		return false
	}, 2*defaultMsgTTL*time.Second, 50*time.Millisecond, "the held genesis's span never ended")
	took := held.EndTime().Sub(held.StartTime())
	assert.InDelta(t, float64(defaultMsgTTL*time.Second), float64(took), float64(500*time.Millisecond))
	var events []string
	for _, e := range held.Events() {
		events = append(events, e.Name)
	}
	assert.Equal(t, []string{"timeout waiting for response"}, events)
	assert.Equal(t, 1, s.f.heldGeneses.size(), "ending the span doesn't end the hold")
}

// A pool genesis doesn't stop a room consumer looking for a held one: a rooms-only consumer, or
// one that needs its room's TURN servers, ignores it.
func TestHeldGenesis_PoolGenesisKeepsRechecking(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	consumer := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)
	go s.genesis(nil)
	pool, ok := recvGenesis(t, consumer, time.Second)
	require.True(t, ok)
	require.False(t, pool.IsRoomGenesis())

	msg, err := json.Marshal(common.SignalMsg{ReplyTo: "held-1", Type: common.SignalMsgGenesis, Room: true, Pairing: pairingP})
	require.NoError(t, err)
	g := &heldGenesis{id: "held-1", rooms: []roomGenesis{{room: pairingP, msg: string(msg)}}, taken: make(chan struct{})}
	require.False(t, s.f.heldGeneses.deliverOrHold(g, func() bool { return false }))
	genesis, ok := recvGenesis(t, consumer, 3*heldRecheck)
	require.True(t, ok, "a pool genesis stopped the consumer looking for a held room genesis")
	assert.Equal(t, "held-1", genesis.ReplyTo)
}
