package freddie

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/getlantern/broflake/common"
)

func (s *testServer) postSignal(form url.Values) int {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// Consumers take anything on the genesis stream as a producer to answer, so only a genesis may be
// broadcast there.
func TestHandleSignal_GenesisRecipientRequiresGenesisType(t *testing.T) {
	s := startTestServer(t, nil)
	pool := s.subscribe("", "")
	s.waitForConsumers(1)

	for _, typ := range []common.SignalMsgType{common.SignalMsgOffer, common.SignalMsgAnswer, common.SignalMsgICE, 99} {
		status := s.postSignal(url.Values{
			"data":    {"{}"},
			"send-to": {common.SignalSendToGenesis},
			"type":    {strconv.Itoa(int(typ))},
		})
		if status != http.StatusBadRequest {
			t.Errorf("type %v to genesis: status %d, want 400", typ, status)
		}
	}
	s.expect("pool", pool, false)
}

func TestHandleSignal_RejectsUnknownType(t *testing.T) {
	s := startTestServer(t, nil)
	status := s.postSignal(url.Values{
		"data":    {"{}"},
		"send-to": {"some-request-id"},
		"type":    {"7"},
	})
	if status != http.StatusBadRequest {
		t.Errorf("unknown type: status %d, want 400", status)
	}
}

func TestHandleSignal_RejectsOversizedBody(t *testing.T) {
	s := startTestServer(t, nil)
	status := s.postSignal(url.Values{
		"data":    {strings.Repeat("x", maxSignalBodyBytes)},
		"send-to": {"some-request-id"},
		"type":    {strconv.Itoa(int(common.SignalMsgICE))},
	})
	if status != http.StatusBadRequest {
		t.Errorf("oversized body: status %d, want 400", status)
	}
}

// ParseForm merges the query into the form, so an oversized query is refused like a body.
func TestHandleSignal_RejectsOversizedQuery(t *testing.T) {
	s := startTestServer(t, nil)
	q := url.Values{
		"data":    {strings.Repeat("x", maxSignalBodyBytes)},
		"send-to": {common.SignalSendToGenesis},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(common.VersionHeader, common.Version)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Errorf("oversized query: status %d, want it refused", res.StatusCode)
	}
}
