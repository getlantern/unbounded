import React from 'react'
import {render} from '@testing-library/react'
import Modal from './index'
import {AppContextProvider} from '../../../context'
import {defaultSettings, Layouts, Settings} from '../../../constants'

// The modal's copy is irrelevant here and an uninitialised i18next only adds
// noise to the run.
jest.mock('react-i18next', () => ({
	useTranslation: () => ({t: (key: string) => key}),
}))

// context.tsx instantiates a WasmInterface for its default value, and that
// module's import chain reaches the Go wasm glue, which throws at load in jsdom
// (no globalThis.crypto). The modal never uses it, so stub the module out.
jest.mock('../../../utils/wasmInterface', () => ({
	WasmInterface: class {},
}))

// onIgnore is wired by useSharingToggle to toggle(true, true): it bypasses the
// geo gate and starts sharing. So "onIgnore was called" means "the widget began
// proxying", which is what makes each of these assertions about consent.
const renderModal = (settings: Partial<Settings>, isCensored: boolean) => {
	const onIgnore = jest.fn()
	render(
		<AppContextProvider value={{
			width: 0,
			setWidth: () => {},
			settings: {...defaultSettings, ...settings},
			wasmInterface: undefined as any,
		}}>
			<Modal isCensored={isCensored} onIgnore={onIgnore} />
		</AppContextProvider>
	)
	return onIgnore
}

describe('censored-region modal', () => {
	// The default embed: banner layout, collapsed. The modal cannot render in that
	// layout, and until the isCensored guard the effect fired for every visitor on
	// mount -- so the widget was proxying with nobody having opted in.
	test('does not start sharing on mount for a visitor who is not censored (collapsed banner)', () => {
		const onIgnore = renderModal({layout: Layouts.BANNER, collapse: true}, false)
		expect(onIgnore).not.toHaveBeenCalled()
	})

	test('does not start sharing on mount for a visitor who is not censored (collapsed floating)', () => {
		const onIgnore = renderModal({layout: Layouts.FLOATING, collapse: true}, false)
		expect(onIgnore).not.toHaveBeenCalled()
	})

	// The behaviour the effect exists for: a censored visitor whose layout cannot
	// show the warning would otherwise be stuck, so proceed for them.
	test('still auto-proceeds for a censored visitor whose layout cannot show the modal', () => {
		const onIgnore = renderModal({layout: Layouts.BANNER, collapse: true}, true)
		expect(onIgnore).toHaveBeenCalledTimes(1)
	})

	// When the modal can render, the visitor gets the warning and decides for
	// themselves; nothing should proceed on their behalf.
	test('leaves the decision to a censored visitor when the modal can render (panel)', () => {
		const onIgnore = renderModal({layout: Layouts.PANEL, collapse: true}, true)
		expect(onIgnore).not.toHaveBeenCalled()
	})
})
