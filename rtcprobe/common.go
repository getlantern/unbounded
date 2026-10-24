package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/elliptic"
	"github.com/pion/webrtc/v4"
)

const (
	tokenHeader = "X-Rtcprobe-Token"
	maxTestSecs = 120
	// drainGrace is how long a receiver keeps counting after the sender says it's done, for
	// messages still in flight.
	drainGrace = 500 * time.Millisecond
)

// testSpec is what the probe asks the responder for, alongside its offer.
type testSpec struct {
	// Dir is "up" (probe sends) or "down" (responder sends).
	Dir string `json:"dir"`
	// Secs is how long to transfer; 0 tests connecting only.
	Secs int `json:"secs"`
	// Mode is "unreliable" (unordered, no retransmits, as Unbounded's data channel) or "reliable".
	Mode  string `json:"mode"`
	Chunk int    `json:"chunk"`
	// DTLS is the fingerprint variant both sides use: see newAPI.
	DTLS string `json:"dtls"`
	// ResponderRole is the DTLS role the answering responder takes: "client", as a browser
	// volunteer does, or "server".
	ResponderRole string `json:"responder_role"`
}

func (t testSpec) validate() error {
	switch {
	case t.Dir != "up" && t.Dir != "down":
		return fmt.Errorf("dir must be up or down, got %q", t.Dir)
	case t.Secs < 0 || t.Secs > maxTestSecs:
		return fmt.Errorf("secs must be 0..%d, got %d", maxTestSecs, t.Secs)
	case t.Mode != "unreliable" && t.Mode != "reliable":
		return fmt.Errorf("mode must be unreliable or reliable, got %q", t.Mode)
	case t.Chunk < 64 || t.Chunk > 65535:
		return fmt.Errorf("chunk must be 64..65535, got %d", t.Chunk)
	case t.ResponderRole != "client" && t.ResponderRole != "server":
		return fmt.Errorf("responder role must be client or server, got %q", t.ResponderRole)
	}
	_, err := newAPI(t.DTLS, nil)
	return err
}

type offerRequest struct {
	SDP  webrtc.SessionDescription `json:"sdp"`
	Test testSpec                  `json:"test"`
}

// doneMsg is what a sender reports on the control channel when it stops.
type doneMsg struct {
	Msgs  int64 `json:"msgs"`
	Bytes int64 `json:"bytes"`
}

// statsMsg is what a receiver counted.
type statsMsg struct {
	Msgs  int64 `json:"msgs"`
	Bytes int64 `json:"bytes"`
	// ActiveSecs runs from the first message received to the last.
	ActiveSecs float64 `json:"active_secs"`
}

// newAPI builds a pion API with a DTLS fingerprint variant:
//   - "default" is pion's own, as Unbounded's consumer uses.
//   - "alt" offers only the two ECDSA GCM suites and only X25519, which changes the cipher suite
//     and supported_groups lists every DTLS hello carries. It isn't meant to look like a browser,
//     only to differ from "default", so a censor matching pion's hello would treat the two arms
//     differently.
func newAPI(variant string, configure func(*webrtc.SettingEngine)) (*webrtc.API, error) {
	var se webrtc.SettingEngine
	switch variant {
	case "default":
	case "alt":
		se.SetDTLSCipherSuites(
			dtls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		)
		se.SetDTLSEllipticCurves(elliptic.X25519)
	default:
		return nil, fmt.Errorf("dtls variant must be default or alt, got %q", variant)
	}
	if configure != nil {
		configure(&se)
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)), nil
}

func dataChannelInit(mode string) *webrtc.DataChannelInit {
	if mode == "reliable" {
		return &webrtc.DataChannelInit{}
	}
	ordered := false
	var retransmits uint16
	return &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits}
}

// send fills dc for d, in messages of chunk bytes, keeping pion's send buffer between its low and
// high water marks so the rate is what SCTP's congestion control allows.
func send(dc *webrtc.DataChannel, d time.Duration, chunk int) (doneMsg, error) {
	const highWater, lowWater = 1 << 20, 256 << 10
	low := make(chan struct{}, 1)
	dc.SetBufferedAmountLowThreshold(lowWater)
	dc.OnBufferedAmountLow(func() {
		select {
		case low <- struct{}{}:
		default:
		}
	})
	buf := make([]byte, chunk)
	var sent doneMsg
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		if dc.BufferedAmount() > highWater {
			select {
			case <-low:
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		if err := dc.Send(buf); err != nil {
			return sent, err
		}
		sent.Msgs++
		sent.Bytes += int64(chunk)
	}
	return sent, nil
}

// tally counts what a receiver got.
type tally struct {
	mu          sync.Mutex
	msgs, bytes int64
	first, last time.Time
}

func (t *tally) add(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.msgs == 0 {
		t.first = now
	}
	t.last = now
	t.msgs++
	t.bytes += int64(n)
}

func (t *tally) stats() statsMsg {
	t.mu.Lock()
	defer t.mu.Unlock()
	return statsMsg{Msgs: t.msgs, Bytes: t.bytes, ActiveSecs: t.last.Sub(t.first).Seconds()}
}

// throughput returns megabits per second and the fraction of sent messages that didn't arrive.
func throughput(sent doneMsg, got statsMsg) (mbps, loss float64) {
	if got.ActiveSecs > 0 {
		mbps = float64(got.Bytes) * 8 / got.ActiveSecs / 1e6
	}
	if sent.Msgs > 0 {
		loss = 1 - float64(got.Msgs)/float64(sent.Msgs)
	}
	return mbps, loss
}

func sendJSON(dc *webrtc.DataChannel, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return dc.SendText(string(b))
}

// channels collects a peer's "data" and "ctl" data channels and reports when both are open.
type channels struct {
	mu        sync.Mutex
	data, ctl *webrtc.DataChannel
	opened    int
	open      chan struct{}
}

func newChannels() *channels {
	return &channels{open: make(chan struct{})}
}

// add registers dc, which must not be open yet, and calls onMessage for each message it gets.
func (c *channels) add(dc *webrtc.DataChannel, onMessage func(webrtc.DataChannelMessage)) {
	c.mu.Lock()
	switch dc.Label() {
	case "data":
		c.data = dc
	case "ctl":
		c.ctl = dc
	default:
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	dc.OnMessage(onMessage)
	dc.OnOpen(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.opened++
		if c.opened == 2 {
			close(c.open)
		}
	})
}

// marks records, once each, when named events happened, in milliseconds since start.
type marks struct {
	mu    sync.Mutex
	start time.Time
	m     map[string]int64
}

func newMarks() *marks {
	return &marks{start: time.Now(), m: make(map[string]int64)}
}

func (k *marks) set(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.m[name]; !ok {
		k.m[name] = time.Since(k.start).Milliseconds()
	}
}

func (k *marks) get(name string) (int64, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[name]
	return v, ok
}
