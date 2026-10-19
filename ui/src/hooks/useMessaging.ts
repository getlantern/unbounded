import {useEffect} from 'react'
import {MessageTypes, SIGNATURE, Targets} from '../constants'
import {
	averageThroughputEmitter,
	connectionsEmitter,
	countryStatsEmitter,
	downloadThroughputEmitter,
	lifetimeChunksEmitter,
	lifetimeConnectionsEmitter,
	lifetimeUploadBytesEmitter,
	readyEmitter,
	sessionBytesEmitter,
	sharingEmitter,
	uploadThroughputEmitter,
} from '../utils/wasmInterface'
import {supportCountryEmitter} from '../utils/supportCountry'
import {messageCheck} from '../utils/messages'

const emitterMap = {
	sharingEmitter,
	connectionsEmitter,
	lifetimeConnectionsEmitter,
	averageThroughputEmitter,
	lifetimeChunksEmitter,
	readyEmitter,
	lifetimeUploadBytesEmitter,
	sessionBytesEmitter,
	uploadThroughputEmitter,
	downloadThroughputEmitter,
	countryStatsEmitter,
	supportCountryEmitter
}

type EmitterKey = keyof typeof emitterMap

let callbacksMap = {} as any

const useMessaging = (target: Targets) => {

	useEffect(() => {
		const handler = (value: any, emitter: EmitterKey) => window.parent.postMessage({
			type: MessageTypes.STATE_UPDATE,
			[SIGNATURE]: true,
			data: {
				emitter,
				value
			}
		}, '*')

		const onOffscreenMessage = (event: MessageEvent) => {
			const message = event.data
			if (!messageCheck(message)) return
			if (message.type !== MessageTypes.HYDRATE_STATE) return
			Object.keys(emitterMap).forEach(emitter => {
				handler(emitterMap[emitter as EmitterKey].state, emitter as EmitterKey)
			})
		}

		const onPopupMessage = (event: MessageEvent) => {
			const message = event.data
			if (!messageCheck(message)) return
			if (message.type !== MessageTypes.STATE_UPDATE) return
			const emitters = Object.keys(emitterMap)
			if (emitters.includes(message.data.emitter)) {
				const emitter = emitterMap[message.data.emitter as EmitterKey]
				// @ts-ignore
				emitter.update(message.data.value)
			}
		}

		if (target === Targets.EXTENSION_POPUP) {
			window.addEventListener('message', onPopupMessage)
			window.parent.postMessage({
				type: MessageTypes.HYDRATE_STATE,
				[SIGNATURE]: true,
				data: {}
			}, '*')
		}
		else if (target === Targets.EXTENSION_OFFSCREEN) {
			Object.keys(emitterMap).forEach(key => {
				const emitter = emitterMap[key as EmitterKey]
				const callback = (value: any) => handler(value, key as EmitterKey)
				callbacksMap[key] = callback
				emitter.on(callback)
			})
			window.addEventListener('message', onOffscreenMessage)
		}
		return () => {
			if (target === Targets.EXTENSION_POPUP) window.removeEventListener('message', onPopupMessage)
			else if (target === Targets.EXTENSION_OFFSCREEN) {
				window.removeEventListener('message', onOffscreenMessage)
				Object.keys(callbacksMap).forEach(key => {
					const emitter = emitterMap[key as EmitterKey]
					emitter.off(callbacksMap[key])
				})
			}
		}
	}, [target])
}

export default useMessaging