import {renderHook} from '@testing-library/react'
import useMessaging from './useMessaging'
import {MessageTypes, SIGNATURE, Targets} from '../constants'
import {ANYWHERE, forgetSupportCountryChoice, storeSupportCountry, supportCountryEmitter} from '../utils/supportCountry'
import {readyEmitter} from '../utils/wasmInterface'
import {supportCountryEnabledEmitter} from '../utils/featureFlags'

jest.mock('../utils/goWasmExec', () => ({}))

const fromOffscreen = (emitter: string, value: any) => window.dispatchEvent(new MessageEvent('message', {
	data: {type: MessageTypes.STATE_UPDATE, [SIGNATURE]: true, data: {emitter, value}}
}))

describe('popup messaging', () => {
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
		readyEmitter.update(false)
		supportCountryEnabledEmitter.update(false)
	})

	const countryCommands = () => posted.filter(m => m.type === MessageTypes.WASM_SUPPORT_COUNTRY).map(m => m.data.supportCountry)

	it('re-sends a choice made before the offscreen client was listening once it reports ready', () => {
		const {unmount} = renderHook(() => useMessaging(Targets.EXTENSION_POPUP))
		storeSupportCountry('IR')
		storeSupportCountry('RU') // both sends were lost: offscreen wasn't listening yet
		fromOffscreen('supportCountryEmitter', 'IR')
		fromOffscreen('readyEmitter', true)
		expect(countryCommands()).toEqual(['RU'])
		unmount()
	})

	it('mirrors the offscreen flag and sends no choice while the feature is off', () => {
		supportCountryEnabledEmitter.update(false)
		const {unmount} = renderHook(() => useMessaging(Targets.EXTENSION_POPUP))
		storeSupportCountry('RU')
		fromOffscreen('supportCountryEnabledEmitter', false)
		fromOffscreen('readyEmitter', true)
		expect(countryCommands()).toEqual([])
		// the offscreen client dropped any earlier send, so the choice goes once the flag turns on
		fromOffscreen('supportCountryEnabledEmitter', true)
		expect(supportCountryEnabledEmitter.state).toBe(true)
		expect(countryCommands()).toEqual(['RU'])
		unmount()
	})

	it('sends nothing on ready when the viewer has not chosen', () => {
		const {unmount} = renderHook(() => useMessaging(Targets.EXTENSION_POPUP))
		fromOffscreen('supportCountryEmitter', 'IR')
		fromOffscreen('readyEmitter', true)
		expect(countryCommands()).toEqual([])
		unmount()
	})
})
