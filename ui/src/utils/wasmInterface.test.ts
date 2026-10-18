import {
	WasmInterface,
	countryStatsEmitter,
	downloadThroughputEmitter,
	lifetimeUploadBytesEmitter,
	mergeCountryStats,
	readyEmitter,
	sessionBytesEmitter,
	sharingEmitter,
	uploadThroughputEmitter,
} from './wasmInterface'
import {lookupPeerCountry} from './geo'

jest.mock('./geo', () => ({lookupPeerCountry: jest.fn()}))
jest.mock('./goWasmExec', () => ({}))

const mockLookup = lookupPeerCountry as jest.MockedFunction<typeof lookupPeerCountry>

const flushPromises = () => new Promise(resolve => setTimeout(resolve, 0))

describe('mergeCountryStats', () => {
	it('adds deltas to existing countries and appends new ones without mutating the input', () => {
		const stats = [{iso: 'IR', down: 10, up: 1, people: 1}]
		const merged = mergeCountryStats(stats, [
			{iso: 'IR', down: 5, up: 2, people: 1},
			{iso: 'RU', down: 3, up: 0, people: 1},
		])
		expect(merged).toEqual([
			{iso: 'IR', down: 15, up: 3, people: 2},
			{iso: 'RU', down: 3, up: 0, people: 1},
		])
		expect(stats).toEqual([{iso: 'IR', down: 10, up: 1, people: 1}])
	})
})

describe('WasmInterface byte accounting', () => {
	beforeEach(() => {
		countryStatsEmitter.update([])
		sessionBytesEmitter.update({down: 0, up: 0})
		lifetimeUploadBytesEmitter.update(0)
		uploadThroughputEmitter.update(0)
		downloadThroughputEmitter.update(0)
		mockLookup.mockReset()
	})

	it('credits bytes sent before the country lookup resolves to that country', async () => {
		let resolve: (iso: any) => void = () => {}
		mockLookup.mockReturnValue(new Promise(r => resolve = r))
		const wi = new WasmInterface()
		wi.trackPeer({state: 1, workerIdx: 0, addr: '1.2.3.4'})
		wi.handleDownstreamChunk({detail: {size: 100, workerIdx: 0}})
		wi.handleUpstreamChunk({detail: {size: 20, workerIdx: 0}})
		resolve('IR')
		await flushPromises()
		wi.handleDownstreamChunk({detail: {size: 50, workerIdx: 0}})
		wi.flushStats()

		expect(countryStatsEmitter.state).toEqual([{iso: 'IR', down: 150, up: 20, people: 1}])
		expect(sessionBytesEmitter.state).toEqual({down: 150, up: 20})
		expect(lifetimeUploadBytesEmitter.state).toBe(20)
		expect(uploadThroughputEmitter.state).toBe(20)
	})

	it('keeps a slow lookup attributed to the person it was started for after the slot is reused', async () => {
		let resolveFirst: (iso: any) => void = () => {}
		mockLookup
			.mockReturnValueOnce(new Promise(r => resolveFirst = r))
			.mockReturnValueOnce(Promise.resolve('RU'))
		const wi = new WasmInterface()
		wi.trackPeer({state: 1, workerIdx: 0, addr: '1.1.1.1'})
		wi.handleDownstreamChunk({detail: {size: 10, workerIdx: 0}})
		wi.trackPeer({state: 1, workerIdx: 0, addr: '2.2.2.2'})
		await flushPromises()
		wi.handleDownstreamChunk({detail: {size: 30, workerIdx: 0}})
		resolveFirst('IR')
		await flushPromises()
		wi.flushStats()

		const byIso = Object.fromEntries(countryStatsEmitter.state.map(s => [s.iso, s]))
		expect(byIso.IR).toEqual({iso: 'IR', down: 10, up: 0, people: 1})
		expect(byIso.RU).toEqual({iso: 'RU', down: 30, up: 0, people: 1})
	})

	it('buckets peers whose country is unknown under an empty iso', async () => {
		mockLookup.mockReturnValue(Promise.resolve(null))
		const wi = new WasmInterface()
		wi.trackPeer({state: 1, workerIdx: 1, addr: ''})
		await flushPromises()
		wi.handleUpstreamChunk({detail: {size: 7, workerIdx: 1}})
		wi.flushStats()
		expect(countryStatsEmitter.state).toEqual([{iso: '', down: 0, up: 7, people: 1}])
	})

	it('sums upload rates across embeds instead of letting an idle one zero them', () => {
		const active = new WasmInterface()
		const idle = new WasmInterface()
		active.handleUpstreamChunk({detail: {size: 40, workerIdx: 0}})
		idle.flushStats()
		expect(uploadThroughputEmitter.state).toBe(40)
		active.handleUpstreamChunk({detail: {size: 5, workerIdx: 0}})
		idle.handleUpstreamChunk({detail: {size: 3, workerIdx: 0}})
		active.flushStats()
		expect(uploadThroughputEmitter.state).toBe(8)
	})

	it('reports download speed as the bytes relayed in one flush interval', () => {
		const wi = new WasmInterface()
		wi.handleDownstreamChunk({detail: {size: 300, workerIdx: 0}})
		wi.handleDownstreamChunk({detail: {size: 200, workerIdx: 1}})
		wi.flushStats()
		expect(downloadThroughputEmitter.state).toBe(500)
		wi.flushStats()
		expect(downloadThroughputEmitter.state).toBe(0)
	})

	it('credits teardown bytes from the previous session to lifetime totals, not the restarted session', () => {
		const wi = new WasmInterface()
		wi.wasmClient = {start: jest.fn(), stop: jest.fn()} as any
		wi.ready = true
		wi.handleUpstreamChunk({detail: {size: 9, workerIdx: 0}})
		wi.start()
		expect(lifetimeUploadBytesEmitter.state).toBe(9)
		expect(sessionBytesEmitter.state).toEqual({down: 0, up: 0})
		wi.flushStats()
		expect(sessionBytesEmitter.state).toEqual({down: 0, up: 0})
		sharingEmitter.update(false)
		readyEmitter.update(false)
	})
})
