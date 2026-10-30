// broflake.go defines mid-layer abstractions for constructing and describing a Broflake instance
package clientcore

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"time"

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
	ctx               context.Context
	cancel            context.CancelFunc
	// stopGrace bounds how long stop() waits for workers before cancelling the
	// engine ctx anyway. Per-engine rather than a package var so tests can shorten
	// it without racing a concurrent stop().
	stopGrace time.Duration
	// activation is shared with the egress slots, which read its ID on
	// every dial. nil for engines built without them.
	activation *activation
	// rtcOpt is shared with the WebRTC workers; kept so the UI can retarget a live instance.
	rtcOpt *WebRTCOptions
	// consumerSlots are a consumer's WebRTC slots. nil for a widget.
	consumerSlots *consumerSlots
}

// ConsumerSlot is one of a consumer's WebRTC connection slots.
type ConsumerSlot struct {
	WorkerIdx int
	Connected bool
	// Pairing is the helper pairing of the room genesis a connected slot
	// answered, or "" for a pool producer, or when Freddie predates pairing
	// attribution. Never log it.
	Pairing string
}

// consumerSlots tracks a consumer's WebRTC slots for ConsumerSlots and
// ConsumerSlotChangeFunc.
type consumerSlots struct {
	mu       sync.Mutex
	slots    []ConsumerSlot
	onChange ConsumerSlotChangeFunc
}

func newConsumerSlots(n int, onChange ConsumerSlotChangeFunc) *consumerSlots {
	s := &consumerSlots{slots: make([]ConsumerSlot, n), onChange: onChange}
	for i := range s.slots {
		s.slots[i].WorkerIdx = i
	}
	return s
}

// reporter returns the function slot idx's worker reports through.
func (s *consumerSlots) reporter(idx int) func(connected bool, pairing string) {
	return func(connected bool, pairing string) {
		if !connected {
			pairing = ""
		}
		s.mu.Lock()
		s.slots[idx] = ConsumerSlot{WorkerIdx: idx, Connected: connected, Pairing: pairing}
		s.mu.Unlock()
		if s.onChange != nil {
			state := -1
			if connected {
				state = 1
			}
			s.onChange(state, idx, pairing)
		}
	}
}

func (s *consumerSlots) snapshot() []ConsumerSlot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.slots)
}

// ConsumerSlots returns a consumer's (ClientType "desktop") WebRTC slots: for
// each, whether it is connected and, if so, the helper pairing it came
// through. nil for a widget.
func (b *BroflakeEngine) ConsumerSlots() []ConsumerSlot {
	if b == nil || b.consumerSlots == nil {
		return nil
	}
	return b.consumerSlots.snapshot()
}

func NewBroflakeEngine(cTable, pTable *WorkerTable, ui UI, wg *sync.WaitGroup, netstated, tag string) *BroflakeEngine {
	ctx, cancel := context.WithCancel(context.Background())
	return &BroflakeEngine{
		cTable:            cTable,
		pTable:            pTable,
		ui:                ui,
		wg:                wg,
		netstated:         netstated,
		tag:               tag,
		netstateHeartbeat: time.Minute,
		netstateStop:      make(chan struct{}),
		stopGrace:         defaultStopGrace,
		ctx:               ctx,
		cancel:            cancel,
	}
}

func (b *BroflakeEngine) start() {
	b.activation.begin()
	b.cTable.Start()
	b.pTable.Start()
	slog.Debug("▶ Broflake started!")

	if b.netstated != "" {
		go func() {
			slog.Debug("Netstate heartbeat ON")

			for {
				slog.Debug("Netstate HEARTBEAT")
				err := netstatecl.Exec(
					b.netstated,
					&netstatecl.Instruction{
						Op:   netstatecl.OpConsumerState,
						Args: netstatecl.EncodeArgsOpConsumerState(connectedConsumers.slice()),
						Tag:  b.tag,
					},
				)

				if err != nil {
					slog.Debug("Netstate client Exec error", "error", err)
				}

				select {
				case <-time.After(b.netstateHeartbeat):
					// Do nothing, iterate the loop
				case <-b.netstateStop:
					defer slog.Debug("Netstate heartbeat OFF")
					return
				}
			}
		}()
	}
}

// defaultStopGrace bounds how long stop() waits for workers to exit before
// cancelling the engine ctx regardless. Generous enough for a worker to finish a
// state. The producer's wait on NATFailTimeout (longer for the widget) returns as
// soon as its worker is stopped, so it needn't fit inside this. The consumer's
// doesn't select on ctx, so a consumer NATFailTimeout above this grace means
// stop() gives up waiting on that worker and cancels the engine ctx anyway.
const defaultStopGrace = 10 * time.Second

func (b *BroflakeEngine) stop() {
	b.activation.end()
	b.cTable.Stop()
	b.pTable.Stop()

	go func() {
		// Prefer cancelling the engine ctx only after workers exit: some worker states
		// block on com.tx sends, and stopping the routers earlier can strand a worker on
		// a full buffer. But that wait must be bounded — a worker parked in an unguarded
		// com.tx send never returns, and without a deadline wg.Wait() would hold the
		// engine ctx, and therefore the bus, both routers and every other worker, for the
		// lifetime of the process. Every re-create then leaks a whole stack.
		waited := make(chan struct{})
		go func() {
			b.wg.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(b.stopGrace):
			slog.Debug("Broflake stop: workers did not exit before deadline, cancelling anyway",
				"grace", b.stopGrace)
		}

		b.cancel()

		if b.netstated != "" {
			b.netstateStop <- struct{}{}
		}
		slog.Debug("■ Broflake stopped.")
		b.ui.OnReady()
	}()
}

func (b *BroflakeEngine) setTargetCountries(countries []string) {
	if b.rtcOpt == nil {
		return
	}
	b.rtcOpt.SetTargetCountries(countries)
	slog.Debug("Target countries set", "countries", b.rtcOpt.targetCountries())
}

// setRoomTickets gives the widget the volunteer tickets of its helper
// pairings. Each worker reads them at its next genesis message. It logs
// nothing: tickets are credentials.
func (b *BroflakeEngine) setRoomTickets(tickets []string) {
	if b.rtcOpt == nil {
		return
	}
	b.rtcOpt.SetRoomTickets(tickets)
}

// setServePool sets whether the widget also serves the general pool ("help
// others too") or only its pairings' rooms.
func (b *BroflakeEngine) setServePool(serve bool) {
	if b.rtcOpt == nil {
		return
	}
	b.rtcOpt.SetServePool(serve)
}

func (b *BroflakeEngine) debug() {
	slog.Debug("NumGoroutine", "goroutines", runtime.NumGoroutine())
}

func NewBroflake(bfOpt *BroflakeOptions, rtcOpt *WebRTCOptions, egOpt *EgressOptions) (bfconn *BroflakeConn, ui *UIImpl, err error) {
	if bfOpt.ClientType != "desktop" && bfOpt.ClientType != "widget" {
		err = fmt.Errorf("invalid clientType '%v\n'", bfOpt.ClientType)
		slog.Debug(err.Error())
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
		egOpt = NewDefaultEgressOptions()
	}

	// The boot DAG:
	// build cTable/pTable -> build the Broflake struct -> run ui.Init -> set up the bus and bind
	// the upstream/downstream handlers -> build cRouter/pRouter -> start the bus, init the routers,
	// call onStartup and onReady. This dependency graph currently requires us to implement two
	// switches on clientType during the boot process, which can probably be improved upon.

	act := &activation{}
	var slots *consumerSlots

	// Step 1: Build consumer table and producer table
	switch bfOpt.ClientType {
	case "desktop":
		// Desktop peers don't share connectivity for the MVP, so the consumer table only gets one
		// workerFSM for the local user stream associated with their HTTP proxy
		var producerUserStream *WorkerFSM
		bfconn, producerUserStream = NewProducerUserStream(&wgReady)
		cTable = NewWorkerTable([]WorkerFSM{*producerUserStream})

		// Desktop peers consume connectivity over WebRTC
		// XXX: we're temporarily forcing all desktop peers to a producer table size of 1, overriding
		// the configuration parameters. In practice, this means that all censored peers will only
		// maintain one concurrent upstream connection -- they will no longer preconnect some additional
		// uncensored peer proxies. This change is required to reduce complexity at the egress server
		// inflicted by cardinality relationships as we implement connection migration. See:
		// https://github.com/getlantern/unbounded/pull/301
		var pfsms []WorkerFSM
		nConsumers := 1 // bfOpt.PTableSize
		slots = newConsumerSlots(nConsumers, bfOpt.OnConsumerSlotChangeFunc)
		for i := 0; i < nConsumers; i++ {
			pfsms = append(pfsms, *newConsumerWebRTCSlot(rtcOpt, &wgReady, newRoomPreference(time.Now), slots.reporter(i)))
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
		for i := 0; i < bfOpt.PTableSize; i++ {
			pfsms = append(pfsms, *NewJITEgressConsumer(egOpt, act, &wgReady))
		}
		pTable = NewWorkerTable(pfsms)
	}

	// Step 2: Build Broflake
	broflake := NewBroflakeEngine(cTable, pTable, ui, &wgReady, bfOpt.Netstated, rtcOpt.Tag)
	broflake.activation = act
	broflake.rtcOpt = rtcOpt
	broflake.consumerSlots = slots

	// Step 3: Init the UI (this constructs and exposes the JavaScript API as required)
	ui.Init(broflake)

	// Set optional connection change callback before UI is copied to bus handlers
	if bfOpt.OnConnectionChangeFunc != nil {
		ui.OnConnectionChangeFunc = bfOpt.OnConnectionChangeFunc
	}
	ui.OnConnectionChangeInfoFunc = bfOpt.OnConnectionChangeInfoFunc
	ui.OnDownstreamChunkFunc = bfOpt.OnDownstreamChunkFunc
	ui.OnUpstreamChunkFunc = bfOpt.OnUpstreamChunkFunc

	// Step 4: Set up the bus, bind upstream and downstream UI handlers
	var bus = NewIpcObserver(
		bfOpt.BusBufferSz,
		UpstreamUIHandler(broflake.ctx, *ui, bfOpt.Netstated, rtcOpt.Tag),
		DownstreamUIHandler(broflake.ctx, *ui, bfOpt.Netstated, rtcOpt.Tag),
	)

	// Step 5: Build consumer router and producer router
	switch bfOpt.ClientType {
	case "desktop":
		cRouter = NewConsumerRouter(bus.Downstream, cTable)
		pRouter = NewProducerSerialRouter(bus.Upstream, pTable, cTable.Size())
	case "widget":
		cRouter = NewConsumerRouter(bus.Downstream, cTable)
		pRouter = NewProducerJITRouter(bus.Upstream, pTable)
	}

	// Step 6: Start the bus, init the routers, fire our UI events to announce that we're ready
	bus.Start(broflake.ctx)
	cRouter.Init(broflake.ctx)
	pRouter.Init(broflake.ctx)
	ui.OnReady()
	ui.OnStartup()
	return bfconn, ui, nil
}
