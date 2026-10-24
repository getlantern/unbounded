package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

type responder struct {
	token     string
	publicIPs []string
	portMin   uint16
	portMax   uint16
	slots     chan struct{}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":8080", "HTTP address for signaling")
	token := fs.String("token", "", "shared secret probes must send (required)")
	publicIP := fs.String("public-ip", "", "public IP to advertise when the host sits behind 1:1 NAT (e.g. a cloud VM)")
	udpPorts := fs.String("udp-ports", "", "UDP port range for ICE, e.g. 50000-50100, to open in a firewall")
	maxSessions := fs.Int("max-sessions", 4, "concurrent sessions")
	fs.Parse(args)

	if *token == "" {
		return errors.New("-token is required: the responder sends traffic on request")
	}
	r := &responder{token: *token, slots: make(chan struct{}, *maxSessions)}
	if *publicIP != "" {
		if net.ParseIP(*publicIP) == nil {
			return fmt.Errorf("bad -public-ip %q", *publicIP)
		}
		r.publicIPs = []string{*publicIP}
	}
	if *udpPorts != "" {
		lo, hi, ok := strings.Cut(*udpPorts, "-")
		min, err1 := strconv.ParseUint(lo, 10, 16)
		max, err2 := strconv.ParseUint(hi, 10, 16)
		if !ok || err1 != nil || err2 != nil || min > max {
			return fmt.Errorf("bad -udp-ports %q", *udpPorts)
		}
		r.portMin, r.portMax = uint16(min), uint16(max)
	}

	srv := &http.Server{Addr: *listen, Handler: httpHandler(r), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("rtcprobe responder listening", "addr", *listen)
	return srv.ListenAndServe()
}

func httpHandler(r *responder) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/offer", r.handleOffer)
	return mux
}

func (r *responder) handleOffer(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Header.Get(tokenHeader)), []byte(r.token)) != 1 {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	var offer offerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 64<<10)).Decode(&offer); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := offer.Test.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case r.slots <- struct{}{}:
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}

	pc, sess, answer, err := r.answer(offer)
	if err != nil {
		<-r.slots
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)

	go func() {
		defer func() { <-r.slots }()
		r.session(pc, sess, req.RemoteAddr)
	}()
}

// answer sets up a peer connection for offer and returns it with its answer, ICE gathering done.
// It registers the data channel handlers before applying the offer, so session can't miss them.
func (r *responder) answer(offer offerRequest) (*webrtc.PeerConnection, *session, *webrtc.SessionDescription, error) {
	role := webrtc.DTLSRoleClient
	if offer.Test.ResponderRole == "server" {
		role = webrtc.DTLSRoleServer
	}
	api, err := newAPI(offer.Test.DTLS, func(se *webrtc.SettingEngine) {
		se.SetAnsweringDTLSRole(role)
		if len(r.publicIPs) > 0 {
			se.SetNAT1To1IPs(r.publicIPs, webrtc.ICECandidateTypeHost)
		}
		if r.portMax > 0 {
			se.SetEphemeralUDPPortRange(r.portMin, r.portMax)
		}
	})
	if err != nil {
		return nil, nil, nil, err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*webrtc.PeerConnection, *session, *webrtc.SessionDescription, error) {
		pc.Close()
		return nil, nil, nil, err
	}
	sess := newSession(offer.Test)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { sess.chans.add(dc, sess.onMessage(dc)) })
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateClosed || st == webrtc.PeerConnectionStateFailed ||
			st == webrtc.PeerConnectionStateDisconnected {
			sess.closeOnce.Do(func() { close(sess.closed) })
		}
	})

	if err := pc.SetRemoteDescription(offer.SDP); err != nil {
		return fail(err)
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(ans); err != nil {
		return fail(err)
	}
	select {
	case <-gathered:
	case <-time.After(10 * time.Second):
		return fail(errors.New("ICE gathering timed out"))
	}
	return pc, sess, pc.LocalDescription(), nil
}

// session is one probe's test on the responder.
type session struct {
	test      testSpec
	chans     *channels
	got       tally
	done      chan doneMsg // the probe finished sending
	closed    chan struct{}
	closeOnce sync.Once
}

func newSession(t testSpec) *session {
	return &session{test: t, chans: newChannels(), done: make(chan doneMsg, 1), closed: make(chan struct{})}
}

func (s *session) onMessage(dc *webrtc.DataChannel) func(webrtc.DataChannelMessage) {
	if dc.Label() == "data" {
		return func(m webrtc.DataChannelMessage) { s.got.add(len(m.Data)) }
	}
	return func(m webrtc.DataChannelMessage) {
		var d doneMsg
		if json.Unmarshal(m.Data, &d) == nil {
			select {
			case s.done <- d:
			default:
			}
		}
	}
}

// session runs the test the probe asked for, logs what the responder saw, and closes pc.
func (r *responder) session(pc *webrtc.PeerConnection, s *session, remote string) {
	defer pc.Close()
	t := s.test
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(t.Secs)*time.Second+time.Minute)
	defer cancel()
	log := slog.With("remote", remote, "dir", t.Dir, "mode", t.Mode, "dtls", t.DTLS, "responder_role", t.ResponderRole)

	select {
	case <-s.chans.open:
	case <-s.closed:
		log.Info("closed before the data channels opened", "ice", pc.ICEConnectionState().String())
		return
	case <-ctx.Done():
		log.Info("timed out before the data channels opened",
			"ice", pc.ICEConnectionState().String(), "pc", pc.ConnectionState().String())
		return
	}

	switch {
	case t.Secs == 0:
		log.Info("connected")
	case t.Dir == "up":
		select {
		case sent := <-s.done:
			time.Sleep(drainGrace)
			got := s.got.stats()
			if err := sendJSON(s.chans.ctl, got); err != nil {
				log.Info("couldn't send stats", "error", err)
			}
			mbps, loss := throughput(sent, got)
			log.Info("received", "mbps", mbps, "loss", loss, "bytes", got.Bytes)
		case <-s.closed:
			log.Info("closed during the test")
			return
		case <-ctx.Done():
			log.Info("timed out during the test")
			return
		}
	case t.Dir == "down":
		sent, err := send(s.chans.data, time.Duration(t.Secs)*time.Second, t.Chunk)
		if err != nil {
			log.Info("send failed", "error", err, "bytes", sent.Bytes)
			return
		}
		if err := sendJSON(s.chans.ctl, sent); err != nil {
			log.Info("couldn't send done", "error", err)
			return
		}
		log.Info("sent", "bytes", sent.Bytes)
	}
	// Let the probe read what was sent on the control channel before closing.
	select {
	case <-s.closed:
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
	}
}
