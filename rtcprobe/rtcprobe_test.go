package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func testResponder(t *testing.T) probeConfig {
	t.Helper()
	r := &responder{token: "secret", slots: make(chan struct{}, 4)}
	srv := httptest.NewServer(httpHandler(r))
	t.Cleanup(srv.Close)
	return probeConfig{
		url:     srv.URL,
		token:   "secret",
		client:  srv.Client(),
		timeout: 15 * time.Second,
		test:    testSpec{Dir: "up", Secs: 1, Mode: "unreliable", Chunk: 1200, DTLS: "default", ResponderRole: "client"},
	}
}

func TestProbeAgainstResponder(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testSpec)
	}{
		{"up unreliable", func(*testSpec) {}},
		{"down reliable", func(s *testSpec) { s.Dir, s.Mode = "down", "reliable" }},
		{"alt dtls, responder as server", func(s *testSpec) { s.DTLS, s.ResponderRole = "alt", "server" }},
		{"covert dtls, probe as client", func(s *testSpec) { s.DTLS, s.ResponderRole = "covert", "server" }},
		{"covert dtls, probe as server", func(s *testSpec) { s.DTLS = "covert" }},
		{"short dtls retransmit", func(s *testSpec) { s.DTLSRetransmitMs = 100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testResponder(t)
			tc.edit(&cfg.test)
			r := attempt(1, cfg)
			if r.Outcome != "connected" || r.Error != "" {
				t.Fatalf("outcome %q, error %q", r.Outcome, r.Error)
			}
			if r.ICEMs == nil || r.DTLSMs == nil || r.OpenMs == nil {
				t.Fatalf("missing timings: %+v", r)
			}
			if r.Mbps == nil || *r.Mbps <= 0 || r.BytesThrough == 0 {
				t.Fatalf("no transfer measured: %+v", r)
			}
			if r.SendSecs < 0.9 {
				t.Fatalf("send_secs %v should cover the 1s transfer", r.SendSecs)
			}
			if r.DrainIncomplete {
				t.Fatalf("a loopback transfer should drain completely: %+v", r)
			}
		})
	}
}

func TestProbeConnectOnly(t *testing.T) {
	cfg := testResponder(t)
	cfg.test.Secs = 0
	r := attempt(1, cfg)
	if r.Outcome != "connected" || r.Mbps != nil {
		t.Fatalf("outcome %q, mbps %v", r.Outcome, r.Mbps)
	}
}

func TestResponderRejectsBadToken(t *testing.T) {
	cfg := testResponder(t)
	cfg.token = "wrong"
	if r := attempt(1, cfg); r.Outcome != "signal" {
		t.Fatalf("a bad token should fail signaling, got %q", r.Outcome)
	}
}

func TestSignalingClientPinsCertificate(t *testing.T) {
	r := &responder{token: "secret", slots: make(chan struct{}, 4)}
	srv := httptest.NewTLSServer(httpHandler(r))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)

	cfg := probeConfig{url: srv.URL, token: "secret", timeout: 15 * time.Second,
		test: testSpec{Dir: "up", Secs: 0, Mode: "unreliable", Chunk: 1200, DTLS: "default", ResponderRole: "client"}}
	var err error
	if cfg.client, err = signalingClient(srv.URL, hex.EncodeToString(sum[:]), false); err != nil {
		t.Fatal(err)
	}
	if r := attempt(1, cfg); r.Outcome != "connected" {
		t.Fatalf("the pinned certificate should be accepted, got %q %q", r.Outcome, r.Error)
	}

	wrong := sha256.Sum256([]byte("not it"))
	if cfg.client, err = signalingClient(srv.URL, hex.EncodeToString(wrong[:]), false); err != nil {
		t.Fatal(err)
	}
	if r := attempt(1, cfg); r.Outcome != "signal" {
		t.Fatalf("a mismatched certificate should fail signaling, got %q", r.Outcome)
	}
}

func TestSignalingClientRefusesRemotePlainHTTP(t *testing.T) {
	if _, err := signalingClient("http://203.0.113.5:8080", "", false); err == nil {
		t.Fatal("plain HTTP to a remote responder should be refused")
	}
	for _, u := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"} {
		if _, err := signalingClient(u, "", false); err != nil {
			t.Errorf("%s: loopback should be allowed: %v", u, err)
		}
	}
	if _, err := signalingClient("http://203.0.113.5:8080", "", true); err != nil {
		t.Errorf("-allow-http should allow it: %v", err)
	}
}

// A peer that opens two ctl channels and no data channel used to leave the responder sending on
// a nil channel, which panics and takes every session down with it.
func TestResponderSurvivesDuplicateChannels(t *testing.T) {
	cfg := testResponder(t)
	cfg.test.Dir = "down"

	api, err := newAPI("default", nil)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	for range 2 {
		if _, err := pc.CreateDataChannel("ctl", nil); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	answer, err := postOffer(context.Background(), cfg, offerRequest{SDP: *pc.LocalDescription(), Test: cfg.test})
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(*answer); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)

	// Still serving.
	if r := attempt(1, cfg); r.Outcome != "connected" {
		t.Fatalf("the responder should still serve, got %q %q", r.Outcome, r.Error)
	}
}

func TestThroughputCountsStalls(t *testing.T) {
	// Ten seconds of sending, but data arrived only during the first.
	mbps, burst, loss := throughput(doneMsg{Msgs: 1000, Bytes: 1_250_000, SendSecs: 10},
		statsMsg{Msgs: 100, Bytes: 125_000, ActiveSecs: 1})
	if mbps != 0.1 || burst != 1 || loss != 0.9 {
		t.Fatalf("mbps=%v burst=%v loss=%v, want 0.1, 1, 0.9", mbps, burst, loss)
	}
}

func TestSettleWaitsForQuietAfterDone(t *testing.T) {
	var got tally
	got.add(100)
	time.Sleep(unreliableQuiet + 100*time.Millisecond)
	// done arrives after a long quiet; a straggler lands shortly after.
	go func() {
		time.Sleep(200 * time.Millisecond)
		got.add(100)
	}()
	if s := got.settle(doneMsg{Msgs: 2}, false); s.Msgs != 2 || !s.Complete {
		t.Fatalf("settle should count the straggler, got %+v", s)
	}
}

func TestSignalingClientRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed, carrying token %q", r.Header.Get(tokenHeader))
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.RedirectHandler(target.URL+"/offer", http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)

	client, err := signalingClient(redirect.URL, "", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := probeConfig{url: redirect.URL, token: "secret", client: client}
	if _, err := postOffer(context.Background(), cfg, offerRequest{}); err == nil {
		t.Fatal("a redirect should fail signaling")
	}
}
