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

// null means this viewer has never chosen, which is different from choosing ANYWHERE: only the
// former defers to the site owner's preset. Storage can be unavailable (private mode, blocked
// third-party storage), in which case the viewer's choice lasts for the page view only.
const readStoredSupportCountry = (): string | null => {
	try {
		const stored = window.localStorage.getItem(STORAGE_KEY)
		return stored === null ? null : sanitizeSupportCountry(stored)
	} catch {
		return null
	}
}

export const storeSupportCountry = (country: string) => {
	try {
		window.localStorage.setItem(STORAGE_KEY, sanitizeSupportCountry(country))
	} catch {
		// see readStoredSupportCountry
	}
}

// The viewer's own choice wins; otherwise the embed's data-support-country preset applies.
export const resolveSupportCountry = (preset: unknown): string => {
	const stored = readStoredSupportCountry()
	return stored !== null ? stored : sanitizeSupportCountry(preset)
}
