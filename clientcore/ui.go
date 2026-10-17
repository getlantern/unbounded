// ui.go defines a standard interface for UI status bindings across build platforms
package clientcore

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/broflake/common"
	netstatecl "github.com/getlantern/broflake/netstate/client"
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

func DownstreamUIHandler(ctx context.Context, ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	var bytesPerSec int64
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
				ui.OnDownstreamThroughput(int(atomic.LoadInt64(&bytesPerSec)))
				tick++
				if tick == uiRefreshHz {
					atomic.SwapInt64(&bytesPerSec, 0)
					tick = 0
				}
			}
		}
	}()

	return func(msg IPCMsg) {
		switch msg.IpcType {
		case ChunkIPC:
			size := len(msg.Data.([]byte))
			atomic.AddInt64(&bytesPerSec, int64(size))
			ui.OnDownstreamChunk(size, int(msg.Wid))
		}
	}
}

func UpstreamUIHandler(ctx context.Context, ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	// Upstream bytes are batched per worker and flushed at uiRefreshHz rather than firing a UI
	// event per chunk, since every event crosses the JS boundary on wasm
	var mu sync.Mutex
	pending := make(map[workerID]int)
	// deliverMu orders batch delivery against connection changes, so bytes relayed for a
	// worker slot are never delivered after that slot's next consumer has connected
	var deliverMu sync.Mutex

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

	return func(msg IPCMsg) {
		switch msg.IpcType {
		case ChunkIPC:
			mu.Lock()
			pending[msg.Wid] += len(msg.Data.([]byte))
			mu.Unlock()
			// the bus can deliver a chunk it picked up before cancellation after the batching
			// goroutine's final flush, which would otherwise strand it in pending
			if ctx.Err() != nil {
				flush()
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
			deliverMu.Unlock()

			// Update our cumulative local state for all connected consumers
			connectedConsumers.set(msg.Wid, ci)

			if netstated != "" {
				// Encode our cumulative local state as a netstate instruction
				args := connectedConsumers.slice()

				inst := &netstatecl.Instruction{
					Op:   netstatecl.OpConsumerState,
					Args: netstatecl.EncodeArgsOpConsumerState(args),
					Tag:  tag,
				}

				// Send it to netstated!
				err := netstatecl.Exec(
					netstated,
					inst,
				)

				if err != nil {
					slog.Debug("Netstate client Exec error", "error", err)
				}
			}
		}
	}
}
