import {useContext, useState} from 'react'
import {AppContext} from '../context'
import {Targets} from '../constants'
import {useEmitterState} from './useStateEmitter'
import {readyEmitter, sharingEmitter} from '../utils/wasmInterface'
import {geoLookup} from './useGeoFuture'
import {censoredCountryCodes} from '../utils/countries'
import {tutorialOnEmitter} from '../components/atoms/tutorial'

// Shared sharing-switch logic: lazy wasm init on web and the censored-geo gate
// that must pass before sharing can start. Spread the returned switchProps onto
// a Switch atom and wire isCensored/onIgnore to a Modal.
export const useSharingToggle = (onToggle?: (share: boolean) => void) => {
	const ready = useEmitterState(readyEmitter)
	const sharing = useEmitterState(sharingEmitter)
	const {wasmInterface, settings} = useContext(AppContext)
	const {mock, target} = settings
	// on web, we don't need to initialize wasm until user starts sharing
	const needsInit = target === Targets.WEB && !wasmInterface?.instance
	const [loading, setLoading] = useState(false)
	const [isCensored, setIsCensored] = useState(false)
	const [ignoreCensored, setIgnoreCensored] = useState(false)
	const [cachedGeo, setCachedGeo] = useState<string | null>(null)

	const init = async (): Promise<boolean> => {
		if (!wasmInterface) return false
		setLoading(true)
		console.log(`initializing p2p ${mock ? '"wasm"' : 'wasm'}`)
		try {
			const instance = await wasmInterface.initialize({mock, target})
			if (!instance) {
				console.warn('wasm failed to initialize')
				return false
			}
			console.log(`p2p ${mock ? '"wasm"' : 'wasm'} initialized!`)
			return true
		} finally {
			setLoading(false)
		}
	}

	const isCensoredGeo = async () => {
		const geo = cachedGeo || await geoLookup(null)
		setCachedGeo(geo)
		return censoredCountryCodes.includes(geo)
	}

	const toggle = async (share: boolean, ignoreCensored = false) => {
		if (share && !ignoreCensored) {
			const isCensored = await isCensoredGeo()
			if (isCensored) {
				setIsCensored(true)
				return
			}
		}
		if (!wasmInterface) return
		if (share) {
			// lazy init is only needed to start sharing; bail if it fails (or is
			// already in flight) rather than calling start() on a non-ready client
			if (needsInit && !(await init())) return
			wasmInterface.start()
			tutorialOnEmitter.update(false)
		}
		if (!share) wasmInterface.stop()
		if (onToggle) onToggle(share)
	}

	return {
		sharing,
		isCensored,
		onIgnore: () => {
			setIgnoreCensored(true)
			toggle(true, true)
		},
		switchProps: {
			onToggle: (share: boolean) => toggle(share, ignoreCensored),
			checked: sharing,
			disabled: !ready && !needsInit,
			loading
		}
	}
}
