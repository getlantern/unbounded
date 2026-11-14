package freddie

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
