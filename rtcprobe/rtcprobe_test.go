package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func testResponder(t *testing.T) probeConfig {
	t.Helper()
	r := &responder{token: "secret", slots: make(chan struct{}, 4)}
	srv := httptest.NewServer(httpHandler(r))
	t.Cleanup(srv.Close)
	return probeConfig{
		url:     srv.URL,
		token:   "secret",
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
