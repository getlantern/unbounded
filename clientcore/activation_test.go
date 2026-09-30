package clientcore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

func TestActivation_BeginEnd(t *testing.T) {
	var a activation
	if got := a.id(); got != "" {
		t.Fatalf("id before begin = %q, want empty", got)
	}

	a.begin()
	first := a.id()
	if first == "" {
		t.Fatal("id after begin is empty")
	}
	if a.id() != first {
		t.Fatal("id changed without a new begin")
	}

	// A repeated start without a stop is still the same activation.
	a.begin()
	if got := a.id(); got != first {
		t.Fatalf("a repeated begin replaced the ID: got %q, want %q", got, first)
	}

	a.end()
	if got := a.id(); got != "" {
		t.Fatalf("id after end = %q, want empty", got)
	}

	// Turning proxy mode off and on again is a new activation.
	a.begin()
	if a.id() == first {
		t.Fatal("a second begin reused the first ID")
	}
}

func TestActivation_NilIsInert(t *testing.T) {
	var a *activation
	a.begin()
	a.end()
	if got := a.id(); got != "" {
		t.Fatalf("nil id = %q, want empty", got)
	}
}

// The engine's start and stop are the proxy-mode toggle, so they are
// what must open and close an activation.
func TestBroflakeEngine_StartStopTogglesActivation(t *testing.T) {
	var wg sync.WaitGroup
	ui := &UIImpl{}
	engine := NewBroflakeEngine(NewWorkerTable(nil), NewWorkerTable(nil), ui, &wg, "", "test")
	engine.activation = &activation{}
	ui.Init(engine)

	engine.start()
	if engine.activation.id() == "" {
		t.Fatal("no activation ID after start")
	}
	engine.stop()
	if got := engine.activation.id(); got != "" {
		t.Fatalf("activation ID %q survived stop", got)
	}
}

// End to end through a real egress slot: the ID the slot puts on the
// wire is the current activation's, and none outside an activation.
func TestJITEgressConsumer_SendsActivationID(t *testing.T) {
	headers := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get(common.SubprotocolsHeader)
		// Refuse, so the slot backs off and waits for the next consumer.
		http.Error(w, "test", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	act := &activation{}
	fsm := NewJITEgressConsumer(&EgressOptions{
		Addr:           "ws" + strings.TrimPrefix(srv.URL, "http"),
		Endpoint:       "/ws",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   10 * time.Millisecond,
	}, act, nil)
	fsm.Start()
	t.Cleanup(fsm.Stop)

	dial := func() []string {
		t.Helper()
		fsm.com.rx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{SessionID: "csid-1", Country: "IR"}}
		select {
		case h := <-headers:
			var out []string
			for _, v := range strings.Split(h, ",") {
				out = append(out, strings.TrimSpace(v))
			}
			return out
		case <-time.After(5 * time.Second):
			t.Fatal("slot never dialed the egress")
			return nil
		}
	}
	parse := func(s []string) (country, id string) {
		t.Helper()
		_, _, country, id, ok := common.ParseSubprotocolsRequestWithActivation(s)
		if !ok {
			t.Fatalf("egress would refuse %v", s)
		}
		return country, id
	}

	if _, id := parse(dial()); id != "" {
		t.Errorf("sent ID %q outside an activation, want none", id)
	}

	act.begin()
	country, id := parse(dial())
	if id != act.id() {
		t.Errorf("sent ID %q, want the current activation's %q", id, act.id())
	}
	if country != "IR" {
		t.Errorf("sent country %q alongside the ID, want IR", country)
	}

	act.end()
	if _, id := parse(dial()); id != "" {
		t.Errorf("sent ID %q after the activation ended, want none", id)
	}
}
