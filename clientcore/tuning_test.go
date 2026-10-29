//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common/covertdtls"
)

func TestWebRTCTuningZeroSetsNothing(t *testing.T) {
	var se webrtc.SettingEngine
	if (WebRTCTuning{}).apply(&se) {
		t.Fatal("the zero tuning must leave pion's defaults alone")
	}
}

func allTuning() WebRTCTuning {
	return WebRTCTuning{
		ICEMaxBindingRequests:  12,
		STUNGatherTimeout:      2 * time.Second,
		ICEDisconnectedTimeout: 3 * time.Second,
		SrflxAcceptanceMinWait: time.Millisecond,
		DTLSRetransmitInterval: 200 * time.Millisecond,
		SCTPRTOMax:             3 * time.Second,
		SCTPMaxReceiveBuffer:   4 << 20,
	}
}

func TestWebRTCTuningEachFieldCounts(t *testing.T) {
	for _, tu := range []WebRTCTuning{
		{ICEMaxBindingRequests: 1}, {STUNGatherTimeout: 1}, {ICEDisconnectedTimeout: 1},
		{ICEFailedTimeout: 1}, {ICEKeepaliveInterval: 1}, {SrflxAcceptanceMinWait: 1},
		{DTLSRetransmitInterval: 1}, {SCTPRTOMax: 1}, {SCTPMaxReceiveBuffer: 1},
	} {
		var se webrtc.SettingEngine
		if !tu.apply(&se) {
			t.Errorf("%+v should count as tuned", tu)
		}
	}
}

// Tuned consumer and producer peer connections must still connect to each other, so the
// settings reach a real handshake rather than only a SettingEngine.
func TestTunedPeersConnect(t *testing.T) {
	api, err := newWebRTCAPI(nil, allTuning())
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, allTuning(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	open := make(chan struct{})
	dc, err := consumer.CreateDataChannel("data", nil)
	if err != nil {
		t.Fatal(err)
	}
	dc.OnOpen(func() { close(open) })

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
	case <-open:
	case <-time.After(15 * time.Second):
		t.Fatal("tuned peers never opened a data channel")
	}
}

// pion runs an extra pass over every pair whenever a candidate is added, which spends the same
// budget as the timed checks. A pair formed by the first remote candidate must still be checking
// near the end of the window after more candidates arrive one at a time, as a producer adds them.
func TestCheckingForOutlastsCandidateArrivals(t *testing.T) {
	const window = 2 * time.Second
	budget := WebRTCTuning{}.checkingFor(window).ICEMaxBindingRequests
	agent, err := ice.NewAgent(&ice.AgentConfig{
		NetworkTypes:       []ice.NetworkType{ice.NetworkTypeUDP4},
		CandidateTypes:     []ice.CandidateType{ice.CandidateTypeHost},
		IncludeLoopback:    true,
		MaxBindingRequests: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	gathered := make(chan struct{})
	if err := agent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			close(gathered)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.GatherCandidates(); err != nil {
		t.Fatal(err)
	}
	<-gathered

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = agent.Dial(ctx, "remoteufrag", "remotepasswordremotepassword") }()

	// Silent remote candidates: bound, so checks aren't refused, but never answered.
	addRemote := func() ice.Candidate {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		c, err := ice.NewCandidateHost(&ice.CandidateHostConfig{
			Network:   "udp",
			Address:   "127.0.0.1",
			Port:      conn.LocalAddr().(*net.UDPAddr).Port,
			Component: ice.ComponentRTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.AddRemoteCandidate(c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	start := time.Now()
	first := addRemote()
	for range 5 {
		time.Sleep(30 * time.Millisecond)
		addRemote()
	}
	time.Sleep(window*9/10 - time.Since(start))

	stats := agent.GetCandidatePairsStats()
	// Taken after reading the stats, so a failure counts only if it happened within the window
	// however late this goroutine woke up.
	if elapsed := time.Since(start); elapsed >= window {
		t.Skipf("read the pairs %v after the first candidate, too late to tell", elapsed)
	}
	checking := 0
	for _, s := range stats {
		if s.RemoteCandidateID != first.ID() {
			continue
		}
		if s.State == ice.CandidatePairStateFailed {
			t.Fatalf("pair failed after %d checks, before the %v window ended", s.RequestsSent, window)
		}
		checking++
	}
	if checking == 0 {
		t.Fatal("no pair with the first remote candidate")
	}
}
