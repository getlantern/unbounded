import {mockAddr, mockRandomInt} from './mockData'
import {
	Connection,
	WasmClient,
	WasmClientEventMap,
	WasmInterface
} from '../utils/wasmInterface'

/***
	This is a mock implementation of the wasm client. It is used in place of the actual wasm client
 	when the MOCK_DATA env flag is set to true. The mock client fires fake events at a regular intervals.
 ***/

const defaultConnections: Connection[] = [...Array(mockAddr.length)].map((_, i) => {
	return {state: -1, addr: mockAddr[i], workerIdx: i}
})

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

class MockWasmClient implements WasmClient {
	connections: Connection[]
	tick: number
	interval: NodeJS.Timer | undefined
	wasmInterface: WasmInterface

	constructor(wasmInterface: WasmInterface) {
		this.wasmInterface = wasmInterface
		this.tick = 0
		this.connections = defaultConnections
	}

	start = async () => {
		const sleepMs = async (ms: number) => new Promise(resolve => setTimeout(resolve, ms))
		await sleepMs(1000)
		this.interval = setInterval(() => {
			const active = this.connections.filter(c => c.state === 1)
			const chunks = [...Array(active.length)].map((_, i) => (
				{
					size: mockRandomInt(0, 1000000),
					workerIdx: i
				}
			))
			let throughput = 0
			// fire fake chunk events
			chunks.forEach(chunk => {
				throughput += chunk.size
				this.wasmInterface.handleDownstreamChunk({detail: chunk})
				this.wasmInterface.handleUpstreamChunk({detail: {...chunk, size: Math.round(chunk.size / 8)}})
			})
			// fire fake throughput events
			this.wasmInterface.handleThroughput({detail: {bytesPerSec: throughput}})
			if ((this.tick === 0 || this.tick % 10 === 0) && active.length !== mockAddr.length) {
				this.connections = this.connections.map((_, i) => (
					{state: i === active.length ? 1 : this.connections[i].state, addr: mockAddr[i], workerIdx: i}
				))
				const connection = this.connections[active.length]
				// fire fake connection event
				this.wasmInterface.handleConnection({detail: connection})
			}

			// CRAZY STRESS TEST CODE BELOW - DO NOT USE
			// if (this.tick % 10 === 0) {
			// 	// Randomly update one of the connection states
			// 	this.connections = this.connections.map((connection, i) => {
			// 		return {
			// 			...connection,
			// 			state: i === mockRandomInt(0, this.connections.length - 1) || i === mockRandomInt(0, this.connections.length - 1) ?  connection.state === 1 ? -1 : 1 : connection.state
			// 		}
			// 	});
			// 	// Trigger events for updated connections
			// 	this.connections.forEach(connection => {
			// 		this.wasmInterface.handleConnection({detail: connection});
			// 	});
			// }

			this.tick += 1
		}, 250)
	}

	stop = async () => {
		clearInterval(this.interval)
		this.connections = defaultConnections
		this.connections.forEach(connection => this.wasmInterface.handleConnection({detail: connection}))
		for (let i = 0; i < 50; i++) {
			await sleep(50)
			this.wasmInterface.handleThroughput({detail: {bytesPerSec: 0}})
		}
		this.wasmInterface.handleReady()
	}

	// these are just dumb stubs to satisfy the interface
	removeEventListener<K extends keyof WasmClientEventMap>(
		type: K,
		listener: (e: WasmClientEventMap[K]) => void,
		options?: boolean | AddEventListenerOptions
	): void
	removeEventListener() {}
	addEventListener<K extends keyof WasmClientEventMap>(
		type: K,
		listener: (e: WasmClientEventMap[K]) => void,
		options?: boolean | AddEventListenerOptions
	): void
	addEventListener() {}
	debug() {}
	ready() {}
	dispatchEvent(event: Event) { return false }

}

export default MockWasmClient