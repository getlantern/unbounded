import React from 'react'
import {act, fireEvent, render, screen} from '@testing-library/react'
import SupportCountry from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings, Targets} from '../../../constants'
import {ANYWHERE, forgetSupportCountryChoice, resolveSupportCountry, supportCountryEmitter} from '../../../utils/supportCountry'
import {supportCountryEnabledEmitter} from '../../../utils/featureFlags'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key, i18n: {language: 'en'}}),
}))

// context.tsx's default value reaches the Go wasm glue, which throws in jsdom.
jest.mock('../../../utils/wasmInterface', () => ({WasmInterface: class {}, sendSupportCountry: jest.fn()}))
const {sendSupportCountry} = jest.requireMock('../../../utils/wasmInterface')

const renderPicker = (target = defaultSettings.target) => render(
	<AppContextProvider value={{width: 0, setWidth: () => {}, settings: {...defaultSettings, mock: true, target}, wasmInterface: undefined as any}}>
		<SupportCountry />
	</AppContextProvider>
)

beforeEach(() => {
	supportCountryEnabledEmitter.update(true)
})

afterEach(() => {
	supportCountryEnabledEmitter.update(false)
	window.localStorage.clear()
	forgetSupportCountryChoice()
	supportCountryEmitter.update(ANYWHERE)
	sendSupportCountry.mockClear()
})

describe('support country picker', () => {
	test('is hidden while the feature is off and appears when it turns on', () => {
		supportCountryEnabledEmitter.update(false)
		renderPicker()
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
		act(() => supportCountryEnabledEmitter.update(true))
		expect(screen.getByRole('combobox')).toBeInTheDocument()
		act(() => supportCountryEnabledEmitter.update(false))
		expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
	})

	test('defaults to anywhere and offers only censored countries', () => {
		renderPicker()
		const select = screen.getByRole('combobox') as HTMLSelectElement
		expect(select.value).toBe(ANYWHERE)
		expect(screen.getByRole('option', {name: 'supportAnywhere'})).toBeInTheDocument()
		const values = Array.from(select.options).map(o => o.value)
		expect(values).toContain('IR')
		expect(values).not.toContain('US')
	})

	test('a choice updates the client preference and persists for this viewer', () => {
		renderPicker()
		fireEvent.change(screen.getByRole('combobox'), {target: {value: 'IR'}})
		expect(supportCountryEmitter.state).toBe('IR')
		expect(resolveSupportCountry('RU')).toBe('IR')
	})

	test('in the extension popup only a viewer choice is sent to the offscreen client', () => {
		renderPicker(Targets.EXTENSION_POPUP)
		fireEvent.change(screen.getByRole('combobox'), {target: {value: 'IR'}})
		expect(sendSupportCountry).toHaveBeenCalledTimes(1)
		expect(sendSupportCountry).toHaveBeenCalledWith('IR')
		// state mirrored back from the offscreen client must not be re-sent
		supportCountryEmitter.update('RU')
		expect(sendSupportCountry).toHaveBeenCalledTimes(1)
	})

	test('outside the popup nothing is sent', () => {
		renderPicker()
		fireEvent.change(screen.getByRole('combobox'), {target: {value: 'IR'}})
		expect(sendSupportCountry).not.toHaveBeenCalled()
	})
})
