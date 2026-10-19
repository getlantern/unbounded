import {StateEmitter} from '../hooks/useStateEmitter'
import {censoredCountryCodes} from './countries'

// The country a volunteer prefers to help, as an ISO code from censoredCountryCodes, or ANYWHERE
// for no preference. It's a preference only: the network routes a volunteer wherever they're
// needed whenever nobody from the chosen country is waiting.
export const ANYWHERE = ''

const STORAGE_KEY = 'unbounded.supportCountry'

export const supportCountryEmitter = new StateEmitter<string>(ANYWHERE)

// Embed attributes and storage are both untrusted input; anything outside the censored list
// collapses to ANYWHERE rather than being sent onward.
export const sanitizeSupportCountry = (value: unknown): string => {
	if (typeof value !== 'string') return ANYWHERE
	const code = value.trim().toUpperCase()
	return censoredCountryCodes.includes(code) ? code : ANYWHERE
}

// The viewer's latest choice in this page view. It wins over storage, which may be unavailable
// (private mode, blocked third-party storage) or reject writes and so hold an older choice.
let pageChoice: string | null = null

// null means this viewer has never chosen, which is different from choosing ANYWHERE: only the
// former defers to the site owner's preset.
const readStoredSupportCountry = (): string | null => {
	if (pageChoice !== null) return pageChoice
	try {
		const stored = window.localStorage.getItem(STORAGE_KEY)
		return stored === null ? null : sanitizeSupportCountry(stored)
	} catch {
		return null
	}
}

export const storeSupportCountry = (country: string) => {
	pageChoice = sanitizeSupportCountry(country)
	try {
		window.localStorage.setItem(STORAGE_KEY, pageChoice)
	} catch {
		// see pageChoice
	}
}

export const forgetSupportCountryChoice = () => {
	pageChoice = null
}

// The viewer's own choice wins; otherwise the embed's data-support-country preset applies.
export const resolveSupportCountry = (preset: unknown): string => {
	const stored = readStoredSupportCountry()
	return stored !== null ? stored : sanitizeSupportCountry(preset)
}

// Seeds the shared emitter from one embed's preset. Every embed on a page shares the emitter, so
// an embed with no preset must not erase another embed's; it only resets the emitter when its own
// preset was just cleared (the editor does this).
export const applySupportCountryPreset = (preset: unknown, previousPreset: unknown = ANYWHERE) => {
	const country = resolveSupportCountry(preset)
	if (country === ANYWHERE && sanitizeSupportCountry(previousPreset) === ANYWHERE) return
	supportCountryEmitter.update(country)
}
