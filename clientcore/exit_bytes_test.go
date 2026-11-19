//go:build !wasm

package clientcore

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// A direct exit session's bytes reach the UI as relayed bytes do: those the consumer sends as
// upstream, those the exit sends back as downstream, on the slot's worker, and all of them before
// the slot's disconnect.
func TestDirectExitReportsBytesToUI(t *testing.T) {
	const wid = 3
	const sent, returned = 3 * 1000, 2 * 700
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, WebRTCTuning{})

	o := NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	o.Exit.Serve = func(_ context.Context, conn net.PacketConn, _ string, _ bool) {
		buf := make([]byte, 1500)
		for got := 0; got < sent; {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				t.Errorf("exit read: %v", err)
				return
			}
			got += n
		}
		for i := 0; i < 2; i++ {
			if _, err := conn.WriteTo(bytes.Repeat([]byte{1}, returned/2), nil); err != nil {
				t.Errorf("exit write: %v", err)
				return
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	ui := UIImpl{
		OnUpstreamChunkFunc:   func(size, w int) { record(fmt.Sprintf("up %d %d", w, size)) },
		OnDownstreamChunkFunc: func(size, w int) { record(fmt.Sprintf("down %d %d", w, size)) },
		OnConnectionChangeFunc: func(state, w int, _ net.IP) {
			record(fmt.Sprintf("conn %d %d", w, state))
		},
	}
	onTx, _ := uiHandlers(ctx, ui, "", "")
	defer connectedConsumers.set(wid, common.ConsumerInfo{})

	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for {
			select {
			case msg := <-fsm.com.tx:
				msg.Wid = wid
				onTx(msg)
				if ci, ok := msg.Data.(common.ConsumerInfo); ok && ci.Nil() {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		c := consumer.packetConn()
		for i := 0; i < sent/1000; i++ {
			if _, err := c.WriteTo(bytes.Repeat([]byte{2}, 1000), nil); err != nil {
				return
			}
		}
	}()

	// state 7 runs until Serve returns, which waits on every byte sent, so bound it: a lost
	// datagram fails the test here, and the deferred cancel then ends the session.
	ended := make(chan int, 1)
	go func() {
		next, _ := fsm.state[7](ctx, fsm.com, []interface{}{producer, common.OfferMsg{}, "session", net.IP{1, 2, 3, 4}, "pw", true})
		ended <- next
	}()
	select {
	case next := <-ended:
		assert.Equal(t, 0, next)
	case <-time.After(10 * time.Second):
		t.Fatal("the direct exit session never ended")
	}
	select {
	case <-forwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("the slot's disconnect never reached the UI")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events)
	assert.Equal(t, fmt.Sprintf("conn %d 1", wid), events[0], "the session's connect comes first")
	assert.Equal(t, fmt.Sprintf("conn %d -1", wid), events[len(events)-1], "the slot's disconnect comes last")
	up, down := 0, 0
	for _, e := range events[1 : len(events)-1] {
		var dir string
		var w, n int
		_, err := fmt.Sscanf(e, "%s %d %d", &dir, &w, &n)
		require.NoError(t, err, e)
		assert.Equal(t, wid, w, e)
		switch dir {
		case "up":
			up += n
		case "down":
			down += n
		default:
			t.Errorf("unexpected event %q between connect and disconnect", e)
		}
	}
	assert.Equal(t, sent, up, "bytes the consumer sent are upstream")
	assert.Equal(t, returned, down, "bytes the exit sent back are downstream")
}

// Upstream routers drop exit byte reports, which are for the UI alone, rather than forwarding
// them to an egress consumer as data.
func TestUpstreamRoutersDropExitBytes(t *testing.T) {
	newTable := func() *WorkerTable { return NewWorkerTable([]WorkerFSM{*NewWorkerFSM(nil, nil)}) }
	msg := IPCMsg{IpcType: ExitBytesIPC, Data: exitBytes{up: 1, down: 1}, Wid: 0}
	for name, mk := range map[string]func(*ipcChan, *WorkerTable) (*baseRouter, func(*baseRouter, IPCMsg)){
		"serial": func(b *ipcChan, tb *WorkerTable) (*baseRouter, func(*baseRouter, IPCMsg)) {
			r := NewProducerSerialRouter(b, tb, 1)
			r.onPathAssertion(common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}, 0)
			return &r.baseRouter, r.busHook
		},
		"pool": func(b *ipcChan, tb *WorkerTable) (*baseRouter, func(*baseRouter, IPCMsg)) {
			r := NewProducerPoolRouter(b, tb)
			return &r.baseRouter, r.busHook
		},
		"jit": func(b *ipcChan, tb *WorkerTable) (*baseRouter, func(*baseRouter, IPCMsg)) {
			r := NewProducerJITRouter(b, tb)
			return &r.baseRouter, r.busHook
		},
	} {
		t.Run(name, func(t *testing.T) {
			bus, table := newIpcChan(4), newTable()
			r, hook := mk(bus, table)
			r.ctx = context.Background()
			hook(r, msg)
			assert.Empty(t, table.slot[0].com.rx, "forwarded to a worker")
			assert.Empty(t, bus.rx, "sent back down the bus")
		})
	}
}

// A direct exit session's downstream bytes count toward downstream throughput.
func TestExitBytesCountTowardDownstreamThroughput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var size, wid int
	ui := UIImpl{OnDownstreamChunkFunc: func(s, w int) { size, wid = s, w }}
	m := newDownstreamMeter(ctx, ui)
	upstreamUIHandler(ctx, ui, "", "", m)(IPCMsg{IpcType: ExitBytesIPC, Data: exitBytes{down: 500}, Wid: 1})
	assert.Equal(t, 500, size)
	assert.Equal(t, 1, wid)
	assert.Equal(t, int64(500), m.bytesPerSec.Load())
}

// A count past what an int holds on 32-bit platforms reaches the UI whole, in int-sized parts.
func TestInIntsSplitsLargeCounts(t *testing.T) {
	var total int64
	var parts int
	n := int64(math.MaxInt32)*3 + 7
	inInts(n, func(p int) {
		assert.Positive(t, p)
		total += int64(p)
		parts++
	})
	assert.Equal(t, n, total)
	assert.GreaterOrEqual(t, parts, 1)
	inInts(0, func(int) { t.Error("called for zero") })
}
