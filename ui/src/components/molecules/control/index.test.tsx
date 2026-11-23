import React from 'react'
import {render, screen} from '@testing-library/react'
import Control from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings, Targets} from '../../../constants'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key}),
}))

// Its import chain loads lottie-web, which needs a real canvas at load time.
jest.mock('../../../hooks/useGeoFuture', () => ({geoLookup: jest.fn()}))

// The real module's import chain reaches the Go wasm glue, which throws at load
// in jsdom. The toggle only needs its two emitters.
jest.mock('../../../utils/wasmInterface', () => {
	const {StateEmitter} = require('../../../hooks/useStateEmitter')
	return {
		WasmInterface: class {},
		readyEmitter: new StateEmitter(false),
		sharingEmitter: new StateEmitter(false),
	}
})

// Drives the real capability check through the global, as Lockdown Mode does.
const original = Object.getOwnPropertyDescriptor(globalThis, 'WebAssembly')
const setWebAssembly = (available: boolean) => Object.defineProperty(globalThis, 'WebAssembly', {
	value: available ? {instantiateStreaming: () => {}} : undefined, configurable: true, writable: true,
})
afterEach(() => { if (original) Object.defineProperty(globalThis, 'WebAssembly', original) })

const renderControl = (mock = false) => render(
	<AppContextProvider value={{
		width: 0,
		setWidth: () => {},
		settings: {...defaultSettings, target: Targets.WEB, mock},
		wasmInterface: {instance: undefined} as any,
	}}>
		<Control />
	</AppContextProvider>
)

describe('status control', () => {
	// iOS Lockdown Mode removes WebAssembly, so the client can never start. The
	// switch used to spin and then silently fall back to OFF.
	test('disables the switch when WebAssembly is unavailable', () => {
		setWebAssembly(false)
		renderControl()
		expect(screen.getByLabelText('connect')).toBeDisabled()
	})

	test('enables the switch when WebAssembly is available', () => {
		setWebAssembly(true)
		renderControl()
		expect(screen.getByLabelText('connect')).toBeEnabled()
	})

	// The mock client never touches WebAssembly, so demo embeds keep working.
	test('keeps the switch enabled in mock mode without WebAssembly', () => {
		setWebAssembly(false)
		renderControl(true)
		expect(screen.getByLabelText('connect')).toBeEnabled()
	})
})
