import {useEffect, useRef} from 'react'
import {useEmitterState} from './useStateEmitter'
import {applySupportCountryPreset} from '../utils/supportCountry'
import {supportCountryEnabledEmitter} from '../utils/featureFlags'

// Seeds the volunteer's country preference: their own stored choice, else the embed's preset.
// Neither applies until the feature is switched on.
const useSupportCountryPreset = (preset: string) => {
	const enabled = useEmitterState(supportCountryEnabledEmitter)
	const previous = useRef<string>()
	useEffect(() => {
		if (!enabled) return
		applySupportCountryPreset(preset, previous.current)
		previous.current = preset
	}, [preset, enabled])
}

export default useSupportCountryPreset
