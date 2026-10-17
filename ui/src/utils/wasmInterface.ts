import go from './goWasmExec'
import {StateEmitter} from '../hooks/useStateEmitter'
import MockWasmClient from '../mocks/mockWasmClient'
import {MessageTypes, SIGNATURE, Targets, WASM_CLIENT_CONFIG} from '../constants'
import {messageCheck} from './messages'
import {installFreezeWatchdog} from './freezeWatchdog'
import {lookupPeerCountry} from './geo'

type WebAssemblyInstance = InstanceType<typeof WebAssembly.Instance>

export interface Chunk {
	size: number
	workerIdx: number
}

export interface Connection {
	state: 1 | -1
	workerIdx: number
	addr: string
}

export interface Throughput {
	bytesPerSec: number
}

// down is data relayed to the people being helped, up is data they sent through the volunteer
export interface Bytes {
	down: number
	up: number
}

// iso is '' when the peer's country could not be determined
export interface CountryStat extends Bytes {
	iso: string
	people: number
}

interface Peer {
	iso: string
	resolved: boolean
	pending: Bytes
}

const STATS_FLUSH_MS = 1000

// create state emitters
export const connectionsEmitter = new StateEmitter<Connection[]>([])
export const averageThroughputEmitter = new StateEmitter<number>(0)
export const servedConnectionsEmitter = new StateEmitter<number>(0)
export const lifetimeConnectionsEmitter = new StateEmitter<number>(0)
export const lifetimeChunksEmitter = new StateEmitter<Chunk[]>([])
export const lifetimeUploadBytesEmitter = new StateEmitter<number>(0)
export const sessionBytesEmitter = new StateEmitter<Bytes>({down: 0, up: 0})
export const uploadThroughputEmitter = new StateEmitter<number>(0)
export const countryStatsEmitter = new StateEmitter<CountryStat[]>([])
export const readyEmitter = new StateEmitter<boolean>(false)
export const sharingEmitter = new StateEmitter<boolean>(false)


export interface WasmClientEventMap {
	'ready': CustomEvent;
	'downstreamChunk': { detail: Chunk };
	'downstreamThroughput': { detail: Throughput };
	'upstreamChunk': { detail: Chunk };
	'consumerConnectionChange': { detail: Connection };
}

export interface WasmClient extends EventTarget {
	addEventListener<K extends keyof WasmClientEventMap>(
		type: K,
		listener: (e: WasmClientEventMap[K]) => void,
		options?: boolean | AddEventListenerOptions
	): void

	addEventListener(
		type: string,
		callback: EventListenerOrEventListenerObject | null,
		options?: EventListenerOptions | boolean
	): void

	removeEventListener<K extends keyof WasmClientEventMap>(
		type: K,
		listener: (e: WasmClientEventMap[K]) => void,
		options?: boolean | AddEventListenerOptions
	): void

	removeEventListener(
		type: string,
		callback: EventListenerOrEventListenerObject | null,
		options?: EventListenerOptions | boolean
	): void

	start(): void

	stop(): void

	debug(): void

	// liveness returns the Go-side heartbeat (see clientcore/watchdog_wasm_impl.go).
	// Optional because the wasm binary deployed to embed.lantern.io can predate it —
	// it does today — and because MockWasmClient has no Go runtime to report on.
	liveness?(): {goTicks: number; goLastTickMs: number; goStartedMs: number; goIntervalMs: number; goNowMs: number} | undefined
}


// bind the client constructor
declare global {
	function newBroflake(
		type: string,
		cTableSz: number,
		pTableSz: number,
		busBufSz: number,
		netstated: string,
		discoverySrv: string,
		discoverySrvEndpoint: string,
		stunBatchSize: number,
		tag: string,
		egressAddr: string,
		egressEndpoint: string,
	): WasmClient;
}

interface Config {
	mock: boolean
	target: Targets
}

export const mergeCountryStats = (stats: CountryStat[], deltas: CountryStat[]): CountryStat[] => {
	const merged = stats.map(s => ({...s}))
	deltas.forEach(delta => {
		const found = merged.find(s => s.iso === delta.iso)
		if (!found) return merged.push({...delta})
		found.down += delta.down
		found.up += delta.up
		found.people += delta.people
	})
	return merged
}

export class WasmInterface {
	go: typeof go
	wasmClient: WasmClient | undefined
	instance: WebAssemblyInstance | undefined
	// raw data
	connectionMap: { [key: number]: Connection }
	throughput: Throughput
	// smoothed and agg data
	connections: Connection[]
	// per-session byte accounting, flushed to the emitters every STATS_FLUSH_MS so chunk-rate
	// events don't re-render the UI or rewrite storage on every chunk
	peers: { [key: number]: Peer }
	bytesAccum: Bytes
	countryAccum: { [iso: string]: CountryStat }
	statsInterval: ReturnType<typeof setInterval> | undefined
	// states
	ready: boolean
	initializing: boolean
	target: Targets

	constructor() {
		this.ready = false
		this.initializing = false
		this.connectionMap = {}
		this.throughput = {bytesPerSec: 0}
		this.connections = []
		this.peers = {}
		this.bytesAccum = {down: 0, up: 0}
		this.countryAccum = {}
		this.go = go
		this.target = Targets.WEB
	}

	initialize = async ({mock, target}: Config): Promise<WebAssemblyInstance | undefined> => {
		// this dumb state is needed to prevent multiple calls to initialize from react hot reload dev server 🥵
		if (this.initializing || this.instance) { // already initialized or initializing
			console.warn('Wasm client has already been initialized or is initializing, aborting init.')
		  return
		}

		this.initializing = true
		this.target = target

		// Start watching before instantiating, not after. Compiling a 12MB wasm
		// module is the single heaviest thing this page does and a plausible freeze
		// site, so a watchdog installed after it would be blind to exactly the
		// window most likely to hang. The liveness getter tolerates the client not
		// existing yet, which is the reason it is a getter and not a value.
		installFreezeWatchdog({
			liveness: () => this.wasmClient?.liveness?.(),
			sharing: () => sharingEmitter.state,
		})

		if (mock) { // fake it till you make it
			this.wasmClient = new MockWasmClient(this)
			this.instance = {} as WebAssemblyInstance
		} else { // the real deal (wasm)
			console.log('instantiate streaming')
			const res = await WebAssembly.instantiateStreaming(
				fetch(process.env.REACT_APP_WIDGET_WASM_URL!), this.go.importObject
			)
			this.instance = res.instance
			console.log('run instance')
			this.go.run(this.instance)
			console.log('building new client')
			this.buildNewClient()
		}
		this.initListeners()
		this.handleReady()
		this.initializing = false
		return this.instance
	}

	buildNewClient = (mock = false) => {
		if (mock) { // fake it till you make it
			this.wasmClient = new MockWasmClient(this)
		} else {
			this.wasmClient = globalThis.newBroflake(
				WASM_CLIENT_CONFIG.type,
				WASM_CLIENT_CONFIG.cTableSz,
				WASM_CLIENT_CONFIG.pTableSz,
				WASM_CLIENT_CONFIG.busBufSz,
				WASM_CLIENT_CONFIG.netstated,
				WASM_CLIENT_CONFIG.discoverySrv,
				WASM_CLIENT_CONFIG.discoverySrvEndpoint,
				WASM_CLIENT_CONFIG.stunBatchSize,
				WASM_CLIENT_CONFIG.tag,
				WASM_CLIENT_CONFIG.egressAddr,
				WASM_CLIENT_CONFIG.egressEndpoint
			)
		}
	}

	start = () => {
		if (!this.ready) return console.warn('Wasm client is not in ready state, aborting start')
		if (!this.wasmClient) return console.warn('Wasm client has not been initialized, aborting start.')
		// if the widget is running in an extension popup window, send message to the offscreen window
		if (this.target === Targets.EXTENSION_POPUP) {
			window.parent.postMessage({
				type: MessageTypes.WASM_START,
				[SIGNATURE]: true,
				data: {}
			}, '*')
		}
		else {
			sessionBytesEmitter.update({down: 0, up: 0})
			this.wasmClient.start()
			sharingEmitter.update(true)
		}
	}

	stop = async () => {
		if (!this.wasmClient) return console.warn('Wasm client has not been initialized, aborting stop.')
		// if the widget is running in an extension popup window, send message to the offscreen window
		if (this.target === Targets.EXTENSION_POPUP) {
			window.parent.postMessage({
				type: MessageTypes.WASM_STOP,
				[SIGNATURE]: true,
				data: {}
			}, '*')
		}
		else {
			this.ready = false
			readyEmitter.update(this.ready)
			this.wasmClient.stop()
			sharingEmitter.update(false)
			this.flushStats()
			uploadThroughputEmitter.update(0)
			// mark every connection disconnected so arcs and the helping count
			// clear on stop instead of lingering until the workers wind down.
			// (emitting an empty array would NOT clear arcs: useGeo's updateArcs
			// only removes arcs for connections it sees with state -1)
			const stopped: { [key: number]: Connection } = {}
			this.connections.forEach(c => stopped[c.workerIdx] = {...c, state: -1})
			this.connectionMap = stopped
			this.connections = this.idxMapToArr(this.connectionMap)
			connectionsEmitter.update(this.connections)
		}
	}

	idxMapToArr = (map: { [key: number]: any }) => {
		return Object.keys(map).map(idx => map[parseInt(idx)])
	}

	handleChunk = (e: { detail: Chunk }) => {
		const {detail} = e
		const chunks = [...lifetimeChunksEmitter.state, detail].reduce((acc: Chunk[], chunk) => {
			const found = acc.find((c: Chunk) => c.workerIdx === chunk.workerIdx)
			if (found) found.size += chunk.size
			else acc.push(chunk)
			return acc
		}, [])
		lifetimeChunksEmitter.update(chunks)
	}

	handleDownstreamChunk = (e: { detail: Chunk }) => {
		this.handleChunk(e)
		this.accumulate(e.detail.workerIdx, {down: e.detail.size, up: 0})
	}

	handleUpstreamChunk = (e: { detail: Chunk }) => {
		this.accumulate(e.detail.workerIdx, {down: 0, up: e.detail.size})
	}

	accumulate = (workerIdx: number, bytes: Bytes) => {
		this.bytesAccum.down += bytes.down
		this.bytesAccum.up += bytes.up
		const peer = this.peers[workerIdx]
		if (!peer) return
		if (peer.resolved) this.addCountry(peer.iso, bytes, 0)
		else {
			peer.pending.down += bytes.down
			peer.pending.up += bytes.up
		}
	}

	addCountry = (iso: string, bytes: Bytes, people: number) => {
		const stat = this.countryAccum[iso] || {iso, down: 0, up: 0, people: 0}
		stat.down += bytes.down
		stat.up += bytes.up
		stat.people += people
		this.countryAccum[iso] = stat
	}

	// a slot is reused for each new person, so each connection gets its own Peer; a lookup that
	// resolves after the slot moved on still credits the person it was started for
	trackPeer = ({workerIdx, addr}: Connection) => {
		const peer: Peer = {iso: '', resolved: false, pending: {down: 0, up: 0}}
		this.peers[workerIdx] = peer
		lookupPeerCountry(addr).then(iso => {
			peer.iso = iso || ''
			peer.resolved = true
			this.addCountry(peer.iso, peer.pending, 1)
			peer.pending = {down: 0, up: 0}
		})
	}

	flushStats = () => {
		const {down, up} = this.bytesAccum
		if (uploadThroughputEmitter.state !== up) uploadThroughputEmitter.update(up)
		if (down || up) {
			const session = sessionBytesEmitter.state
			sessionBytesEmitter.update({down: session.down + down, up: session.up + up})
			if (up) lifetimeUploadBytesEmitter.update(lifetimeUploadBytesEmitter.state + up)
			this.bytesAccum = {down: 0, up: 0}
		}
		const deltas = Object.values(this.countryAccum)
		if (!deltas.length) return
		this.countryAccum = {}
		countryStatsEmitter.update(mergeCountryStats(countryStatsEmitter.state, deltas))
	}

	handleThroughput = (e: { detail: Throughput }) => {
		const {detail} = e
		this.throughput = detail
		// calc moving average for time series smoothing and emit state
		averageThroughputEmitter.update((averageThroughputEmitter.state + detail.bytesPerSec) / 2)
	}

	handleConnection = (e: { detail: Connection }) => {
		// on web the wasm client's workers wind down asynchronously after
		// stop(), and its listeners stay attached; stop() already broadcast
		// the disconnected state, so drop late events — otherwise an in-flight
		// state:1 event re-activates an arc/count while the widget is off and
		// double-increments lifetimeConnections against the reset map. (the
		// extension popup receives synced state instead, so it is exempt)
		if (this.target !== Targets.EXTENSION_POPUP && !sharingEmitter.state) return
		const {detail: connection} = e
		const {state, workerIdx} = connection
		const existingState = this.connectionMap[workerIdx]?.state || -1
		this.connectionMap = {
			...this.connectionMap,
			[workerIdx]: connection
		}
		this.connections = this.idxMapToArr(this.connectionMap)
		// emit state
		connectionsEmitter.update(this.connections)
		if (existingState === -1 && state === 1) {
			this.trackPeer(connection)
			lifetimeConnectionsEmitter.update(lifetimeConnectionsEmitter.state + 1)
			servedConnectionsEmitter.update(servedConnectionsEmitter.state + 1)
		}
	}

	handleReady = () => {
		this.ready = true
		readyEmitter.update(this.ready)
	}

	onMessage = (event: MessageEvent) => {
		const message = event.data
		if (!messageCheck(message)) return
		switch (message.type) {
			case MessageTypes.WASM_START:
				this.start()
				break
			case MessageTypes.WASM_STOP:
				this.stop()
				break
		}
	}

	initListeners = () => {
		if (!this.wasmClient) return console.warn('Wasm client has not been initialized, aborting listener init.')

		// if the widget is running in an extension offscreen window, listen for messages from the popup (start/stop)
		if (this.target === Targets.EXTENSION_OFFSCREEN) window.addEventListener('message', this.onMessage)

		// register listeners
		this.wasmClient.addEventListener('downstreamChunk', this.handleDownstreamChunk)
		this.wasmClient.addEventListener('upstreamChunk', this.handleUpstreamChunk)
		this.wasmClient.addEventListener('downstreamThroughput', this.handleThroughput)
		this.wasmClient.addEventListener('consumerConnectionChange', this.handleConnection)
		this.wasmClient.addEventListener('ready', this.handleReady)

		if (!this.statsInterval) this.statsInterval = setInterval(this.flushStats, STATS_FLUSH_MS)
	}
}
