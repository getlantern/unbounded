import React from 'react'
import {render, screen} from '@testing-library/react'
import UnsupportedNote from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings} from '../../../constants'
import {hasWebAssembly} from '../../../utils/capabilities'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key}),
}))

jest.mock('../../../utils/capabilities', () => ({hasWebAssembly: jest.fn()}))

// context.tsx's default value reaches the Go wasm glue, which throws in jsdom.
jest.mock('../../../utils/wasmInterface', () => ({WasmInterface: class {}}))

const renderNote = () => render(
	<AppContextProvider value={{width: 0, setWidth: () => {}, settings: defaultSettings, wasmInterface: undefined as any}}>
		<UnsupportedNote />
	</AppContextProvider>
)

describe('unsupported-browser note', () => {
	// The disabled switch alone reads as broken. Someone in Lockdown Mode needs
	// the reason to know they can exempt the site.
	test('explains why sharing is unavailable when WebAssembly is missing', () => {
		;(hasWebAssembly as jest.Mock).mockReturnValue(false)
		renderNote()
		expect(screen.getByText('unsupportedBrowser')).toBeInTheDocument()
	})

	test('renders nothing when WebAssembly is available', () => {
		;(hasWebAssembly as jest.Mock).mockReturnValue(true)
		const {container} = renderNote()
		expect(container).toBeEmptyDOMElement()
	})
})
