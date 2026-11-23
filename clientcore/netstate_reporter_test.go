//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
	netstatecl "github.com/getlantern/broflake/netstate/client"
)

// A netstated that accepts the report and never answers must not hold up the forwarding path:
// the handler runs inline on it.
func TestUpstreamUIHandlerDoesNotWaitForNetstated(t *testing.T) {
	release := make(chan struct{})
	reached := make(chan struct{}, 1)
	netstated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
	}))
	t.Cleanup(netstated.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handle := UpstreamUIHandler(ctx, UIImpl{}, netstated.URL, "test")
	defer connectedConsumers.set(5, common.ConsumerInfo{})

	done := make(chan struct{})
	go func() {
		handle(IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{Addr: net.IPv4(192, 0, 2, 1)}, Wid: 5})
		handle(IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{}, Wid: 5})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler waited on netstated")
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the report never reached netstated")
	}
}

// Reports made while one is in flight collapse into the latest, which goes next.
func TestNetstateReporterSendsTheLatest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var sent [][]string
	first := make(chan struct{})
	unblock := make(chan struct{})
	r := &netstateReporter{netstated: "http://netstated.invalid", tag: "t", wake: make(chan struct{}, 1),
		exec: func(_ string, inst *netstatecl.Instruction) error {
			mu.Lock()
			sent = append(sent, inst.Args)
			n := len(sent)
			mu.Unlock()
			if n == 1 {
				close(first)
				<-unblock
			}
			return nil
		}}
	go r.run(ctx)

	r.report([][]string{{"192.0.2.1", "t", "0"}})
	<-first
	r.report([][]string{{"192.0.2.2", "t", "0"}})
	r.report([][]string{{"192.0.2.3", "t", "0"}})
	close(unblock)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("want the first report and then only the latest, got %v", sent)
	}
	if got := sent[1]; len(got) != 1 || got[0] != "192.0.2.3,t,0" {
		t.Fatalf("second report = %v, want the latest set", got)
	}
}

// A report pending when the context ends, while another is in flight, is never sent.
func TestNetstateReporterDropsPendingOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 4)
	inFlight := make(chan struct{})
	unblock := make(chan struct{})
	r := &netstateReporter{netstated: "http://netstated.invalid", tag: "t", wake: make(chan struct{}, 1),
		exec: func(string, *netstatecl.Instruction) error {
			calls <- struct{}{}
			if len(calls) == 1 {
				close(inFlight)
				<-unblock
			}
			return nil
		}}
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()

	r.report([][]string{{"192.0.2.1", "t", "0"}})
	<-inFlight
	r.report([][]string{{"192.0.2.2", "t", "0"}})
	cancel()
	close(unblock)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the reporter didn't stop")
	}
	if n := len(calls); n != 1 {
		t.Fatalf("sent %d reports, want only the one in flight", n)
	}
}

// The reporter stops with its context and sends nothing after.
func TestNetstateReporterStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 4)
	r := newNetstateReporter(ctx, "http://netstated.invalid", "t")
	r.exec = func(string, *netstatecl.Instruction) error { calls <- struct{}{}; return nil }
	cancel()
	time.Sleep(20 * time.Millisecond)
	r.report([][]string{{"192.0.2.1", "t", "0"}})
	select {
	case <-calls:
		t.Fatal("reported after its context ended")
	case <-time.After(100 * time.Millisecond):
	}
}
