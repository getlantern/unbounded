import {readyEmitter, sharingEmitter, connectionsEmitter, averageThroughputEmitter, lifetimeConnectionsEmitter, lifetimeChunksEmitter} from './utils/wasmInterface'

// Mock WasmInterface before importing headlessApi
jest.mock('./utils/wasmInterface', () => {
	const {StateEmitter} = jest.requireActual('./hooks/useStateEmitter')
	const readyEmitter = new StateEmitter(false)
	const sharingEmitter = new StateEmitter(false)
	const connectionsEmitter = new StateEmitter([])
	const averageThroughputEmitter = new StateEmitter(0)
	const lifetimeConnectionsEmitter = new StateEmitter(0)
	const lifetimeChunksEmitter = new StateEmitter([])

	const mockInstance = {}
	const WasmInterface = jest.fn().mockImplementation(() => ({
		initialize: jest.fn().mockResolvedValue(mockInstance),
		start: jest.fn(),
		stop: jest.fn(),
	}))

	return {
		WasmInterface,
		readyEmitter,
		sharingEmitter,
		connectionsEmitter,
		averageThroughputEmitter,
		lifetimeConnectionsEmitter,
		lifetimeChunksEmitter,
	}
})

// Import after mock is set up
import {LanternProxy} from './headlessApi'

beforeEach(() => {
	// Reset emitter state between tests
	readyEmitter.update(false)
	sharingEmitter.update(false)
	connectionsEmitter.update([])
	averageThroughputEmitter.update(0)
	lifetimeConnectionsEmitter.update(0)
	lifetimeChunksEmitter.update([])
})

describe('LanternProxy.on / off', () => {
	test('on() delivers emitter updates to subscribers', () => {
		const cb = jest.fn()
		LanternProxy.on('ready', cb)
		readyEmitter.update(true)
		expect(cb).toHaveBeenCalledWith(true)
	})

	test('on() returns an unsubscribe function', () => {
		const cb = jest.fn()
		const unsub = LanternProxy.on('throughput', cb)
		averageThroughputEmitter.update(100)
		expect(cb).toHaveBeenCalledTimes(1)

		unsub()
		averageThroughputEmitter.update(200)
		expect(cb).toHaveBeenCalledTimes(1) // no new calls
	})

	test('off() removes a specific callback', () => {
		const cb1 = jest.fn()
		const cb2 = jest.fn()
		LanternProxy.on('sharing', cb1)
		LanternProxy.on('sharing', cb2)

		LanternProxy.off('sharing', cb1)
		sharingEmitter.update(true)

		expect(cb1).not.toHaveBeenCalled()
		expect(cb2).toHaveBeenCalledWith(true)
	})

	test('multiple event types work independently', () => {
		const readyCb = jest.fn()
		const connCb = jest.fn()
		LanternProxy.on('ready', readyCb)
		LanternProxy.on('connections', connCb)

		readyEmitter.update(true)
		expect(readyCb).toHaveBeenCalledWith(true)
		expect(connCb).not.toHaveBeenCalled()

		const conns = [{state: 1, workerIdx: 0, addr: '1.2.3.4'}]
		connectionsEmitter.update(conns)
		expect(connCb).toHaveBeenCalledWith(conns)
	})
})

describe('LanternProxy.getState', () => {
	test('returns current emitter state', () => {
		readyEmitter.update(true)
		sharingEmitter.update(true)
		averageThroughputEmitter.update(500)
		lifetimeConnectionsEmitter.update(42)

		const state = LanternProxy.getState()
		expect(state.ready).toBe(true)
		expect(state.sharing).toBe(true)
		expect(state.throughput).toBe(500)
		expect(state.lifetimeConnections).toBe(42)
	})
})

describe('LanternProxy.init', () => {
	test('concurrent calls return the same promise', () => {
		const p1 = LanternProxy.init()
		const p2 = LanternProxy.init()
		expect(p1).toBe(p2)
	})
})

describe('window.LanternProxy', () => {
	test('is exposed globally', () => {
		expect((window as any).LanternProxy).toBe(LanternProxy)
	})

	test('is not writable', () => {
		expect(() => {
			(window as any).LanternProxy = 'overwrite'
		}).toThrow()
	})
})
