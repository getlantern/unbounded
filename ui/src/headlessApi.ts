/**
 * Headless API for controlling the unbounded WASM proxy without rendering any UI.
 *
 * Usage:
 *   <browsers-unbounded data-headless="true"></browsers-unbounded>
 *   <script src="https://embed.lantern.io/static/js/main.js"></script>
 *   <script>
 *     const proxy = window.LanternProxy;
 *     await proxy.init();
 *     proxy.on('ready', () => proxy.start());
 *     proxy.on('connections', (conns) => console.log(conns));
 *     proxy.on('throughput', (bps) => console.log(bps));
 *   </script>
 */

import {WasmInterface, connectionsEmitter, averageThroughputEmitter, lifetimeConnectionsEmitter, lifetimeChunksEmitter, readyEmitter, sharingEmitter, type Connection, type Chunk} from './utils/wasmInterface'
import {Targets, WASM_CLIENT_CONFIG} from './constants'

export type ProxyEvent = 'ready' | 'sharing' | 'connections' | 'throughput' | 'lifetimeConnections' | 'chunks'

export interface ProxyState {
	ready: boolean
	sharing: boolean
	connections: Connection[]
	throughput: number
	lifetimeConnections: number
	chunks: Chunk[]
}

type EventCallback<T = unknown> = (value: T) => void

const listeners = new Map<string, Set<EventCallback>>()

function emitToListeners(event: string, value: unknown) {
	const set = listeners.get(event)
	if (set) set.forEach(cb => cb(value))
}

// Wire up emitters to forward to external listeners
function wireEmitters() {
	readyEmitter.on((v) => emitToListeners('ready', v))
	sharingEmitter.on((v) => emitToListeners('sharing', v))
	connectionsEmitter.on((v) => emitToListeners('connections', v))
	averageThroughputEmitter.on((v) => emitToListeners('throughput', v))
	lifetimeConnectionsEmitter.on((v) => emitToListeners('lifetimeConnections', v))
	lifetimeChunksEmitter.on((v) => emitToListeners('chunks', v))
}

let wasmInterface: WasmInterface | null = null
let initialized = false

export const LanternProxy = {
	/**
	 * Initialize the WASM proxy. Must be called before start().
	 * @param options.mock - Use mock client for testing (default: false)
	 */
	async init(options?: { mock?: boolean }): Promise<void> {
		if (initialized) {
			console.warn('LanternProxy already initialized')
			return
		}
		const mock = options?.mock ?? false
		wasmInterface = new WasmInterface()
		const instance = await wasmInterface.initialize({mock, target: Targets.WEB})
		if (!instance) throw new Error('WASM proxy failed to initialize')
		initialized = true
	},

	/** Start proxying traffic. Resolves once sharing begins. */
	start(): void {
		if (!wasmInterface) throw new Error('LanternProxy not initialized — call init() first')
		wasmInterface.start()
	},

	/** Stop proxying traffic. */
	stop(): void {
		if (!wasmInterface) throw new Error('LanternProxy not initialized — call init() first')
		wasmInterface.stop()
	},

	/** Subscribe to a proxy event. Returns an unsubscribe function. */
	on<T = unknown>(event: ProxyEvent, callback: EventCallback<T>): () => void {
		if (!listeners.has(event)) listeners.set(event, new Set())
		const set = listeners.get(event)!
		set.add(callback as EventCallback)
		return () => set.delete(callback as EventCallback)
	},

	/** Unsubscribe from a proxy event. */
	off(event: ProxyEvent, callback: EventCallback): void {
		listeners.get(event)?.delete(callback)
	},

	/** Get a snapshot of the current proxy state. */
	getState(): ProxyState {
		return {
			ready: readyEmitter.state,
			sharing: sharingEmitter.state,
			connections: connectionsEmitter.state,
			throughput: averageThroughputEmitter.state,
			lifetimeConnections: lifetimeConnectionsEmitter.state,
			chunks: lifetimeChunksEmitter.state,
		}
	},

	/** Whether init() has been called successfully. */
	get initialized(): boolean {
		return initialized
	},

	/** The WASM client config (discovery server, egress, etc). Read-only. */
	get config() {
		return {...WASM_CLIENT_CONFIG}
	},
}

// Wire emitters immediately so subscriptions work before init()
wireEmitters()

// Expose globally
;(window as any).LanternProxy = LanternProxy
