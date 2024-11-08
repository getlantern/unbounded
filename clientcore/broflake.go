// broflake.go defines mid-layer abstractions for constructing and describing a Broflake instance
package clientcore

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
	netstatecl "github.com/getlantern/broflake/netstate/client"
)

type BroflakeEngine struct {
	cTable            *WorkerTable
	pTable            *WorkerTable
	ui                UI
	wg                *sync.WaitGroup
	netstated         string
	tag               string
	netstateHeartbeat time.Duration
	netstateStop      chan struct{}
}

func NewBroflakeEngine(cTable, pTable *WorkerTable, ui UI, wg *sync.WaitGroup, netstated, tag string) *BroflakeEngine {
	return &BroflakeEngine{
		cTable,
		pTable,
		ui,
		wg,
		netstated,
		tag,
		1 * time.Minute,
		make(chan struct{}, 0),
	}
}

func (b *BroflakeEngine) start() {
	b.cTable.Start()
	b.pTable.Start()
	common.Debug("▶ Broflake started!")

	if b.netstated != "" {
		go func() {
			common.Debug("Netstate hearbeat ON")

			for {
				common.Debug("Netstate HEARTBEAT")
				err := netstatecl.Exec(
					b.netstated,
					&netstatecl.Instruction{
						Op:   netstatecl.OpConsumerState,
						Args: netstatecl.EncodeArgsOpConsumerState(connectedConsumers.slice()),
						Tag:  b.tag,
					},
				)

				if err != nil {
					common.Debugf("Netstate client Exec error: %v", err)
				}

				select {
				case <-time.After(b.netstateHeartbeat):
					// Do nothing, iterate the loop
				case <-b.netstateStop:
					defer common.Debug("Netstate heartbeat OFF")
					return
				}
			}
		}()
	}
}

func (b *BroflakeEngine) stop() {
	b.cTable.Stop()
	b.pTable.Stop()

	go func() {
		b.wg.Wait()

		if b.netstated != "" {
			b.netstateStop <- struct{}{}
		}

		common.Debug("■ Broflake stopped.")
		b.ui.OnReady()
	}()
}

func (b *BroflakeEngine) debug() {
	common.Debugf("NumGoroutine: %v", runtime.NumGoroutine())
}

func NewBroflake(bfOpt *BroflakeOptions, rtcOpt *WebRTCOptions, egOpt *EgressOptions) (bfconn *BroflakeConn, ui *UIImpl, err error) {
	if bfOpt.ClientType != "desktop" && bfOpt.ClientType != "widget" {
		err = fmt.Errorf("Invalid clientType '%v\n'", bfOpt.ClientType)
		common.Debugf(err.Error())
		return bfconn, ui, err
	}

	ui = &UIImpl{}
	var cTable *WorkerTable
	var cRouter TableRouter
	var pTable *WorkerTable
	var pRouter TableRouter
	var wgReady sync.WaitGroup

	if bfOpt == nil {
		bfOpt = NewDefaultBroflakeOptions()
	}

	if rtcOpt == nil {
		rtcOpt = NewDefaultWebRTCOptions()
	}

	if egOpt == nil {
		if bfOpt.WebTransport {
			egOpt = NewDefaultWebTransportEgressOptions()
		} else {
			egOpt = NewDefaultWebSocketEgressOptions()
		}
	}

	// The boot DAG:
	// build cTable/pTable -> build the Broflake struct -> run ui.Init -> set up the bus and bind
	// the upstream/downstream handlers -> build cRouter/pRouter -> start the bus, init the routers,
	// call onStartup and onReady. This dependency graph currently requires us to implement two
	// switches on clientType during the boot process, which can probably be improved upon.

	// Step 1: Build consumer table and producer table
	switch bfOpt.ClientType {
	case "desktop":
		// Desktop peers don't share connectivity for the MVP, so the consumer table only gets one
		// workerFSM for the local user stream associated with their HTTP proxy
		var producerUserStream *WorkerFSM
		bfconn, producerUserStream = NewProducerUserStream(&wgReady)
		cTable = NewWorkerTable([]WorkerFSM{*producerUserStream})

		// Desktop peers consume connectivity over WebRTC
		var pfsms []WorkerFSM
		for i := 0; i < bfOpt.PTableSize; i++ {
			pfsms = append(pfsms, *NewConsumerWebRTC(rtcOpt, &wgReady))
		}
		pTable = NewWorkerTable(pfsms)
	case "widget":
		// Widget peers share connectivity over WebRTC
		var cfsms []WorkerFSM
		for i := 0; i < bfOpt.CTableSize; i++ {
			cfsms = append(cfsms, *NewProducerWebRTC(rtcOpt, &wgReady))
		}
		cTable = NewWorkerTable(cfsms)

		// Widget peers consume connectivity from an egress server over WebSocket
		var pfsms []WorkerFSM
		if bfOpt.WebTransport {
			// Chrome widget peers consume connectivity from an egress server over WebTransport
			for i := 0; i < bfOpt.PTableSize; i++ {
				pfsms = append(pfsms, *NewEgressConsumerWebTransport(egOpt, &wgReady))
			}
		} else {
			// Widget peers consume connectivity from an egress server over WebSocket
			for i := 0; i < bfOpt.PTableSize; i++ {
				pfsms = append(pfsms, *NewEgressConsumerWebSocket(egOpt, &wgReady))
			}
		}
		pTable = NewWorkerTable(pfsms)
	}

	// Step 2: Build Broflake
	broflake := NewBroflakeEngine(cTable, pTable, ui, &wgReady, bfOpt.Netstated, rtcOpt.Tag)

	// Step 3: Init the UI (this constructs and exposes the JavaScript API as required)
	ui.Init(broflake)

	// Step 4: Set up the bus, bind upstream and downstream UI handlers
	var bus = NewIpcObserver(
		bfOpt.BusBufferSz,
		UpstreamUIHandler(*ui, bfOpt.Netstated, rtcOpt.Tag),
		DownstreamUIHandler(*ui, bfOpt.Netstated, rtcOpt.Tag),
	)

	// Step 5: Build consumer router and producer router
	switch bfOpt.ClientType {
	case "desktop":
		cRouter = NewConsumerRouter(bus.Downstream, cTable)
		pRouter = NewProducerSerialRouter(bus.Upstream, pTable, cTable.Size())
	case "widget":
		cRouter = NewConsumerRouter(bus.Downstream, cTable)
		pRouter = NewProducerPoolRouter(bus.Upstream, pTable)
	}

	// Step 6: Start the bus, init the routers, fire our UI events to announce that we're ready
	bus.Start()
	cRouter.Init()
	pRouter.Init()
	ui.OnReady()
	ui.OnStartup()
	return bfconn, ui, nil
}
