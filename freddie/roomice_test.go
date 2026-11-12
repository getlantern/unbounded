package freddie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

var testICE = []webrtc.ICEServer{{URLs: []string{"turns:turn.example:443?transport=tcp"}, Username: "u", Credential: "c"}}

// genesisResponse posts a genesis with form's fields and returns Freddie's response body: the
// offer a consumer sent, or "" if none did within the request's lifetime.
func (s *testServer) genesisResponse(form url.Values) chan string {
	form.Set("data", "{}")
	if form.Get("send-to") == "" {
		form.Set("send-to", common.SignalSendToGenesis)
	}
	form.Set("type", strconv.Itoa(int(common.SignalMsgGenesis)))
	out := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
		if err != nil {
			out <- ""
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(common.VersionHeader, common.Version)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- ""
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		out <- strings.TrimSpace(string(body))
	}()
	return out
}

// answerGenesis waits for a genesis on consumer, answers it with an offer, and returns the
// genesis as the consumer decoded it.
func (s *testServer) answerGenesis(consumer chan string) common.SignalMsg {
	s.t.Helper()
	var g common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the genesis never reached the consumer")
	}
	go s.postOffer(g.ReplyTo, "RU", common.Version)
	return g
}

// answerOffer decodes the offer the producer got, raw, and answers it, so the offer request
// returns rather than waiting out its TTL.
func answerOffer(t *testing.T, raw string) common.SignalMsg {
	t.Helper()
	var m common.SignalMsg
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decoding the offer the producer got: %v", err)
	}
	signalTable.SendOnce(m.ReplyTo, "{}")
	return m
}

func TestRoomICE_StampedOnRoomGenesisAndOffer(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	var calls atomic.Int32
	s.f.RoomICEServers = func(context.Context) []webrtc.ICEServer { calls.Add(1); return testICE }
	paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)

	resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
	g := s.answerGenesis(paired)
	if !reflect.DeepEqual(g.ICEServers, testICE) {
		t.Fatalf("room genesis ICE servers: got %+v", g.ICEServers)
	}
	offer := answerOffer(t, <-resp)
	if offer.Type != common.SignalMsgOffer || !reflect.DeepEqual(offer.ICEServers, testICE) {
		t.Fatalf("offer to the producer: type %v, ICE servers %+v", offer.Type, offer.ICEServers)
	}
	if calls.Load() != 1 {
		t.Fatalf("the hook ran %d times for one genesis, want 1", calls.Load())
	}
}

func TestRoomICE_NotOnPool(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	s.f.RoomICEServers = func(context.Context) []webrtc.ICEServer { return testICE }
	pool := s.subscribe("RU", "")
	s.waitForConsumers(1)

	for name, form := range map[string]url.Values{
		"no rooms": {},
		// Nobody is in Q's room, so the genesis falls through to the pool.
		"no room consumer": roomGenesisForm([]string{k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, nil),
	} {
		resp := s.genesisResponse(form)
		g := s.answerGenesis(pool)
		if g.ICEServers != nil {
			t.Errorf("%s: pool genesis carried ICE servers", name)
		}
		if offer := answerOffer(t, <-resp); offer.ICEServers != nil {
			t.Errorf("%s: pool offer carried ICE servers", name)
		}
		s.waitForConsumers(1)
	}
}

func TestRoomICE_NilHookAndEmptyResult(t *testing.T) {
	for name, hook := range map[string]func(context.Context) []webrtc.ICEServer{
		"nil hook":     nil,
		"empty result": func(context.Context) []webrtc.ICEServer { return nil },
	} {
		k := newRoomKeys(t)
		s := startRoomServer(t, k)
		s.f.RoomICEServers = hook
		paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
		s.waitForConsumers(1)
		resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
		g := s.answerGenesis(paired)
		raw := <-resp
		answerOffer(t, raw)
		if g.ICEServers != nil || strings.Contains(raw, "ICEServers") {
			t.Errorf("%s: ICE servers stamped", name)
		}
		s.hangUp()
	}
}

func TestRecordProducerOutcome_LabelsPath(t *testing.T) {
	for reported, want := range map[string]string{"relay": "relay", "direct": "direct", "": "unreported", "x": "unreported"} {
		form := url.Values{common.PrevOutcomeField: {common.OutcomeConnected}}
		if reported != "" {
			form.Set(common.PrevPathField, reported)
		}
		got := outcomeLabels(t, form, common.Version, "path")
		if got[want] != 1 || len(got) != 1 {
			t.Errorf("prev-path %q: got %v, want %s", reported, got, want)
		}
	}
}
