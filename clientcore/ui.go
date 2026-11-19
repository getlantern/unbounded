// ui.go defines a standard interface for UI status bindings across build platforms
package clientcore

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/broflake/common"
)

const (
	uiRefreshHz = 4
)

// XXX: This structure is used to maintain cumulative state for the identity of currently connected
// consumers, and it exists only for the purpose of reporting network graph data to netstated
type safeConsumerMap struct {
	mu sync.RWMutex
	v  map[workerID]common.ConsumerInfo
}

func (c *safeConsumerMap) set(wid workerID, ci common.ConsumerInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.v[wid] = ci
}

func (c *safeConsumerMap) get(wid workerID) (ci common.ConsumerInfo, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ci, ok = c.v[wid]
	return ci, ok
}

// Return only the currently connected consumers as a slice of 3-tuples: [IP addr, tag, workerIdx]
func (c *safeConsumerMap) slice() [][]string {
	var s [][]string
	c.mu.RLock()
	defer c.mu.RUnlock()

	for wid, cinfo := range c.v {
		if !cinfo.Nil() {
			s = append(s, []string{cinfo.Addr.String(), cinfo.Tag, strconv.Itoa(int(wid))})
		}
	}

	return s
}

var connectedConsumers = safeConsumerMap{v: make(map[workerID]common.ConsumerInfo)}

type UI interface {
	Init(bf *BroflakeEngine)

	Start()

	Stop()

	Debug()

	OnReady()

	OnStartup()

	OnDownstreamChunk(size int, workerIdx int)

	OnDownstreamThroughput(bytesPerSec int)

	OnUpstreamChunk(size int, workerIdx int)

	OnConsumerConnectionChange(state int, workerIdx int, addr net.IP)
}

// onConsumerConnectionChangeInfo calls OnConnectionChangeInfoFunc, if set. It is
// not on the UI interface, and not fired to JavaScript on wasm: it carries the
// consumer's pairing, which stays in Go.
func (ui UIImpl) onConsumerConnectionChangeInfo(state int, workerIdx int, info common.ConsumerInfo) {
	if ui.OnConnectionChangeInfoFunc != nil {
		ui.OnConnectionChangeInfoFunc(state, workerIdx, info)
	}
}

// uiHandlers are the bus's upstream and downstream UI handlers, sharing one downstream
// throughput meter so that a direct exit session's downstream bytes, which reach the upstream
// handler, count toward it.
func uiHandlers(ctx context.Context, ui UIImpl, netstated, tag string) (onTx, onRx func(IPCMsg)) {
	down := newDownstreamMeter(ctx, ui)
	return upstreamUIHandler(ctx, ui, netstated, tag, down), down.handle
}

// downstreamMeter reports downstream chunks, and their throughput at uiRefreshHz.
type downstreamMeter struct {
	ui          UIImpl
	bytesPerSec atomic.Int64
}

func newDownstreamMeter(ctx context.Context, ui UIImpl) *downstreamMeter {
	m := &downstreamMeter{ui: ui}
	var tick uint
	tickInterval := time.Second / uiRefreshHz

	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ui.OnDownstreamThroughput(int(m.bytesPerSec.Load()))
				tick++
				if tick == uiRefreshHz {
					m.bytesPerSec.Store(0)
					tick = 0
				}
			}
		}
	}()

	return m
}

func (m *downstreamMeter) chunk(size int, wid workerID) {
	m.bytesPerSec.Add(int64(size))
	m.ui.OnDownstreamChunk(size, int(wid))
}

func (m *downstreamMeter) handle(msg IPCMsg) {
	switch msg.IpcType {
	case ChunkIPC:
		m.chunk(len(msg.Data.([]byte)), msg.Wid)
	}
}

func DownstreamUIHandler(ctx context.Context, ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	return newDownstreamMeter(ctx, ui).handle
}

// UpstreamUIHandler handles the bus's upstream traffic. A direct exit session's downstream bytes
// reach it too, and go to OnDownstreamChunk without counting toward a DownstreamUIHandler's
// throughput: uiHandlers pairs the two so that they do.
func UpstreamUIHandler(ctx context.Context, ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	return upstreamUIHandler(ctx, ui, netstated, tag, nil)
}

func upstreamUIHandler(ctx context.Context, ui UIImpl, netstated, tag string, down *downstreamMeter) func(msg IPCMsg) {
	// Upstream bytes are batched per worker and flushed at uiRefreshHz rather than firing a UI
	// event per chunk, since every event crosses the JS boundary on wasm
	var mu sync.Mutex
	pending := make(map[workerID]int)
	// deliverMu orders batch delivery against connection changes, so bytes relayed for a
	// worker slot are never delivered after that slot's next consumer has connected
	var deliverMu sync.Mutex
	// This handler runs on the forwarding path, so netstated is reported to from elsewhere.
	var reporter *netstateReporter
	if netstated != "" {
		reporter = newNetstateReporter(ctx, netstated, tag)
	}

	flush := func() {
		deliverMu.Lock()
		defer deliverMu.Unlock()
		mu.Lock()
		batch := pending
		pending = make(map[workerID]int, len(batch))
		mu.Unlock()
		for wid, size := range batch {
			ui.OnUpstreamChunk(size, int(wid))
		}
	}

	go func() {
		ticker := time.NewTicker(time.Second / uiRefreshHz)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// deliver bytes already relayed since the last tick so they aren't lost on stop
				flush()
				return
			case <-ticker.C:
				flush()
			}
		}
	}()

	addPending := func(wid workerID, size int) {
		mu.Lock()
		pending[wid] += size
		mu.Unlock()
		// the bus can deliver a chunk it picked up before cancellation after the batching
		// goroutine's final flush, which would otherwise strand it in pending
		if ctx.Err() != nil {
			flush()
		}
	}

	return func(msg IPCMsg) {
		switch msg.IpcType {
		case ChunkIPC:
			addPending(msg.Wid, len(msg.Data.([]byte)))
		case ExitBytesIPC:
			// A direct exit session's bytes, which bypass the bus. They arrive in order with the
			// slot's ConsumerInfoIPC, so they are delivered before its next connection change.
			b := msg.Data.(exitBytes)
			if b.down > 0 {
				if down != nil {
					down.chunk(b.down, msg.Wid)
				} else {
					ui.OnDownstreamChunk(b.down, int(msg.Wid))
				}
			}
			if b.up > 0 {
				addPending(msg.Wid, b.up)
			}
		case ConsumerInfoIPC:
			deliverMu.Lock()
			mu.Lock()
			size := pending[msg.Wid]
			delete(pending, msg.Wid)
			mu.Unlock()
			if size > 0 {
				ui.OnUpstreamChunk(size, int(msg.Wid))
			}

			ci := msg.Data.(common.ConsumerInfo)

			// Fire a UI event for the consumer delta
			state := 1
			if ci.Nil() {
				state = -1
			}

			ui.OnConsumerConnectionChange(state, int(msg.Wid), ci.Addr)
			ui.onConsumerConnectionChangeInfo(state, int(msg.Wid), ci)
			deliverMu.Unlock()

			// Update our cumulative local state for all connected consumers
			connectedConsumers.set(msg.Wid, ci)

			if reporter != nil {
				reporter.report(connectedConsumers.slice())
			}
		}
	}
}
