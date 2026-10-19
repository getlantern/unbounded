import {
	WasmInterface,
	countryStatsEmitter,
	downloadThroughputEmitter,
	lifetimeUploadBytesEmitter,
	mergeCountryStats,
	readyEmitter,
	resendSupportCountryChoice,
	sessionBytesEmitter,
	sendSupportCountry,
	sharingEmitter,
	uploadThroughputEmitter,
} from './wasmInterface'
import {
	ANYWHERE,
	forgetSupportCountryChoice,
	resolveSupportCountry,
	storeSupportCountry,
	supportCountryEmitter,
} from './supportCountry'
import {MessageTypes, SIGNATURE, Targets} from '../constants'
import {supportCountryEnabledEmitter} from './featureFlags'
import {lookupPeerCountry} from './geo'

jest.mock('./geo', () => ({lookupPeerCountry: jest.fn()}))
jest.mock('./goWasmExec', () => ({}))
jest.mock('./freezeWatchdog', () => ({installFreezeWatchdog: jest.fn()}))

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
	let clock = 0
	const advance = (ms: number) => { clock += ms }

	beforeEach(() => {
		jest.spyOn(performance, 'now').mockImplementation(() => clock)
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

	it('normalizes speeds by the time elapsed when the flush timer runs late', () => {
		const wi = new WasmInterface()
		wi.flushStats()
		wi.handleDownstreamChunk({detail: {size: 3000, workerIdx: 0}})
		wi.handleUpstreamChunk({detail: {size: 600, workerIdx: 0}})
		advance(3000)
		wi.flushStats()
		expect(downloadThroughputEmitter.state).toBe(1000)
		expect(uploadThroughputEmitter.state).toBe(200)
		expect(sessionBytesEmitter.state).toEqual({down: 3000, up: 600})
	})

	it('measures a partial interval from the previous flush and never spikes on an early one', () => {
		const wi = new WasmInterface()
		wi.flushStats()
		advance(400)
		wi.handleDownstreamChunk({detail: {size: 300, workerIdx: 0}})
		wi.flushStats()
		expect(downloadThroughputEmitter.state).toBe(300)
		wi.handleDownstreamChunk({detail: {size: 1500, workerIdx: 0}})
		advance(1500)
		wi.flushStats()
		expect(downloadThroughputEmitter.state).toBe(1000)
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

describe('extension support country commands', () => {
	let posted: any[]

	beforeEach(() => {
		posted = []
		supportCountryEnabledEmitter.update(true)
		jest.spyOn(window.parent, 'postMessage').mockImplementation((message: any) => { posted.push(message) })
	})

	afterEach(() => {
		jest.restoreAllMocks()
		window.localStorage.clear()
		forgetSupportCountryChoice()
		supportCountryEmitter.update(ANYWHERE)
		supportCountryEnabledEmitter.update(false)
	})

	const pick = (country: string) => {
		storeSupportCountry(country)
		supportCountryEmitter.update(country)
		sendSupportCountry(country)
	}

	it('keeps the latest choice when a stale echo reaches the popup before Start', () => {
		const popup = new WasmInterface()
		popup.target = Targets.EXTENSION_POPUP
		popup.wasmClient = {} as any
		popup.ready = true
		pick('IR')
		pick('RU')
		supportCountryEmitter.update('IR') // offscreen's delayed echo of the first pick
		popup.start()
		expect(posted.map(m => m.type)).toEqual([MessageTypes.WASM_SUPPORT_COUNTRY, MessageTypes.WASM_SUPPORT_COUNTRY, MessageTypes.WASM_START])
		expect(posted[2].data.supportCountry).toBeUndefined()

		// replay the popup's commands in a fresh offscreen context
		forgetSupportCountryChoice()
		window.localStorage.clear()
		supportCountryEmitter.update(ANYWHERE)
		const offscreen = new WasmInterface()
		offscreen.target = Targets.EXTENSION_OFFSCREEN
		offscreen.wasmClient = {start: jest.fn(), stop: jest.fn(), setTargetCountries: jest.fn()} as any
		offscreen.ready = true
		supportCountryEmitter.on(offscreen.applySupportCountry)
		posted.forEach(data => offscreen.onMessage({data} as MessageEvent))
		supportCountryEmitter.off(offscreen.applySupportCountry)
		expect(supportCountryEmitter.state).toBe('RU')
		expect(resolveSupportCountry('CN')).toBe('RU')
		expect(offscreen.wasmClient!.setTargetCountries).toHaveBeenLastCalledWith('RU')
		sharingEmitter.update(false)
	})

	it('keeps a choice sent while the offscreen client is still loading its wasm', () => {
		jest.spyOn(WebAssembly, 'instantiateStreaming').mockReturnValue(new Promise(() => {}))
		const fetchMock = jest.fn()
		Object.defineProperty(window, 'fetch', {value: fetchMock, configurable: true})
		const offscreen = new WasmInterface()
		offscreen.initialize({mock: false, target: Targets.EXTENSION_OFFSCREEN})
		window.dispatchEvent(new MessageEvent('message', {
			data: {type: MessageTypes.WASM_SUPPORT_COUNTRY, [SIGNATURE]: true, data: {supportCountry: 'RU'}}
		}))
		window.removeEventListener('message', offscreen.onMessage)
		supportCountryEmitter.off(offscreen.applySupportCountry)
		expect(supportCountryEmitter.state).toBe('RU')
		expect(resolveSupportCountry('CN')).toBe('RU')
	})
})

describe('support country feature flag', () => {
	const client = () => ({start: jest.fn(), stop: jest.fn(), setTargetCountries: jest.fn()})
	const listening: WasmInterface[] = []

	const offscreen = () => {
		const wasm = new WasmInterface()
		wasm.target = Targets.EXTENSION_OFFSCREEN
		wasm.wasmClient = client() as any
		wasm.ready = true
		supportCountryEmitter.on(wasm.applySupportCountry)
		supportCountryEnabledEmitter.on(wasm.applySupportCountryEnabled)
		listening.push(wasm)
		return wasm
	}

	afterEach(() => {
		listening.splice(0).forEach(wasm => {
			supportCountryEmitter.off(wasm.applySupportCountry)
			supportCountryEnabledEmitter.off(wasm.applySupportCountryEnabled)
		})
		window.localStorage.clear()
		forgetSupportCountryChoice()
		supportCountryEmitter.update(ANYWHERE)
		supportCountryEnabledEmitter.update(false)
		sharingEmitter.update(false)
	})

	it('drops a popup\'s country command while off, without storing it', () => {
		const wasm = offscreen()
		wasm.onMessage({data: {type: MessageTypes.WASM_SUPPORT_COUNTRY, [SIGNATURE]: true, data: {supportCountry: 'IR'}}} as MessageEvent)
		expect(supportCountryEmitter.state).toBe(ANYWHERE)
		expect(resolveSupportCountry('')).toBe(ANYWHERE)
		expect(wasm.wasmClient!.setTargetCountries).not.toHaveBeenCalled()
	})

	it('applies no country and starts with none while off, whatever the emitter holds', () => {
		const wasm = offscreen()
		supportCountryEmitter.update('IR')
		expect(wasm.wasmClient!.setTargetCountries).not.toHaveBeenCalled()
		wasm.start()
		expect(wasm.wasmClient!.setTargetCountries).toHaveBeenCalledTimes(1)
		expect(wasm.wasmClient!.setTargetCountries).toHaveBeenCalledWith('')
	})

	it('applies the country when the flag turns on and clears it when it turns off', () => {
		const wasm = offscreen()
		supportCountryEmitter.update('IR')
		supportCountryEnabledEmitter.update(true)
		expect(wasm.wasmClient!.setTargetCountries).toHaveBeenLastCalledWith('IR')
		supportCountryEnabledEmitter.update(false)
		expect(wasm.wasmClient!.setTargetCountries).toHaveBeenLastCalledWith('')
	})

	it('never applies a country from the popup itself', () => {
		const popup = new WasmInterface()
		popup.target = Targets.EXTENSION_POPUP
		popup.wasmClient = client() as any
		supportCountryEnabledEmitter.on(popup.applySupportCountryEnabled)
		supportCountryEmitter.update('IR')
		supportCountryEnabledEmitter.update(true)
		supportCountryEnabledEmitter.off(popup.applySupportCountryEnabled)
		expect(popup.wasmClient!.setTargetCountries).not.toHaveBeenCalled()
	})

	it('does not re-send the popup viewer\'s choice while off', () => {
		const posted: any[] = []
		jest.spyOn(window.parent, 'postMessage').mockImplementation((message: any) => { posted.push(message) })
		storeSupportCountry('IR')
		resendSupportCountryChoice()
		expect(posted).toEqual([])
		supportCountryEnabledEmitter.update(true)
		resendSupportCountryChoice()
		expect(posted.map(m => m.data.supportCountry)).toEqual(['IR'])
		jest.restoreAllMocks()
	})
})
