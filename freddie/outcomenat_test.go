package freddie

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

func TestNATClass_OutcomeLabel(t *testing.T) {
	for want, class := range map[string]natClass{
		"no_srflx":         {nat: natNoSrflx},
		"symmetric":        {nat: natSymmetric},
		"hidden_symmetric": {nat: natSymmetric, baseHidden: true},
		"hidden":           {nat: natUndetermined, baseHidden: true},
		"port_preserved":   {nat: natUndetermined, portPreserved: true},
		"port_changed":     {nat: natUndetermined},
	} {
		if got := class.outcomeLabel(); got != want {
			t.Errorf("%+v: got %q, want %q", class, got, want)
		}
	}
}

func TestOutcomeNAT(t *testing.T) {
	for _, tc := range []struct {
		reported bool
		nat      string
		want     string
	}{
		{false, "port_changed", "unreported"},
		{true, "", "unknown"},
		{true, outcomeNATNone, "none"},
		{true, "port_changed", "port_changed"},
	} {
		if got := outcomeNAT(tc.reported, tc.nat); got != tc.want {
			t.Errorf("outcomeNAT(%v, %q) = %q, want %q", tc.reported, tc.nat, got, tc.want)
		}
	}
}

func newOffer() offerConsumer {
	return offerConsumer{version: common.Version, volunteerNAT: outcomeNATNone, consumerNAT: outcomeNATNone}
}

func TestOfferConsumerCache_LinksBothNATsToTheOffer(t *testing.T) {
	c := newOfferConsumerCache(time.Now)
	c.add("offer", newOffer())

	// The consumer's ICE message can only be linked once Freddie has seen the answer it replies to.
	c.consumerICE("answer", "port_changed")
	c.answered("offer", "answer", "hidden_symmetric")
	c.consumerICE("answer", "port_changed")

	got := c.take("offer")
	if got.volunteerNAT != "hidden_symmetric" || got.consumerNAT != "port_changed" || got.version != common.Version {
		t.Fatalf("got %+v", got)
	}
	if len(c.answers) != 0 {
		t.Fatalf("taking an offer must forget its answer, %d remain", len(c.answers))
	}
}

func TestOfferConsumerCache_UnseenPeerStaysNone(t *testing.T) {
	c := newOfferConsumerCache(time.Now)
	c.add("offer", newOffer())
	// An answer without candidates still links the consumer's ICE message, but says nothing about
	// the volunteer's NAT.
	c.answered("offer", "answer", "")
	if got := c.take("offer"); got.volunteerNAT != outcomeNATNone || got.consumerNAT != outcomeNATNone {
		t.Fatalf("got %+v, want none for both peers", got)
	}
}

func TestOfferConsumerCache_IgnoresUnknownOffersAndAnswers(t *testing.T) {
	c := newOfferConsumerCache(time.Now)
	c.answered("missing", "answer", "port_changed")
	c.consumerICE("answer", "port_changed")
	if len(c.m) != 0 || len(c.answers) != 0 {
		t.Fatalf("an answer to an unknown offer must not be remembered, have %d offers, %d answers", len(c.m), len(c.answers))
	}
}

func TestOfferConsumerCache_FirstAnswerWins(t *testing.T) {
	c := newOfferConsumerCache(time.Now)
	c.add("offer", newOffer())
	c.answered("offer", "first", "port_changed")
	// A second answer to the same offer gets a 404, and must not unlink the delivered one.
	c.answered("offer", "second", "port_preserved")
	c.consumerICE("second", "no_srflx")
	c.consumerICE("first", "port_preserved")
	if len(c.answers) != 1 {
		t.Fatalf("a rejected answer must not be remembered, answers %v", c.answers)
	}
	if got := c.take("offer"); got.volunteerNAT != "port_changed" || got.consumerNAT != "port_preserved" {
		t.Fatalf("got %+v", got)
	}
}

func TestOfferConsumerCache_EvictionForgetsAnswer(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })
	c.add("old", newOffer())
	c.answered("old", "answer", "port_changed")
	now = now.Add(offerConsumerTTL)
	c.add("new", newOffer())
	if len(c.answers) != 0 {
		t.Fatalf("an expired offer's answer must be dropped with it, answers %v", c.answers)
	}
}

// post posts a signaling message, with the current VersionHeader unless header sets one, and
// returns the response's status and body.
func (s *testServer) post(sendTo string, msgType common.SignalMsgType, data string, header http.Header) (int, []byte) {
	form := url.Values{
		"data":    {data},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(msgType))},
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Error(err)
		return 0, nil
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if req.Header.Get(common.VersionHeader) == "" {
		req.Header.Set(common.VersionHeader, common.Version)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

// natLabels returns the consumer_nat and volunteer_nat labels for an attempt that answered offerID.
func (s *testServer) natLabels(offerID string) (consumer, volunteer string) {
	for _, kv := range s.f.attemptConsumerAttrs(offerID) {
		switch kv.Key {
		case "consumer_nat":
			consumer = kv.Value.AsString()
		case "volunteer_nat":
			volunteer = kv.Value.AsString()
		}
	}
	return consumer, volunteer
}

// The full exchange through Freddie: the consumer's offer, the volunteer's answer to it, and the
// consumer's ICE message to the answer.
func TestSignaling_LabelsOutcomeWithBothNATs(t *testing.T) {
	s := startTestServer(t, headerHook)
	genesisID := "producer-genesis"
	producer := signalTable.Add(genesisID)
	defer signalTable.Delete(genesisID)

	type reply struct {
		status int
		body   []byte
	}
	offerDone := make(chan reply, 1)
	go func() {
		status, body := s.post(genesisID, common.SignalMsgOffer, "{}", http.Header{
			common.VersionHeader: {common.Version},
			testCountryHeader:    {"CN"},
		})
		offerDone <- reply{status, body}
	}()
	var offer common.SignalMsg
	select {
	case raw := <-producer:
		if err := json.Unmarshal([]byte(raw), &offer); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the offer never reached the producer")
	}

	// A browser volunteer whose one socket two STUN servers mapped to different ports.
	answer := sdpJSON(t,
		"a=candidate:2 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 0.0.0.0 rport 0 generation 0",
		"a=candidate:3 1 udp 1686052607 203.0.113.7 40002 typ srflx raddr 0.0.0.0 rport 0 generation 0",
	)
	answerDone := make(chan int, 1)
	go func() {
		status, _ := s.post(offer.ReplyTo, common.SignalMsgAnswer, answer, nil)
		answerDone <- status
	}()
	r := <-offerDone
	if r.status != http.StatusOK {
		t.Fatalf("offer status %d", r.status)
	}
	var answerMsg common.SignalMsg
	if err := json.Unmarshal(r.body, &answerMsg); err != nil {
		t.Fatalf("decoding the answer: %v (%q)", err, r.body)
	}

	// A pion consumer whose NAT changed its port.
	ice, err := json.Marshal(common.ICEMsg{Candidates: []webrtc.ICECandidate{{
		Typ: webrtc.ICECandidateTypeSrflx, Protocol: webrtc.ICEProtocolUDP,
		Address: "198.51.100.4", Port: 61000, RelatedAddress: "10.0.0.2", RelatedPort: 50000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := s.post(answerMsg.ReplyTo, common.SignalMsgICE, string(ice), nil); status != http.StatusOK {
		t.Fatalf("ICE status %d", status)
	}
	if status := <-answerDone; status != http.StatusOK {
		t.Fatalf("answer status %d", status)
	}

	if c, v := s.natLabels(offer.ReplyTo); c != "port_changed" || v != "hidden_symmetric" {
		t.Fatalf("got consumer_nat %q, volunteer_nat %q, want port_changed, hidden_symmetric", c, v)
	}
}

func TestDeliveredOffer_UnansweredNATsAreNone(t *testing.T) {
	s := startTestServer(t, headerHook)
	offerID := s.deliverOffer("cn", common.Version)
	if c, v := s.natLabels(offerID); c != "none" || v != "none" {
		t.Fatalf("got %q, %q, want none for both peers", c, v)
	}
	if c, v := s.natLabels(offerID); c != "unknown" || v != "unknown" {
		t.Fatalf("a second report of the same offer must be unknown, got %q, %q", c, v)
	}
	if c, v := s.natLabels(""); c != "unreported" || v != "unreported" {
		t.Fatalf("a producer that sends no offer ID must be unreported, got %q, %q", c, v)
	}
}

// The consumer can send its ICE message the moment it receives the answer, so the answer must be
// linked to its offer by then.
func TestSignaling_LinksAnswerBeforePublishingIt(t *testing.T) {
	s := startTestServer(t, headerHook)
	s.f.offerConsumers.add("offer", newOffer())
	consumer := signalTable.Add("offer")
	defer signalTable.Delete("offer")

	answerDone := make(chan int, 1)
	go func() {
		status, _ := s.post("offer", common.SignalMsgAnswer, sdpJSON(t,
			"a=candidate:2 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 10.0.0.2 rport 40001 generation 0",
		), nil)
		answerDone <- status
	}()
	var answer common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &answer); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the consumer")
	}
	s.f.offerConsumers.Lock()
	offerID := s.f.offerConsumers.answers[answer.ReplyTo]
	s.f.offerConsumers.Unlock()
	if offerID != "offer" {
		t.Fatalf("the answer was published before it was linked to its offer")
	}

	// Release the answer request, which waits for the consumer's ICE message.
	signalTable.SendOnce(answer.ReplyTo, "{}")
	if status := <-answerDone; status != http.StatusOK {
		t.Fatalf("answer status %d", status)
	}
}
