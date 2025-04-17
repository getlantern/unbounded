import go from './goWasmExec'
import {StateEmitter} from '../hooks/useStateEmitter'
import MockWasmClient from '../mocks/mockWasmClient'
import {MessageTypes, SIGNATURE, Targets, WASM_CLIENT_CONFIG} from '../constants'
import {messageCheck} from './messages'

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

// create state emitters
export const connectionsEmitter = new StateEmitter<Connection[]>([])
export const averageThroughputEmitter = new StateEmitter<number>(0)
export const lifetimeConnectionsEmitter = new StateEmitter<number>(0)
export const lifetimeChunksEmitter = new StateEmitter<Chunk[]>([])
export const readyEmitter = new StateEmitter<boolean>(false)
export const sharingEmitter = new StateEmitter<boolean>(false)


export interface WasmClientEventMap {
	'ready': CustomEvent;
	'downstreamChunk': { detail: Chunk };
	'downstreamThroughput': { detail: Throughput };
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
}


// bind the client constructor
declare global {
	function newBroflake(
		type: string,
		cTableSz: number,
		pTableSz: number,
		busBufSz: number,
		netstated: string,
		webTransport: boolean,
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


// a WebTransport bridge used by Go to send and receive datagrams since Go compiled to WASM can't access webtransport
export class WebTransportBridge {
	wt!: WebTransport

	datagramReader!: ReadableStreamDefaultReader<Uint8Array>
	datagramWriter!: WritableStreamDefaultWriter<Uint8Array>
	connected: boolean = false
	connecting: Promise<boolean> | null = null;

	id: number = -1
	receiveFunction!: (datagram: Uint8Array) => void
	readLoopPromise: Promise<void> | null = null

	constructor(id: number) {
		this.id = id;

		// bind 2 functions to global window for Go to call
		const callbacks = (window as any).WebTransportCallbacks;
		callbacks.connect[this.id] = this.connect.bind(this);
		callbacks.send[this.id] = this.send.bind(this);
	}

	async connect(url: string): Promise<boolean> {
		if (this.connected) return true;
		if (this.connecting) return this.connecting;

		this.connecting = (async () => {
			try {
				console.debug("[JS WT:%d]: connecting to %s", this.id, url);
				this.wt = new WebTransport(url);
				await this.wt.ready;

				this.datagramReader = this.wt.datagrams.readable.getReader();
				this.datagramWriter = this.wt.datagrams.writable.getWriter();

				const callbacks = (window as any).WebTransportCallbacks;
				this.receiveFunction = callbacks.receive[this.id];
				if (!this.receiveFunction) {
					throw new Error(`Missing Go receive function for instance ${this.id}`);
				}

				this.connected = true;
				this.readLoopPromise = this.readLoop();
				return true;
			} catch (e) {
				console.error(`[JS WT:${this.id}]: connection error`, e);
				return false;
			} finally {
				this.connecting = null;
			}
		})();
		return this.connecting;
	}

	// readLoop reads incoming datagrams and forward the data to Go by calling this.receiveFunction
	async readLoop() {
		while (true) {
			try {
				const { value, done } = await this.datagramReader.read();
				if (done || !value) continue;
				this.receiveFunction(value); // Pass the received datagram to the function in Go
			} catch (error) {
				console.warn("[JS WT:%d]: error reading datagram:%s", this.id, error);
				this.connected = false;
				this.wt.close();
				// disconnect in Go, so FSM can resume
				this.goDisconnect();
				break;
			}
		}
	}

	// this function will be called by Go through "sendWebTransportDatagramJS" + id"
	async send(data: Uint8Array): Promise<void> {
		try {
			await this.datagramWriter.write(data);
		} catch (e) {
			console.debug(`[JS WT:${this.id}]: send failed`, e);
			throw e;
		}
	}

	// this will be called in WasmInterface.stop()
	async disconnect() {
		if (!this.connected) {
			return;
		}
		// specifically close the datagram writer to force stop the write() because sometimes when the
		// write() is in progress it hangs there forever even if the wt.close() is called
		try {
			await this.datagramWriter.close();
		// we don't care about this error because either way the webtransport will be closed by the next line
		} catch (_) {}

		// close the webtransport, which will force all read() to finish
		this.wt.close();

		// wait for readLoop to finish
		if (this.readLoopPromise) {
			try {
				await this.readLoopPromise;
			} catch (_) {}
		}
		this.goDisconnect();
		this.connected = false;
	}

	// call Go's disconnect so the FSM state can finish and stop
	goDisconnect() {
		const callbacks = (window as any).WebTransportCallbacks;
		if (callbacks.disconnect[this.id]) {
			callbacks.disconnect[this.id]();
		}
	}
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
	// states
	ready: boolean
	initializing: boolean
	target: Targets

	wtInstances!: Array<WebTransportBridge>

	constructor() {
		this.ready = false
		this.initializing = false
		this.connectionMap = {}
		this.throughput = {bytesPerSec: 0}
		this.connections = []
		this.go = go
		this.target = Targets.WEB
	}

	initialize = async ({mock, target}: Config): Promise<WebAssemblyInstance | undefined> => {
		// this dumb state is needed to prevent multiple calls to initialize from react hot reload dev server 🥵
		if (this.initializing || this.instance) { // already initialized or initializing
			console.warn('Wasm client has already been initialized or is initializing, aborting init.')
		  return
		}

		// check if we need webtransport and initialize it
		if (WASM_CLIENT_CONFIG.webTransport) {
			// initialize WebTransport callbacks for JS<->Go communications
			// which contains 4 methods (as key name), each is a object that maps webtransport instance id to the actual function
			(window as any).WebTransportCallbacks = {
				connect: {},	// set in WebTransportBridge constructor, defined in JS
				send: {},		// set in WebTransportBridge constructor, defined in JS
				receive: {}, 	// set in Go's newJSWebTransportConn(), defined in Go
				disconnect: {},	// set in Go's newJSWebTransportConn(), defined in Go
			};
			console.log('JS: initializing %d webtransport instances', WASM_CLIENT_CONFIG.cTableSz);
			this.wtInstances = Array.from({length: WASM_CLIENT_CONFIG.cTableSz}, (_, i) => new WebTransportBridge(i))
		}

		this.initializing = true
		this.target = target
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
				WASM_CLIENT_CONFIG.webTransport,
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

			// disconnect the webtransport instances
			if (WASM_CLIENT_CONFIG.webTransport) {
				console.debug('JS: disconnecting %d webtransport instances...', WASM_CLIENT_CONFIG.cTableSz);
				await Promise.all(this.wtInstances.map(wt => wt.disconnect()));
				console.debug("All %d webtransport instances disconnected", WASM_CLIENT_CONFIG.cTableSz);
			}
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

	handleThroughput = (e: { detail: Throughput }) => {
		const {detail} = e
		this.throughput = detail
		// calc moving average for time series smoothing and emit state
		averageThroughputEmitter.update((averageThroughputEmitter.state + detail.bytesPerSec) / 2)
	}

	handleConnection = (e: { detail: Connection }) => {
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
			lifetimeConnectionsEmitter.update(lifetimeConnectionsEmitter.state + 1)
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
		this.wasmClient.addEventListener('downstreamChunk', this.handleChunk)
		this.wasmClient.addEventListener('downstreamThroughput', this.handleThroughput)
		this.wasmClient.addEventListener('consumerConnectionChange', this.handleConnection)
		this.wasmClient.addEventListener('ready', this.handleReady)
	}
}
