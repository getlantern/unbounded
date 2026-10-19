import {act, renderHook} from '@testing-library/react'
import useSupportCountryPreset from './useSupportCountryPreset'
import {ANYWHERE, forgetSupportCountryChoice, storeSupportCountry, supportCountryEmitter} from '../utils/supportCountry'
import {supportCountryEnabledEmitter} from '../utils/featureFlags'

afterEach(() => {
	window.localStorage.clear()
	forgetSupportCountryChoice()
	supportCountryEmitter.update(ANYWHERE)
	supportCountryEnabledEmitter.update(false)
})

describe('useSupportCountryPreset', () => {
	it('ignores the embed preset and a stored choice while the feature is off', () => {
		storeSupportCountry('RU')
		forgetSupportCountryChoice() // as on a later page view: only storage holds it
		renderHook(() => useSupportCountryPreset('IR'))
		expect(supportCountryEmitter.state).toBe(ANYWHERE)
	})

	it('applies the preset when the feature turns on after load', () => {
		renderHook(() => useSupportCountryPreset('IR'))
		expect(supportCountryEmitter.state).toBe(ANYWHERE)
		act(() => supportCountryEnabledEmitter.update(true))
		expect(supportCountryEmitter.state).toBe('IR')
	})

	it('prefers the viewer\'s stored choice once on', () => {
		supportCountryEnabledEmitter.update(true)
		storeSupportCountry('RU')
		renderHook(() => useSupportCountryPreset('IR'))
		expect(supportCountryEmitter.state).toBe('RU')
	})
})
