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
			statBytesToPeers.Add(uint64(size))
			ui.OnDownstreamChunk(size, int(msg.Wid))
		}
	}
}

func UpstreamUIHandler(ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	return func(msg IPCMsg) {
		switch msg.IpcType {
		case ChunkIPC:
			// Upload direction (peer -> widget -> egress). Accounted here for the
			// aggregate Stats() snapshot; DownstreamUIHandler accounts the download
			// direction. See stats.go for the direction convention.
			statBytesFromPeers.Add(uint64(len(msg.Data.([]byte))))
		case ConsumerInfoIPC:
			ci := msg.Data.(common.ConsumerInfo)

			// Fire a UI event for the consumer delta
			state := 1
			if ci.Nil() {
				state = -1
			}

			if state == 1 {
				recordPeerConnect(ci.Addr)
			} else {
				recordPeerDisconnect()
			}

			ui.OnConsumerConnectionChange(state, int(msg.Wid), ci.Addr)

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
