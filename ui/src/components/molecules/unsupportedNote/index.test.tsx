import React from 'react'
import {render, screen} from '@testing-library/react'
import UnsupportedNote from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings} from '../../../constants'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key}),
}))

// context.tsx's default value reaches the Go wasm glue, which throws in jsdom.
jest.mock('../../../utils/wasmInterface', () => ({WasmInterface: class {}}))

const original = Object.getOwnPropertyDescriptor(globalThis, 'WebAssembly')
const setWebAssembly = (available: boolean) => Object.defineProperty(globalThis, 'WebAssembly', {
	value: available ? {instantiateStreaming: () => {}} : undefined, configurable: true, writable: true,
})
afterEach(() => { if (original) Object.defineProperty(globalThis, 'WebAssembly', original) })

const renderNote = (mock = false) => render(
	<AppContextProvider value={{width: 0, setWidth: () => {}, settings: {...defaultSettings, mock}, wasmInterface: undefined as any}}>
		<UnsupportedNote />
	</AppContextProvider>
)

describe('unsupported-browser note', () => {
	// The disabled switch alone reads as broken. Someone in Lockdown Mode needs
	// the reason to know they can exempt the site.
	test('explains why sharing is unavailable when WebAssembly is missing', () => {
		setWebAssembly(false)
		renderNote()
		expect(screen.getByText('unsupportedBrowser')).toBeInTheDocument()
	})

	test('renders nothing when WebAssembly is available', () => {
		setWebAssembly(true)
		const {container} = renderNote()
		expect(container).toBeEmptyDOMElement()
	})

	// Must agree with the switch, which stays enabled in mock mode.
	test('renders nothing in mock mode without WebAssembly', () => {
		setWebAssembly(false)
		const {container} = renderNote(true)
		expect(container).toBeEmptyDOMElement()
	})
})
