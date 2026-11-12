package freddie

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

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

// Signaling fields come only from the capped body: a query, which an intermediary may add, can
// neither add to nor reroute what the body says.
func TestHandleSignal_IgnoresQuery(t *testing.T) {
	s := startTestServer(t, nil)
	pool := s.subscribe("", "")
	s.waitForConsumers(1)
	body := url.Values{
		"data":    {"{}"},
		"send-to": {common.SignalSendToGenesis},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	// pool=0 would keep this genesis from the pool consumer, were it read.
	query := url.Values{common.PoolField: {"0"}}
	// Nobody answers a genesis message here, so don't wait out its TTL.
	ctx, cancel := context.WithTimeout(s.ctx, 200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/v1/signal?"+query.Encode(), strings.NewReader(body.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	go func() {
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	s.expect("pool", pool, true)
}
