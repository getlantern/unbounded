package clientcore

import (
	"slices"
	"testing"
	"time"
)

// everyKey sets every recognized key to a valid value.
var everyKey = map[string]string{
	"ice_max_binding_requests":      "12",
	"stun_gather_timeout_ms":        "2000",
	"ice_disconnected_timeout_ms":   "3000",
	"ice_failed_timeout_ms":         "10000",
	"ice_keepalive_interval_ms":     "1000",
	"srflx_acceptance_min_wait_ms":  "1",
	"dtls_retransmit_ms":            "200",
	"sctp_rto_max_ms":               "3000",
	"sctp_max_receive_buffer_bytes": "4194304",
}

func TestParseWebRTCTuning(t *testing.T) {
	got, ignored := ParseWebRTCTuning(everyKey)
	want := WebRTCTuning{
		ICEMaxBindingRequests:  12,
		STUNGatherTimeout:      2 * time.Second,
		ICEDisconnectedTimeout: 3 * time.Second,
		ICEFailedTimeout:       10 * time.Second,
		ICEKeepaliveInterval:   time.Second,
		SrflxAcceptanceMinWait: time.Millisecond,
		DTLSRetransmitInterval: 200 * time.Millisecond,
		SCTPRTOMax:             3 * time.Second,
		SCTPMaxReceiveBuffer:   4 << 20,
	}
	if got != want || len(ignored) != 0 {
		t.Fatalf("got %+v ignored %v, want %+v", got, ignored, want)
	}
}

func TestParseWebRTCTuningEveryKeyIsTested(t *testing.T) {
	// TestParseWebRTCTuning sets every key in everyKey; this keeps a new key from going untested.
	for k := range tuningKeys {
		if _, ok := everyKey[k]; !ok {
			t.Errorf("tuning key %q is missing from everyKey", k)
		}
	}
}

// Unknown keys are how a server sends knobs newer clients understand, so they must be ignored,
// not rejected, and bad values must leave the default in place.
func TestParseWebRTCTuningIgnoresUnknownAndInvalid(t *testing.T) {
	got, ignored := ParseWebRTCTuning(map[string]string{
		"dtls_retransmit_ms":            "200",
		"from_a_newer_server":           "5",
		"sctp_rto_max_ms":               "abc",
		"stun_gather_timeout_ms":        "0",
		"ice_failed_timeout_ms":         "-5",
		"ice_max_binding_requests":      "101",
		"sctp_max_receive_buffer_bytes": "1073741824",
		"ice_keepalive_interval_ms":     "999999999",
	})
	if got != (WebRTCTuning{DTLSRetransmitInterval: 200 * time.Millisecond}) {
		t.Fatalf("only the valid key should apply, got %+v", got)
	}
	if len(ignored) != 7 || !slices.Contains(ignored, "from_a_newer_server") || !slices.IsSorted(ignored) {
		t.Fatalf("ignored %v", ignored)
	}
}

// A value below a key's floor would have pion retransmit in a tight loop or fail at once.
func TestParseWebRTCTuningIgnoresBelowMinimum(t *testing.T) {
	got, ignored := ParseWebRTCTuning(map[string]string{
		"dtls_retransmit_ms":            "1",
		"sctp_rto_max_ms":               "999",
		"stun_gather_timeout_ms":        "1",
		"sctp_max_receive_buffer_bytes": "1024",
	})
	if got != (WebRTCTuning{}) || len(ignored) != 4 {
		t.Fatalf("got %+v ignored %v", got, ignored)
	}
}

// A keepalive at or above the disconnected timeout, explicit or pion's 5s default, would let idle
// connections flap, so all three ICE liveness overrides are dropped.
func TestParseWebRTCTuningRejectsKeepaliveAtOrAboveDisconnected(t *testing.T) {
	for _, opts := range []map[string]string{
		{"ice_keepalive_interval_ms": "5000", "ice_failed_timeout_ms": "10000"},
		{"ice_keepalive_interval_ms": "3000", "ice_disconnected_timeout_ms": "2000"},
		{"ice_disconnected_timeout_ms": "1000"},
	} {
		got, ignored := ParseWebRTCTuning(opts)
		if got != (WebRTCTuning{}) || len(ignored) != 1 {
			t.Errorf("%v: got %+v ignored %v", opts, got, ignored)
		}
	}
	got, _ := ParseWebRTCTuning(map[string]string{"ice_keepalive_interval_ms": "4999"})
	if got.ICEKeepaliveInterval != 4999*time.Millisecond {
		t.Fatalf("keepalive below the default disconnected timeout should apply, got %+v", got)
	}
}

func TestParseWebRTCTuningEmpty(t *testing.T) {
	if got, ignored := ParseWebRTCTuning(nil); got != (WebRTCTuning{}) || ignored != nil {
		t.Fatalf("got %+v ignored %v", got, ignored)
	}
}

func TestCheckingForFillsTheWindow(t *testing.T) {
	// 25 timed checks fill 5s, doubled for the checks pion runs when candidates are added.
	if got := (WebRTCTuning{}).checkingFor(5 * time.Second).ICEMaxBindingRequests; got != 50 {
		t.Fatalf("5s should allow 50 checks, got %d", got)
	}
	if got := (WebRTCTuning{}).checkingFor(time.Hour).ICEMaxBindingRequests; got != maxICEBindingRequests {
		t.Fatalf("a long window should be capped at %d, got %d", maxICEBindingRequests, got)
	}
}

// A value from config is an explicit choice, so the window must not override it, and a peer with
// no window keeps pion's default.
func TestCheckingForKeepsExplicitAndZero(t *testing.T) {
	if got := (WebRTCTuning{ICEMaxBindingRequests: 50}).checkingFor(5 * time.Second); got.ICEMaxBindingRequests != 50 {
		t.Fatalf("an explicit value must win, got %d", got.ICEMaxBindingRequests)
	}
	if got := (WebRTCTuning{}).checkingFor(0); got != (WebRTCTuning{}) {
		t.Fatalf("no window should change nothing, got %+v", got)
	}
}
