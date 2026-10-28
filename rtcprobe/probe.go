package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

type probeConfig struct {
	url     string
	token   string
	client  *http.Client
	stun    []string
	timeout time.Duration
	test    testSpec
}

// result is one attempt. Timings are milliseconds since the attempt started; a missing timing
// means that step never happened.
type result struct {
	Attempt int       `json:"attempt"`
	Time    time.Time `json:"time"`
	DTLS    string    `json:"dtls"`
	Mode    string    `json:"mode"`
	Dir     string    `json:"dir"`
	// Outcome is "connected", or the step it stalled at: "gather", "signal", "ice", "dtls" or
	// "sctp" (DTLS done but the data channels never opened).
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	// Srflx is the probe's public addresses as its STUN servers saw them, to confirm the vantage
	// point is where it's meant to be.
	Srflx     []string `json:"srflx,omitempty"`
	GatherMs  *int64   `json:"gather_ms,omitempty"`
	SignalMs  *int64   `json:"signal_ms,omitempty"`
	ICEMs     *int64   `json:"ice_ms,omitempty"`
	DTLSMs    *int64   `json:"dtls_ms,omitempty"`
	OpenMs    *int64   `json:"open_ms,omitempty"`
	ICEState  string   `json:"ice_state"`
	DTLSState string   `json:"dtls_state"`
	// Mbps is over the whole transfer; BurstMbps only while data was arriving.
	Mbps         *float64 `json:"mbps,omitempty"`
	BurstMbps    *float64 `json:"burst_mbps,omitempty"`
	Loss         *float64 `json:"loss,omitempty"`
	BytesThrough int64    `json:"bytes_through,omitempty"`
	SendSecs     float64  `json:"send_secs,omitempty"`
	// DrainIncomplete means the receiver stopped counting while data was still due, so Loss
	// overstates what was lost.
	DrainIncomplete bool `json:"drain_incomplete,omitempty"`
}

func probe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	rawURL := fs.String("url", "", "responder base URL, e.g. http://203.0.113.5:8080 (required)")
	token := fs.String("token", "", "the responder's shared secret (required)")
	n := fs.Int("n", 10, "attempts")
	interval := fs.Duration("interval", 2*time.Second, "pause between attempts")
	stun := fs.String("stun", "stun:stun.l.google.com:19302,stun:stun.cloudflare.com:3478", "comma-separated STUN URLs")
	timeout := fs.Duration("timeout", 30*time.Second, "time allowed for one attempt to open its data channels")
	dir := fs.String("dir", "up", "transfer direction: up (probe sends) or down")
	secs := fs.Int("secs", 10, "transfer seconds; 0 skips the transfer")
	mode := fs.String("mode", "unreliable", "data channel mode: unreliable (unordered, no retransmits, as Unbounded) or reliable")
	chunk := fs.Int("chunk", 1200, "message size in bytes; 1200 is about one QUIC packet, as Unbounded sends")
	dtlsVariant := fs.String("dtls", "default", "DTLS fingerprint variant: default, alt or covert (covert shapes only the ClientHello)")
	role := fs.String("responder-role", "client", "DTLS role the responder takes: client (as a browser volunteer) or server")
	retransmit := fs.Duration("dtls-retransmit", 0, "initial DTLS handshake retransmission interval for both sides; 0 keeps pion's default (1s)")
	pin := fs.String("pin", "", "hex SHA-256 of the responder's certificate, as `rtcprobe serve -tls` logs it")
	allowHTTP := fs.Bool("allow-http", false, "allow plain HTTP to a non-loopback responder, which sends the token in the clear")
	fs.Parse(args)

	if *rawURL == "" || *token == "" {
		return errors.New("-url and -token are required")
	}
	client, err := signalingClient(*rawURL, *pin, *allowHTTP)
	if err != nil {
		return err
	}
	retransmitMs, err := durationMs(*retransmit, 5*time.Second)
	if err != nil {
		return fmt.Errorf("-dtls-retransmit: %w", err)
	}
	cfg := probeConfig{
		url:     strings.TrimRight(*rawURL, "/"),
		token:   *token,
		client:  client,
		timeout: *timeout,
		test: testSpec{
			Dir: *dir, Secs: *secs, Mode: *mode, Chunk: *chunk, DTLS: *dtlsVariant, ResponderRole: *role,
			DTLSRetransmitMs: retransmitMs,
		},
	}
	for _, s := range strings.Split(*stun, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.stun = append(cfg.stun, s)
		}
	}
	if err := cfg.test.validate(); err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	var results []result
	for i := 1; i <= *n; i++ {
		r := attempt(i, cfg)
		results = append(results, r)
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("writing results: %w", err)
		}
		if i < *n {
			time.Sleep(*interval)
		}
	}
	summarize(results)
	return nil
}

func attempt(n int, cfg probeConfig) (r result) {
	t := cfg.test
	r = result{Attempt: n, Time: time.Now().UTC(), DTLS: t.DTLS, Mode: t.Mode, Dir: t.Dir}
	k := newMarks()
	ms := func(name string) *int64 {
		if v, ok := k.get(name); ok {
			return &v
		}
		return nil
	}

	api, err := newAPI(t.DTLS, func(se *webrtc.SettingEngine) { applyRetransmit(t, se) })
	if err != nil {
		r.Outcome, r.Error = "gather", err.Error()
		return r
	}
	var servers []webrtc.ICEServer
	if len(cfg.stun) > 0 {
		servers = []webrtc.ICEServer{{URLs: cfg.stun}}
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		r.Outcome, r.Error = "gather", err.Error()
		return r
	}
	defer pc.Close()

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected {
			k.set("ice")
		}
	})
	pc.SCTP().Transport().OnStateChange(func(s webrtc.DTLSTransportState) {
		if s == webrtc.DTLSTransportStateConnected {
			k.set("dtls")
		}
	})
	defer func() {
		r.ICEState = pc.ICEConnectionState().String()
		r.DTLSState = pc.SCTP().Transport().State().String()
		r.GatherMs, r.SignalMs, r.ICEMs, r.DTLSMs, r.OpenMs = ms("gather"), ms("signal"), ms("ice"), ms("dtls"), ms("open")
	}()

	got := &tally{}
	doneCh := make(chan doneMsg, 1)
	statsCh := make(chan statsMsg, 1)
	chans := newChannels()
	for _, label := range []string{"data", "ctl"} {
		init := &webrtc.DataChannelInit{}
		if label == "data" {
			init = dataChannelInit(t.Mode)
		}
		dc, err := pc.CreateDataChannel(label, init)
		if err != nil {
			r.Outcome, r.Error = "gather", err.Error()
			return r
		}
		onMessage := func(m webrtc.DataChannelMessage) { got.add(len(m.Data)) }
		if label == "ctl" {
			// The responder sends doneMsg after a download and statsMsg after an upload.
			onMessage = func(m webrtc.DataChannelMessage) {
				if t.Dir == "down" {
					var d doneMsg
					if json.Unmarshal(m.Data, &d) == nil {
						select {
						case doneCh <- d:
						default:
						}
					}
					return
				}
				var s statsMsg
				if json.Unmarshal(m.Data, &s) == nil {
					select {
					case statsCh <- s:
					default:
					}
				}
			}
		}
		chans.add(dc, onMessage)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		r.Outcome, r.Error = "gather", err.Error()
		return r
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		r.Outcome, r.Error = "gather", err.Error()
		return r
	}
	select {
	case <-gathered:
		k.set("gather")
	case <-ctx.Done():
		r.Outcome, r.Error = "gather", "ICE gathering timed out"
		return r
	}
	r.Srflx = srflxAddrs(pc.LocalDescription().SDP)

	answer, err := postOffer(ctx, cfg, offerRequest{SDP: *pc.LocalDescription(), Test: t})
	if err != nil {
		r.Outcome, r.Error = "signal", err.Error()
		return r
	}
	if err := pc.SetRemoteDescription(*answer); err != nil {
		r.Outcome, r.Error = "signal", err.Error()
		return r
	}
	k.set("signal")

	select {
	case <-chans.open:
		k.set("open")
	case <-ctx.Done():
		switch {
		case !has(k, "ice"):
			r.Outcome = "ice"
		case !has(k, "dtls"):
			r.Outcome = "dtls"
		default:
			r.Outcome = "sctp"
		}
		return r
	}
	r.Outcome = "connected"
	if t.Secs == 0 {
		return r
	}

	// The transfer gets its own deadline: the attempt's timeout covered only connecting.
	wait := time.Duration(t.Secs)*time.Second + 15*time.Second
	var sent doneMsg
	var stats statsMsg
	switch t.Dir {
	case "up":
		sent, err = send(chans.data, time.Duration(t.Secs)*time.Second, t.Chunk)
		if err == nil {
			err = sendJSON(chans.ctl, sent)
		}
		if err != nil {
			r.Error = "send: " + err.Error()
			return r
		}
		select {
		case stats = <-statsCh:
		case <-time.After(wait):
			r.Error = "no stats from the responder"
			return r
		}
	case "down":
		select {
		case sent = <-doneCh:
			stats = got.settle(sent, t.Mode == "reliable")
		case <-time.After(wait):
			r.Error = "the responder never finished sending"
			return r
		}
	}
	mbps, burst, loss := throughput(sent, stats)
	r.Mbps, r.BurstMbps, r.Loss, r.BytesThrough = &mbps, &burst, &loss, stats.Bytes
	r.DrainIncomplete = !stats.Complete
	r.SendSecs = sent.SendSecs
	return r
}

func has(k *marks, name string) bool {
	_, ok := k.get(name)
	return ok
}

func postOffer(ctx context.Context, cfg probeConfig, offer offerRequest) (*webrtc.SessionDescription, error) {
	body, err := json.Marshal(offer)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.url+"/offer", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, cfg.token)
	res, err := cfg.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("responder: %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return &answer, nil
}

// srflxAddrs returns the address:port of each srflx candidate in sdp.
func srflxAddrs(sdp string) []string {
	var out []string
	for _, line := range strings.Split(sdp, "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "a="))
		if len(f) >= 8 && strings.HasPrefix(f[0], "candidate:") && f[6] == "typ" && f[7] == "srflx" {
			if a := f[4] + ":" + f[5]; !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

func summarize(results []result) {
	outcomes := map[string]int{}
	var dtls, open []int64
	var mbps []float64
	for _, r := range results {
		outcomes[r.Outcome]++
		if r.DTLSMs != nil && r.ICEMs != nil {
			dtls = append(dtls, *r.DTLSMs-*r.ICEMs)
		}
		if r.OpenMs != nil {
			open = append(open, *r.OpenMs)
		}
		if r.Mbps != nil {
			mbps = append(mbps, *r.Mbps)
		}
	}
	fmt.Fprintf(os.Stderr, "attempts=%d outcomes=%v\n", len(results), outcomes)
	fmt.Fprintf(os.Stderr, "median ms: dtls_after_ice=%v open=%v   median mbps=%.2f\n",
		median(dtls), median(open), medianF(mbps))
}

func median(v []int64) any {
	if len(v) == 0 {
		return "n/a"
	}
	slices.Sort(v)
	return v[len(v)/2]
}

func medianF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// signalingClient returns the client the probe signals with. The token travels in every request,
// so it refuses plain HTTP to anything but loopback unless allowHTTP. With pin, it accepts only
// the certificate whose SHA-256 that is, in place of CA verification.
func signalingClient(rawURL, pin string, allowHTTP bool) (*http.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		if ip := net.ParseIP(u.Hostname()); !allowHTTP && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("plain HTTP would send the token in the clear: run the responder with -tls and pass -pin, or -allow-http")
		}
		if pin != "" {
			return nil, errors.New("-pin needs an https URL")
		}
		return noRedirects(&http.Client{}), nil
	case "https":
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if pin == "" {
		return noRedirects(&http.Client{}), nil
	}
	want, err := hex.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("-pin must be a hex SHA-256")
	}
	tlsConfig := &tls.Config{
		// Verification is VerifyConnection's pin check, not a CA chain.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("responder sent no certificate")
			}
			if sum := sha256.Sum256(cs.PeerCertificates[0].Raw); !bytes.Equal(sum[:], want) {
				return errors.New("responder certificate doesn't match -pin")
			}
			return nil
		},
	}
	return noRedirects(&http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}), nil
}

// noRedirects stops c following redirects, which would carry the token to wherever they point,
// including from https to http.
func noRedirects(c *http.Client) *http.Client {
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// durationMs converts d to whole milliseconds, refusing what the conversion would otherwise
// change silently: a negative value, one over limit, or a fraction of a millisecond, which would
// round 500us down to 0 and so to pion's default.
func durationMs(d, limit time.Duration) (int, error) {
	if d < 0 || d > limit || d%time.Millisecond != 0 {
		return 0, fmt.Errorf("must be a whole number of milliseconds from 0 to %s, got %s", limit, d)
	}
	return int(d / time.Millisecond), nil
}
