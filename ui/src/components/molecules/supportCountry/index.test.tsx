import React from 'react'
import {fireEvent, render, screen} from '@testing-library/react'
import SupportCountry from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings} from '../../../constants'
import {ANYWHERE, resolveSupportCountry, supportCountryEmitter} from '../../../utils/supportCountry'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key, i18n: {language: 'en'}}),
}))

// context.tsx's default value reaches the Go wasm glue, which throws in jsdom.
jest.mock('../../../utils/wasmInterface', () => ({WasmInterface: class {}}))

const renderPicker = () => render(
	<AppContextProvider value={{width: 0, setWidth: () => {}, settings: {...defaultSettings, mock: true}, wasmInterface: undefined as any}}>
		<SupportCountry />
	</AppContextProvider>
)

afterEach(() => {
	window.localStorage.clear()
	supportCountryEmitter.update(ANYWHERE)
})

describe('support country picker', () => {
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
})
