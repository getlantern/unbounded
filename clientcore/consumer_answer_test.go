package clientcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// negotiatedConsumer returns a consumer peer connection with its offer applied, as state 2 leaves
// it, and the producer's answer to that offer.
func negotiatedConsumer(t *testing.T) (*webrtc.PeerConnection, webrtc.SessionDescription) {
	t.Helper()
	consumer, _ := newTestPeerConnection(t)
	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	producer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { producer.Close() })
	if err := producer.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, err := producer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	return consumer, answer
}

// runICEState runs consumer state 3, which POSTs the consumer's candidates, against discoverySrv.
func runICEState(t *testing.T, discoverySrv string, pc *webrtc.PeerConnection, answer webrtc.SessionDescription) int {
	t.Helper()
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = discoverySrv
	o.ErrorBackoff = 10 * time.Millisecond
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	next, _ := fsm.state[3](context.Background(), fsm.com, []interface{}{
		pc,
		"answer-id",
		[]webrtc.ICECandidate{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
		answer,
	})
	return next
}

// Applying the answer starts the consumer's ICE checks, so it must wait until freddie has
// delivered the consumer's candidates to the producer, which only then starts its own.
func TestConsumerAppliesAnswerOnlyAfterICEDelivered(t *testing.T) {
	pc, answer := negotiatedConsumer(t)
	posted := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(posted)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	// Cleanups run last-registered first, so a failed test releases the handler before srv.Close
	// waits for it.
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	done := make(chan int, 1)
	go func() { done <- runICEState(t, srv.URL, pc, answer) }()
	select {
	case <-posted:
	case <-time.After(5 * time.Second):
		t.Fatal("state 3 never POSTed the candidates")
	}
	if pc.RemoteDescription() != nil {
		t.Fatal("the answer was applied before the candidates were delivered")
	}
	unblock()
	if next := <-done; next != 4 {
		t.Fatalf("state 3 went to state %d, want 4", next)
	}
	if got := pc.RemoteDescription(); got == nil || got.SDP != answer.SDP {
		t.Fatal("the answer was not applied once the candidates were delivered")
	}
}

func TestConsumerNeverAppliesAnswerWhenICENotDelivered(t *testing.T) {
	t.Run("producer hung up", func(t *testing.T) {
		pc, answer := negotiatedConsumer(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		if next := runICEState(t, srv.URL, pc, answer); next != 0 {
			t.Fatalf("state 3 went to state %d, want 0", next)
		}
		if pc.RemoteDescription() != nil {
			t.Fatal("the answer was applied though the producer hung up")
		}
	})
	t.Run("request failed", func(t *testing.T) {
		pc, answer := negotiatedConsumer(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		if next := runICEState(t, srv.URL, pc, answer); next != 0 {
			t.Fatalf("state 3 went to state %d, want 0", next)
		}
		if pc.RemoteDescription() != nil {
			t.Fatal("the answer was applied though the candidates never reached freddie")
		}
	})
}
