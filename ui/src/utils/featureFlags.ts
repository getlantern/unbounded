import {StateEmitter} from '../hooks/useStateEmitter'

// Remote switches for features the widget ships before the backend can honor them. Every flag
// fails closed: an unset URL, a slow or failed request, or a malformed reply all leave it off.

export const FEATURES_TIMEOUT_MS = 3000

export const SUPPORT_COUNTRY_FLAG = 'unbounded_support_country'

// Whether a volunteer may pick a country to support. Until it is on, the picker is hidden and
// no country, whether chosen, stored or preset by the embed, reaches the client.
export const supportCountryEnabledEmitter = new StateEmitter<boolean>(false)

export interface FeatureFlags {
	supportCountry: boolean
}

const OFF: FeatureFlags = {supportCountry: false}

export const fetchFeatureFlags = async (
	url: string | undefined = process.env.REACT_APP_FEATURES_URL,
	timeoutMs: number = FEATURES_TIMEOUT_MS,
): Promise<FeatureFlags> => {
	if (!url) return OFF
	let timer: ReturnType<typeof setTimeout> | undefined
	let controller: AbortController | undefined
	try {
		controller = new AbortController()
	} catch {
		controller = undefined
	}
	// the race covers fetch implementations that ignore the abort signal
	const timeout = new Promise<FeatureFlags>(resolve => {
		timer = setTimeout(() => {
			controller?.abort()
			resolve(OFF)
		}, timeoutMs)
	})
	const request = (async (): Promise<FeatureFlags> => {
		const res = await fetch(url, {signal: controller?.signal, credentials: 'omit'})
		if (!res.ok) return OFF
		const body = await res.json()
		return {supportCountry: body?.features?.[SUPPORT_COUNTRY_FLAG] === true}
	})().catch(() => OFF)
	try {
		return await Promise.race([request, timeout])
	} finally {
		clearTimeout(timer)
	}
}

let loading: Promise<void> | undefined

// Fetches the flags once per page, however many embeds call it. Nothing awaits it: the widget
// renders and loads the wasm with every flag off, and a flag that arrives on turns its feature
// on then.
export const loadFeatureFlags = (): Promise<void> => {
	if (!loading) loading = fetchFeatureFlags().then(flags => {
		supportCountryEnabledEmitter.update(flags.supportCountry)
	})
	return loading
}

export const resetFeatureFlagsForTest = () => {
	loading = undefined
	supportCountryEnabledEmitter.update(false)
}
