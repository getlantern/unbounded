//go:build !wasm

package clientcore

import (
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common/covertdtls"
)

// connectedPair signals two local peers to each other and returns the producer once ICE has
// connected.
func connectedPair(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	consumer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { consumer.Close() })
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { producer.Close() })
	if _, err := consumer.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}

	iceConnected := make(chan struct{}, 1)
	producer.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected {
			select {
			case iceConnected <- struct{}{}:
			default:
			}
		}
	})

	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(consumer)
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := producer.SetRemoteDescription(*consumer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := producer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered = webrtc.GatheringCompletePromise(producer)
	if err := producer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := consumer.SetRemoteDescription(*producer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-iceConnected:
	case <-time.After(15 * time.Second):
		t.Fatal("ICE never connected")
	}
	return producer
}

// ICE that has connected by the time the wait starts leaves less than grace before natFail, so the
// wait must outlast natFail.
func TestAwaitDataChannelExtendsOnceICEConnects(t *testing.T) {
	pc := connectedPair(t)
	const natFail, grace = 100 * time.Millisecond, time.Second

	established := make(chan *webrtc.DataChannel, 1)
	time.AfterFunc(3*natFail, func() { established <- &webrtc.DataChannel{} })
	start := time.Now()
	if _, ok := awaitDataChannel(context.Background(), pc, established, natFail, grace); !ok {
		t.Fatalf("gave up after %v; ICE connected, so it should wait %v", time.Since(start), grace)
	}
}

func TestAwaitDataChannelGivesUpAfterGrace(t *testing.T) {
	pc := connectedPair(t)
	const natFail, grace = 100 * time.Millisecond, 300 * time.Millisecond

	start := time.Now()
	if _, ok := awaitDataChannel(context.Background(), pc, nil, natFail, grace); ok {
		t.Fatal("nothing was established")
	}
	if waited := time.Since(start); waited < grace || waited > grace+time.Second {
		t.Fatalf("waited %v, want about %v", waited, grace)
	}
}

// Without ICE connecting, natFail alone bounds the wait.
func TestAwaitDataChannelKeepsNATFailWithoutICE(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	const natFail, grace = 100 * time.Millisecond, 5 * time.Second

	start := time.Now()
	if _, ok := awaitDataChannel(context.Background(), pc, nil, natFail, grace); ok {
		t.Fatal("nothing was established")
	}
	if waited := time.Since(start); waited > natFail+time.Second {
		t.Fatalf("waited %v; ICE never connected, so natFail %v should bound it", waited, natFail)
	}
}

// A long natFail with plenty left when ICE connects isn't shortened to grace.
func TestAwaitDataChannelNeverShortensNATFail(t *testing.T) {
	pc := connectedPair(t)
	const natFail, grace = time.Second, 100 * time.Millisecond

	established := make(chan *webrtc.DataChannel, 1)
	time.AfterFunc(5*grace, func() { established <- &webrtc.DataChannel{} })
	if _, ok := awaitDataChannel(context.Background(), pc, established, natFail, grace); !ok {
		t.Fatal("gave up before natFail")
	}
}

func TestAwaitDataChannelStopsWithCtx(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	if _, ok := awaitDataChannel(ctx, pc, nil, time.Minute, time.Minute); ok {
		t.Fatal("nothing was established")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %v after ctx ended", waited)
	}
}
