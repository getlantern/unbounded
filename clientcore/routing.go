// routing.go defines upstream and downstream router components
package clientcore

import (
	"sync"

	"github.com/getlantern/broflake/common"
)

// A tableRouter is a multiplexer/demultiplexer which functions as the interface to a WorkerTable,
// abstracting the complexity of connecting a WorkerTable of arbitrary size to the message bus. An
// upstream tableRouter, in managing a WorkerTable consisting of workers which handle egress traffic,
// decides how to best utilize those connections (ie, in serial, in parallel, 1:1, multipath, etc.)
type TableRouter interface {
	Init()

	onBus(msg IPCMsg)

	onWorker(msg IPCMsg, workerIdx workerID)
}

// A baseRouter implements basic router functionality
type baseRouter struct {
	bus        *ipcChan
	table      *WorkerTable
	busHook    func(r *baseRouter, msg IPCMsg)
	workerHook func(r *baseRouter, msg IPCMsg, workerIdx workerID)
}

func (r *baseRouter) Init(
	listen chan IPCMsg,
	onBus func(msg IPCMsg),
	onWorker func(msg IPCMsg,
		workerIdx workerID),
) {
	for i := range r.table.slot {
		go func(i int) {
			for {
				msg := <-r.table.slot[i].com.tx
				onWorker(msg, workerID(i))
			}
		}(i)
	}

	go func() {
		for {
			onBus(<-listen)
		}
	}()
}

func (r *baseRouter) onBus(msg IPCMsg) {
	r.busHook(r, msg)
}

func (r *baseRouter) onWorker(msg IPCMsg, workerIdx workerID) {
	r.workerHook(r, msg, workerIdx)
}

// An upstreamRouter parameterizes a baseRouter to send on rx and receive on tx
type upstreamRouter struct {
	baseRouter
}

func (r *upstreamRouter) Init() {
	r.baseRouter.Init(r.bus.tx, r.onBus, r.onWorker)
}

func (r *upstreamRouter) onBus(msg IPCMsg) {
	r.baseRouter.onBus(msg)
}

func (r *upstreamRouter) onWorker(msg IPCMsg, workerIdx workerID) {
	// TODO: a brittle assumption here: upstreamRouter.workerHook will call backRoute and add the
	// wid to the IPCMsg, and it will also forward all appropriate messages to the bus. This is
	// the opposite of downstreamRouter.onWorker, which adds the wid to the IPCMsg and forwards all
	// msgs to the bus. The funkiness here is part of the larger issue concerning the two different
	// ways in which we use the wid field and the asymmetry in how wids are assigned.
	r.baseRouter.onWorker(msg, workerIdx)
}

func (r *upstreamRouter) toBus(msg IPCMsg) {
	r.bus.rx <- msg
}

func (r *upstreamRouter) toWorker(msg IPCMsg, peerIdx workerID) {
	r.table.slot[peerIdx].com.rx <- msg
}

// A downstreamRouter parameterizes a baseRouter to send on tx and receive on rx
type downstreamRouter struct {
	baseRouter
}

func (r *downstreamRouter) Init() {
	r.baseRouter.Init(r.bus.rx, r.onBus, r.onWorker)
}

func (r *downstreamRouter) onBus(msg IPCMsg) {
	r.baseRouter.onBus(msg)
}

func (r *downstreamRouter) onWorker(msg IPCMsg, workerIdx workerID) {
	// Add the worker's ID to the msg
	msg.Wid = workerIdx
	r.toBus(msg)
	r.baseRouter.onWorker(msg, workerIdx)
}

func (r *downstreamRouter) toBus(msg IPCMsg) {
	r.bus.tx <- msg
}

func (r *downstreamRouter) toWorker(msg IPCMsg) {
	r.table.slot[msg.Wid].com.rx <- msg
}

func (r *downstreamRouter) toAllWorkers(msg IPCMsg) {
	for peerIdx := range r.table.slot {
		msg.Wid = workerID(peerIdx)
		r.toWorker(msg)
	}
}

// A producerSerialRouter is a tableRouter for managing producer tables. It employs a naive
// "serial failover" routing strategy - that is, given ingress traffic from N consumers, and a
// a choice of M connected producers to route egress traffic through, the producerSerialRouter
// will route all N consumers through a single producer until that producer fails, at which point
// it will reroute all N consumers to the next available producer. This routing strategy is
// certainly inefficient, and it also provides no redundancy or fault recovery wrt end-to-end
// connection state. A producerSerialRouter announces a non-nil path assertion when it has > 0
// connected producers. TODO: We have intentionally ignored the added complexity of evaluating path
// assertions as part of the routing strategy, since we hypothesize that path mismatches (ie,
// traffic destined for an endpoint which a producer cannot reach) will be fixed for free by
// redundantly sending all N consumers' traffic through all M producers in a multipath
// configuration. The multipath router to provide this functionality is forthcoming after the MVP.
// Despite its obvious inefficiencies, we hypothesize that the producerSerialRouter should work
// fine for the MVP, since desktop peers do not share their connectivity - ie, any peer that
// implements this producerSerialRouter won't even have multiple consumers to route for!
type producerSerialRouter struct {
	upstreamRouter
	producerPA  map[workerID]common.PathAssertion
	forwardIdx  map[workerID]workerID   // consumer:producer
	invertedIdx map[workerID][]workerID // producer:consumers
	sync.RWMutex
}

func NewProducerSerialRouter(bus *ipcChan, table *WorkerTable, cTableSize int) *producerSerialRouter {
	psr := producerSerialRouter{
		upstreamRouter: upstreamRouter{
			baseRouter: baseRouter{
				bus:   bus,
				table: table,
			},
		},
		producerPA:  make(map[workerID]common.PathAssertion),
		forwardIdx:  make(map[workerID]workerID),
		invertedIdx: make(map[workerID][]workerID),
	}

	psr.upstreamRouter.baseRouter.busHook = psr.busHook
	psr.upstreamRouter.baseRouter.workerHook = psr.workerHook

	for i := 0; i < table.size; i++ {
		psr.producerPA[workerID(i)] = common.PathAssertion{}
		psr.invertedIdx[workerID(i)] = []workerID{}
	}

	for i := 0; i < cTableSize; i++ {
		psr.forwardIdx[workerID(i)] = NoRoute
	}

	return &psr
}

func (r *producerSerialRouter) onPathAssertion(pa common.PathAssertion, workerIdx workerID) {
	r.Lock()
	defer r.Unlock()
	r.producerPA[workerIdx] = pa

	if !pa.Nil() {
		// Case 1: We've got a new connected producer, so let's route any unrouted consumers to them
		for consumer, producer := range r.forwardIdx {
			if producer == NoRoute {
				r.forwardIdx[consumer] = workerIdx
				r.invertedIdx[workerIdx] = append(r.invertedIdx[workerIdx], consumer)
			}
		}
	} else {
		// Case 2: A producer has died, so reroute their consumers to a random new producer if available
		newProducer := NoRoute
		for producer, pa := range r.producerPA {
			if !pa.Nil() {
				newProducer = producer
			}
		}

		for _, consumer := range r.invertedIdx[workerIdx] {
			r.forwardIdx[consumer] = newProducer
			if newProducer != NoRoute {
				r.invertedIdx[newProducer] = append(r.invertedIdx[newProducer], consumer)
			}
		}

		r.invertedIdx[workerIdx] = []workerID{}
	}
}

func (r *producerSerialRouter) route(wid workerID) (bool, workerID) {
	r.RLock()
	defer r.RUnlock()
	route := r.forwardIdx[wid]
	return route != NoRoute, route
}

func (r *producerSerialRouter) backRoute(wid workerID) (bool, workerID) {
	r.RLock()
	defer r.RUnlock()
	consumers := r.invertedIdx[wid]

	if len(consumers) == 0 {
		return false, NoRoute
	}

	// TODO: this line is the hack that enables us to route packets without a real mux/demux protocol
	// during the MVP phase. Why this works: the producerSerialRouter is only used in the MVP-phase
	// desktop client, and during MVP phase it's only used in conjunction with a consumerRouter that
	// manages a table consisting of exactly 1 worker (the producerUserStream). Since a
	// producerSerialRouter maps many consumers to a single producer, it would be impossible to
	// round trip a packet from producer -> consumer without piggybacking some kind of consumer ID
	// information on the packet. But since we know we only have a single consumer right now, we can
	// just route all returning streams to him. That solves demux in the client, but we also rely on
	// two other constraints to make this work: during MVP phase, there is no multi-hop routing (and
	// thus no downstream multiplexing), and the "free" peers on our network implement a
	// producerPoolRouter which guarantees a 1:1 mapping between WebRTC datachannel and WebSocket
	// connection to the egress server
	return true, consumers[0]
}

func (psr *producerSerialRouter) busHook(r *baseRouter, msg IPCMsg) {
	switch msg.IpcType {
	case ChunkIPC:
		ok, route := psr.route(msg.Wid)
		if ok {
			psr.toWorker(msg, route)
		}
	}
}

func (psr *producerSerialRouter) workerHook(r *baseRouter, msg IPCMsg, workerIdx workerID) {
	switch msg.IpcType {
	case PathAssertionIPC:
		psr.onPathAssertion(msg.Data.(common.PathAssertion), workerIdx)
	case ChunkIPC:
		ok, route := psr.backRoute(workerIdx)
		if ok {
			msg.Wid = route
			psr.toBus(msg)
		}
	}
}

// A producerPoolRouter is a tableRouter for managing producer tables which employs the most
// naive routing strategy available: it maintains a persistent pool of pre-connected producers,
// routing each consumer through its own producer in a 1:1 mapping. A producerPoolRouter only
// announces a non-nil path assertion when all of its producer WorkerFSMs have established stable
// connections. For these reasons, a producerPoolRouter is only useful for managing connections to
// producers which are under Lantern's control - eg, WebSocket connections to an egress server.
// Using a producerPoolRouter to manage a producer table with N slots in a client which has M
// consumer slots (where N != M) will result in undefined behavior.
type producerPoolRouter struct {
	upstreamRouter
	producerPA map[workerID]common.PathAssertion
	sync.RWMutex
}

func NewProducerPoolRouter(bus *ipcChan, table *WorkerTable) *producerPoolRouter {
	ppr := producerPoolRouter{
		upstreamRouter: upstreamRouter{
			baseRouter: baseRouter{
				bus:   bus,
				table: table,
			},
		},
		producerPA: make(map[workerID]common.PathAssertion),
	}

	ppr.upstreamRouter.baseRouter.busHook = ppr.busHook
	ppr.upstreamRouter.baseRouter.workerHook = ppr.workerHook

	return &ppr
}

func (r *producerPoolRouter) onPathAssertion(pa common.PathAssertion, workerIdx workerID) {
	r.Lock()
	defer r.Unlock()
	r.producerPA[workerIdx] = pa
}

func (r *producerPoolRouter) globalPathAssertion() common.PathAssertion {
	r.RLock()
	defer r.RUnlock()

	// If all producer path assertions are non-nil, the global path assertion is their set
	// intersection; otherwise, it's nil

	allNonNil := true
	for _, pa := range r.producerPA {
		if pa.Nil() {
			allNonNil = false
			break
		}
	}

	// TODO: actually implement the intersection function in the common module, don't hardcode
	// (*, 1) after the MVP!
	if allNonNil {
		return common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}
	}

	return common.PathAssertion{}
}

func (r *producerPoolRouter) route(wid workerID) (bool, workerID) {
	return true, wid
}

// For producerPoolRouter, the route function is the identity function, so it works backwards
func (r *producerPoolRouter) backRoute(wid workerID) (bool, workerID) {
	return r.route(wid)
}

func (ppr *producerPoolRouter) busHook(r *baseRouter, msg IPCMsg) {
	switch msg.IpcType {
	case ConnectivityCheckIPC:
		ppr.toBus(IPCMsg{IpcType: PathAssertionIPC, Data: ppr.globalPathAssertion(), Wid: msg.Wid})
	case ChunkIPC:
		_, route := ppr.route(msg.Wid)
		ppr.toWorker(msg, route)
	case ConsumerInfoIPC:
		_, route := ppr.route(msg.Wid)
		ppr.toWorker(msg, route)
	}
}

func (ppr *producerPoolRouter) workerHook(r *baseRouter, msg IPCMsg, workerIdx workerID) {
	switch msg.IpcType {
	case PathAssertionIPC:
		ppr.onPathAssertion(msg.Data.(common.PathAssertion), workerIdx)
		pa := ppr.globalPathAssertion()
		// TODO: currently we send a new path assertion IPC every time we receive a new path
		// assertion from a worker. In practice this is harmless, since our workers are egress
		// consumers who are likely to send just a single path assertion per session. But an optimal
		// implementation would cache the last PA and only send a new IPC when there's a delta.
		ppr.toBus(IPCMsg{IpcType: PathAssertionIPC, Data: pa, Wid: BroadcastRoute})
	case ChunkIPC:
		// Backrouting! TODO: the asymmetry in how upstream and downstream routers determine and
		// assign and interpret the wid is a source of much confusion and must be fixed
		_, route := ppr.backRoute(workerIdx)
		msg.Wid = route
		ppr.toBus(msg)
	}
}

// A producerJITRouter is like a producerPoolRouter, except it's designed to manage producer tables
// which connect "just in time," upon request, as opposed to pre-connecting. Like the producerPoolRouter,
// it routes each consumer through its own producer in a 1:1 mapping. The producerJITRouter notably
// handles path assertions differently than the producerPoolRouter: with the producerJITRouter, the
// intersection of path assertions is meaningless (since this router does not support muxing consumers
// over producers, but rather assigns them 1:1) and so path assertions are announced per producer.
// Using a producerJITRouter to manage a producer table with N slots in a client which has M consumer
// slots (where N != M) will result in undefined behavior.
type producerJITRouter struct {
	upstreamRouter
	producerPA map[workerID]common.PathAssertion
	sync.RWMutex
}

func NewProducerJITRouter(bus *ipcChan, table *WorkerTable) *producerJITRouter {
	pjr := producerJITRouter{
		upstreamRouter: upstreamRouter{
			baseRouter: baseRouter{
				bus:   bus,
				table: table,
			},
		},
		producerPA: make(map[workerID]common.PathAssertion),
	}

	pjr.upstreamRouter.baseRouter.busHook = pjr.busHook
	pjr.upstreamRouter.baseRouter.workerHook = pjr.workerHook

	return &pjr
}

func (r *producerJITRouter) onPathAssertion(pa common.PathAssertion, workerIdx workerID) {
	r.Lock()
	defer r.Unlock()
	r.producerPA[workerIdx] = pa
}

func (r *producerJITRouter) route(wid workerID) (bool, workerID) {
	return true, wid
}

// For producerJITRouter, the route function is the identity function, so it works backwards
func (r *producerJITRouter) backRoute(wid workerID) (bool, workerID) {
	return r.route(wid)
}

func (pjr *producerJITRouter) busHook(r *baseRouter, msg IPCMsg) {
	switch msg.IpcType {
	case ConnectivityCheckIPC:
		pjr.RLock()
		pjr.toBus(IPCMsg{IpcType: PathAssertionIPC, Data: pjr.producerPA[msg.Wid], Wid: msg.Wid})
		pjr.RUnlock()
	case ChunkIPC:
		_, route := pjr.route(msg.Wid)
		pjr.toWorker(msg, route)
	case ConsumerInfoIPC:
		_, route := pjr.route(msg.Wid)
		pjr.toWorker(msg, route)
	}
}

func (pjr *producerJITRouter) workerHook(r *baseRouter, msg IPCMsg, workerIdx workerID) {
	switch msg.IpcType {
	case PathAssertionIPC:
		pjr.onPathAssertion(msg.Data.(common.PathAssertion), workerIdx)
		pjr.toBus(IPCMsg{IpcType: PathAssertionIPC, Data: msg.Data.(common.PathAssertion), Wid: workerIdx})
	case ChunkIPC:
		// Backrouting! TODO: the asymmetry in how upstream and downstream routers determine and
		// assign and interpret the wid is a source of much confusion and must be fixed
		_, route := pjr.backRoute(workerIdx)
		msg.Wid = route
		pjr.toBus(msg)
	}
}

// A consumerRouter is the standard downstream tableRouter. Since workers in the downstream
// router(s) handle ingress traffic, the consumerRouter just muxes and demuxes to/from the bus
// without implementing any fancy routing logic
type consumerRouter struct {
	downstreamRouter
}

func NewConsumerRouter(bus *ipcChan, table *WorkerTable) *consumerRouter {
	cr := consumerRouter{
		downstreamRouter: downstreamRouter{
			baseRouter: baseRouter{
				bus:   bus,
				table: table,
			},
		},
	}

	cr.downstreamRouter.baseRouter.busHook = cr.busHook
	cr.downstreamRouter.baseRouter.workerHook = cr.workerHook

	return &cr
}

func (cr *consumerRouter) busHook(r *baseRouter, msg IPCMsg) {
	// TODO: we currently forward all msg types without any filter... maybe it's worth revisiting
	switch msg.Wid {
	case BroadcastRoute:
		cr.toAllWorkers(msg)
	default:
		cr.toWorker(msg)
	}
}

func (cr *consumerRouter) workerHook(r *baseRouter, msg IPCMsg, workerIdx workerID) {
	// Do nothing
}

// WorkerTable ts the structure we use to represent the producer and consumer tables
type WorkerTable struct {
	size int
	slot []WorkerFSM
}

// Construct a new WorkerTable; len(list) corresponds to max concurrent connections for this table.
// By mixing WorkerFSMs, you can construct a table consisting of connections over different transports.
func NewWorkerTable(list []WorkerFSM) *WorkerTable {
	pt := WorkerTable{slot: list, size: len(list)}
	return &pt
}

// Start all of this table's workers
func (t WorkerTable) Start() {
	for i := range t.slot {
		t.slot[i].Start()
	}
}

// Stop all of this table's workers
func (t WorkerTable) Stop() {
	for i := range t.slot {
		t.slot[i].Stop()
	}
}

func (t WorkerTable) Size() int {
	return t.size
}
