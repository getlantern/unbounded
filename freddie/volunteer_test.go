package freddie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// volunteerSocket is a test volunteer's socket to s.
type volunteerSocket struct {
	t *testing.T
	c *websocket.Conn
}

func (s *testServer) dialVolunteer(version string) (*volunteerSocket, *http.Response, error) {
	h := http.Header{}
	h.Set(common.VersionHeader, version)
	c, res, err := websocket.Dial(s.ctx, "ws"+strings.TrimPrefix(s.url, "http")+common.VolunteerSocketEndpoint,
		&websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, res, err
	}
	s.t.Cleanup(func() { c.CloseNow() })
	return &volunteerSocket{t: s.t, c: c}, res, nil
}

func (v *volunteerSocket) send(id uint64, form url.Values) {
	v.t.Helper()
	b, err := json.Marshal(common.VolunteerRequest{ID: id, Form: form})
	require.NoError(v.t, err)
	require.NoError(v.t, v.c.Write(context.Background(), websocket.MessageText, b))
}

func (v *volunteerSocket) reply() common.VolunteerReply {
	v.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, b, err := v.c.Read(ctx)
	require.NoError(v.t, err)
	var r common.VolunteerReply
	require.NoError(v.t, json.Unmarshal(b, &r))
	return r
}

func signalForm(sendTo string, typ common.SignalMsgType, data string) url.Values {
	return url.Values{"send-to": {sendTo}, "type": {strconv.Itoa(int(typ))}, "data": {data}}
}

// A whole handshake with the volunteer on its socket and the consumer on HTTP, which sees no
// difference.
func TestVolunteerSocket_Handshake(t *testing.T) {
	s := startTestServer(t, nil)
	consumer := s.subscribe("", "")
	s.waitForConsumers(1)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)

	vol.send(1, signalForm(common.SignalSendToGenesis, common.SignalMsgGenesis, "{}"))
	var genesis common.SignalMsg
	select {
	case raw := <-consumer:
		require.NoError(t, json.Unmarshal([]byte(raw), &genesis))
	case <-time.After(5 * time.Second):
		t.Fatal("the genesis never reached the consumer")
	}

	// The consumer's offer comes back as the genesis's reply.
	offerDone := make(chan []byte, 1)
	go func() {
		_, body := s.post(genesis.ReplyTo, common.SignalMsgOffer, "offer-sdp", nil)
		offerDone <- body
	}()
	r := vol.reply()
	require.Equal(t, uint64(1), r.ID)
	require.Equal(t, http.StatusOK, r.Status)
	var offer common.SignalMsg
	require.NoError(t, json.Unmarshal([]byte(r.Body), &offer))
	assert.Equal(t, common.SignalMsgOffer, offer.Type)
	assert.Equal(t, "offer-sdp", offer.Payload)

	// The volunteer's answer reaches the consumer, and the consumer's ICE comes back as its reply.
	vol.send(2, signalForm(offer.ReplyTo, common.SignalMsgAnswer, "answer-sdp"))
	var answer common.SignalMsg
	select {
	case body := <-offerDone:
		require.NoError(t, json.Unmarshal(body, &answer))
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the consumer")
	}
	assert.Equal(t, "answer-sdp", answer.Payload)
	status, _ := s.post(answer.ReplyTo, common.SignalMsgICE, "ice", nil)
	require.Equal(t, http.StatusOK, status)
	r = vol.reply()
	require.Equal(t, uint64(2), r.ID)
	var ice common.SignalMsg
	require.NoError(t, json.Unmarshal([]byte(r.Body), &ice))
	assert.Equal(t, "ice", ice.Payload)
}

// An answer to an offer nobody is waiting on gets the 404 its POST would.
func TestVolunteerSocket_LateAnswer(t *testing.T) {
	s := startTestServer(t, nil)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(7, signalForm("gone", common.SignalMsgAnswer, "answer-sdp"))
	r := vol.reply()
	assert.Equal(t, uint64(7), r.ID)
	assert.Equal(t, http.StatusNotFound, r.Status)
}

// The socket carries only what volunteers send: a consumer's message types are refused.
func TestVolunteerSocket_RefusesConsumerMessages(t *testing.T) {
	s := startTestServer(t, nil)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(3, signalForm("someone", common.SignalMsgOffer, "x"))
	r := vol.reply()
	assert.Equal(t, uint64(3), r.ID)
	assert.Equal(t, http.StatusBadRequest, r.Status)
}

// A volunteer on an incompatible protocol version is refused before the upgrade, as its POSTs are.
func TestVolunteerSocket_RefusesBadVersion(t *testing.T) {
	s := startTestServer(t, nil)
	_, res, err := s.dialVolunteer("v1.0.0")
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, http.StatusTeapot, res.StatusCode)
}

// Requests overlap: a genesis waiting for an offer doesn't hold up the next request's reply.
func TestVolunteerSocket_RequestsOverlap(t *testing.T) {
	s := startTestServer(t, nil)
	vol, _, err := s.dialVolunteer(common.Version)
	require.NoError(t, err)
	vol.send(1, signalForm(common.SignalSendToGenesis, common.SignalMsgGenesis, "{}"))
	vol.send(2, signalForm("gone", common.SignalMsgAnswer, "answer-sdp"))
	start := time.Now()
	r := vol.reply()
	assert.Equal(t, uint64(2), r.ID, "the late answer is refused while the genesis still waits")
	assert.Less(t, time.Since(start), time.Duration(defaultMsgTTL)*time.Second)
	r = vol.reply()
	assert.Equal(t, uint64(1), r.ID)
	assert.Equal(t, http.StatusOK, r.Status)
	assert.Empty(t, r.Body, "no consumer took the genesis")
}
