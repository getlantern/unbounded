// ui.go defines a standard interface for UI status bindings across build platforms
package clientcore

import (
	"net"
	"net/http"
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

func DownstreamUIHandler(ui UIImpl, netstated, tag string) func(msg IPCMsg) {
	var bytesPerSec int64
	var tick uint
	tickMs := time.Duration(1000 / uiRefreshHz)

	go func() {
		for {
			<-time.After(tickMs * time.Millisecond)
			ui.OnDownstreamThroughput(int(atomic.LoadInt64(&bytesPerSec)))
			if tick%uiRefreshHz == 0 {
				atomic.SwapInt64(&bytesPerSec, 0)
			}
			tick++
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

func UpstreamUIHandler(ui UIImpl, netstated, tag string, httpClient *http.Client) func(msg IPCMsg) {
	return func(msg IPCMsg) {
		switch msg.IpcType {
		case ConsumerInfoIPC:
			ci := msg.Data.(common.ConsumerInfo)

			// Fire a UI event for the consumer delta
			state := 1
			if ci.Nil() {
				state = -1
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
					httpClient,
					netstated,
					inst,
				)

				if err != nil {
					common.Debugf("Netstate client Exec error: %v", err)
				}
			}
		}
	}
}
