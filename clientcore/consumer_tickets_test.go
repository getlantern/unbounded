//go:build !js

package clientcore

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

// A rooms-only consumer without room tickets doesn't subscribe, since nothing it would answer could
// reach it, but waits for them, and subscribes with them once its source has them.
func TestRoomsOnlyConsumerWaitsForTickets(t *testing.T) {
	poll := roomTicketPoll
	roomTicketPoll = 10 * time.Millisecond
	t.Cleanup(func() { roomTicketPoll = poll })

	subscribed := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subscribed <- r.Header.Get(common.RoomTicketsHeader)
	}))
	t.Cleanup(srv.Close)
	var tickets atomic.Value
	tickets.Store([]string(nil))
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.ConsumerRoomsOnly = true
	o.RoomTicketSource = func() []string { return tickets.Load().([]string) }
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	pc, _ := newTestPeerConnection(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		consumerState1(t, fsm, pc)
	}()
	select {
	case got := <-subscribed:
		t.Fatalf("subscribed without tickets (header %q)", got)
	case <-time.After(100 * time.Millisecond):
	}
	tickets.Store([]string{"aa.bb"})
	select {
	case got := <-subscribed:
		if got != "aa.bb" {
			t.Errorf("subscribed with tickets %q, want aa.bb", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never subscribed once it had tickets")
	}
	<-done
}

// A consumer that answers strangers too subscribes without tickets, as it always has.
func TestPoolConsumerSubscribesWithoutTickets(t *testing.T) {
	o := streamingFreddie(t)
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	pc, _ := newTestPeerConnection(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumerState1(t, fsm, pc)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a pool consumer without tickets never got through state 1")
	}
}
