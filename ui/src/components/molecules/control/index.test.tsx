import React from 'react'
import {render, screen} from '@testing-library/react'
import Control from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings, Targets} from '../../../constants'
import {hasWebAssembly} from '../../../utils/capabilities'

jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key}),
}))

jest.mock('../../../utils/capabilities', () => ({hasWebAssembly: jest.fn()}))

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

const renderControl = () => render(
	<AppContextProvider value={{
		width: 0,
		setWidth: () => {},
		settings: {...defaultSettings, target: Targets.WEB},
		wasmInterface: {instance: undefined} as any,
	}}>
		<Control />
	</AppContextProvider>
)

describe('status control', () => {
	// iOS Lockdown Mode removes WebAssembly, so the client can never start. The
	// switch used to spin and then silently fall back to OFF.
	test('disables the switch when WebAssembly is unavailable', () => {
		;(hasWebAssembly as jest.Mock).mockReturnValue(false)
		renderControl()
		expect(screen.getByLabelText('connect')).toBeDisabled()
	})

	test('enables the switch when WebAssembly is available', () => {
		;(hasWebAssembly as jest.Mock).mockReturnValue(true)
		renderControl()
		expect(screen.getByLabelText('connect')).toBeEnabled()
	})
})
