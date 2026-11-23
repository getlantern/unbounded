import {countries} from './countries'

type ISO = keyof typeof countries

const CENSORED_ISO_FALLBACK = 'IR'
const UNCENSORED_ISO_FALLBACK = 'US'
const PEER_CACHE_MAX = 256

// this function is a fallback if geo lookup fails. It uses the navigator's language to determine the country
const getCountryFromNavigator = (): ISO => {
	const lang = navigator.language
	console.warn(
		`Uncensored geo lookup failed, using fallback. Navigator language: ${lang}, ISO: ${lang.split('-')?.[1]}, fallback: ${UNCENSORED_ISO_FALLBACK}`
	)
	return lang.split('-')?.[1] as ISO || UNCENSORED_ISO_FALLBACK
}

// the globe and the stats both look up every peer, so share one request per address
const peerCache = new Map<string, Promise<ISO | null>>()

// resolves to null when the address is missing or the lookup fails, so callers can tell
// "unknown" apart from a real country
export const lookupPeerCountry = (ip: string): Promise<ISO | null> => {
	if (!ip) return Promise.resolve(null)
	const cached = peerCache.get(ip)
	if (cached) return cached
	const lookup = fetch(`${process.env.REACT_APP_GEO_LOOKUP_URL}/${ip}`)
		.then(res => res.json())
		.then(data => (data.Country.IsoCode as ISO) || null)
		.catch(() => {
			peerCache.delete(ip)
			return null
		})
	if (peerCache.size >= PEER_CACHE_MAX) peerCache.delete(peerCache.keys().next().value!)
	peerCache.set(ip, lookup)
	return lookup
}

// a null ip results in a self lookup
export const geoLookup = async (ip: string | null): Promise<ISO> => {
	if (ip !== null) {
		const iso = await lookupPeerCountry(ip)
		if (iso) return iso
		console.warn(`Censored geo lookup failed for ${ip}, using fallback. Fallback: ${CENSORED_ISO_FALLBACK}`)
		return CENSORED_ISO_FALLBACK
	}
	try {
		const res = await fetch(`${process.env.REACT_APP_GEO_LOOKUP_URL}/`)
		const data = await res.json()
		return data.Country.IsoCode
	} catch (e) {
		return getCountryFromNavigator()
	}
}
