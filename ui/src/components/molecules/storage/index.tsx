import React, {useCallback, useContext, useEffect, useRef, useState} from 'react'
import {
	Chunk,
	CountryStat,
	countryStatsEmitter,
	lifetimeChunksEmitter,
	lifetimeConnectionsEmitter,
	lifetimeUploadBytesEmitter,
	mergeCountryStats,
	servedConnectionsEmitter,
	sharingEmitter
} from '../../../utils/wasmInterface'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import {SIGNATURE, MessageTypes, Settings} from '../../../constants'
import {messageCheck} from '../../../utils/messages'
import {AppContext} from '../../../context'

/***
	Since the widget can be embeded in any website, this iframe is used to
  store data in the local storage of a lantern domain. We use the iframe
  messaging API to sync data between the widget and the iframe. See the storage.html
  file in the public dir for more details.
 ***/
enum StorageKeys {
	LIFETIME_CONNECTIONS = 'lifetimeConnections',
	LIFETIME_CHUNKS = 'lifetimeChunks',
	LIFETIME_UPLOAD_BYTES = 'lifetimeUploadBytes',
	COUNTRY_STATS = 'countryStats',
}

let reportedConnections = 0
// The emitters are shared by every embed on the page, so stored totals are added once per page
// load, not once per embed; each embed still marks its own key synced so it can persist updates.
const hydrated = new Set<StorageKeys>()
const hydrateOnce = (key: StorageKeys, apply: () => void) => {
	if (hydrated.has(key)) return
	hydrated.add(key)
	apply()
}
export const resetStorageHydration = () => hydrated.clear()

const Storage = ({settings}: {settings: Settings}) => {
	const {target} = settings
	const site = window.location.hostname
	const {wasmInterface} = useContext(AppContext)
	const synced = useRef({
		[StorageKeys.LIFETIME_CONNECTIONS]: false,
		[StorageKeys.LIFETIME_CHUNKS]: false,
		[StorageKeys.LIFETIME_UPLOAD_BYTES]: false,
		[StorageKeys.COUNTRY_STATS]: false,
	})
	const [ready, setReady] = useState(false)
	const loaded = useRef(false)
	const reportedSharing = useRef(sharingEmitter.state)
	const servedConnections = useEmitterState(servedConnectionsEmitter)
	const iframe = useRef<HTMLIFrameElement>(null)
	const lifetimeConnections = useEmitterState(lifetimeConnectionsEmitter)
	const lifetimeChunks = useEmitterState(lifetimeChunksEmitter)
	const lifetimeUploadBytes = useEmitterState(lifetimeUploadBytesEmitter)
	const countryStats = useEmitterState(countryStatsEmitter)
	const sharing = useEmitterState(sharingEmitter)

	const onMessage = useCallback((event: MessageEvent) => {
		const message = event.data
		if (event.source !== iframe.current?.contentWindow || !messageCheck(message)) return
		switch (message.type) {
			case MessageTypes.STORAGE_GET:
				const keys = Object.keys(message.data)
				// handle lifetime connections messages from the iframe local storage
				if (keys.includes(StorageKeys.LIFETIME_CONNECTIONS)) {
					const value = parseInt(message.data[StorageKeys.LIFETIME_CONNECTIONS]) || 0
					if (synced.current[StorageKeys.LIFETIME_CONNECTIONS]) return
					synced.current[StorageKeys.LIFETIME_CONNECTIONS] = true
					hydrateOnce(StorageKeys.LIFETIME_CONNECTIONS, () => lifetimeConnectionsEmitter.update(lifetimeConnectionsEmitter.state + value))
				}
				// handle lifetime chunks messages from the iframe local storage
				if (keys.includes(StorageKeys.LIFETIME_CHUNKS)) {
					if (synced.current[StorageKeys.LIFETIME_CHUNKS]) return
					let value = []
					try {
						value = JSON.parse(message.data[StorageKeys.LIFETIME_CHUNKS]) || []
					} catch (e) {
						console.error(e)
					}
					hydrateOnce(StorageKeys.LIFETIME_CHUNKS, () => value.forEach((chunk: Chunk) => wasmInterface && wasmInterface.handleChunk({detail: chunk})))
					synced.current[StorageKeys.LIFETIME_CHUNKS] = true
				}
				if (keys.includes(StorageKeys.LIFETIME_UPLOAD_BYTES)) {
					if (synced.current[StorageKeys.LIFETIME_UPLOAD_BYTES]) return
					const value = parseInt(message.data[StorageKeys.LIFETIME_UPLOAD_BYTES]) || 0
					synced.current[StorageKeys.LIFETIME_UPLOAD_BYTES] = true
					hydrateOnce(StorageKeys.LIFETIME_UPLOAD_BYTES, () => lifetimeUploadBytesEmitter.update(lifetimeUploadBytesEmitter.state + value))
				}
				if (keys.includes(StorageKeys.COUNTRY_STATS)) {
					if (synced.current[StorageKeys.COUNTRY_STATS]) return
					let value: CountryStat[] = []
					try {
						value = JSON.parse(message.data[StorageKeys.COUNTRY_STATS]) || []
					} catch (e) {
						console.error(e)
					}
					synced.current[StorageKeys.COUNTRY_STATS] = true
					hydrateOnce(StorageKeys.COUNTRY_STATS, () => countryStatsEmitter.update(mergeCountryStats(countryStatsEmitter.state, value)))
				}
				break
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [!!wasmInterface])

	const onLoad = useCallback(() => {
		setReady(true)
		iframe.current?.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_GET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_CHUNKS
			}
		}, '*')
		iframe.current?.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_GET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_CONNECTIONS
			}
		}, '*')
		iframe.current?.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_GET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_UPLOAD_BYTES
			}
		}, '*')
		iframe.current?.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_GET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.COUNTRY_STATS
			}
		}, '*')
	}, [iframe])

	useEffect(() => {
		if (!iframe.current) return
		window.addEventListener('message', onMessage)

		return () => {
			window.removeEventListener('message', onMessage)
		}
	}, [onLoad, onMessage, iframe])

	useEffect(() => {
		if (!iframe.current || !synced.current?.[StorageKeys.LIFETIME_CONNECTIONS]) return
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_SET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_CONNECTIONS,
				value: lifetimeConnections
			}
		}, '*')
	}, [lifetimeConnections])

	useEffect(() => {
		if (!iframe.current || !synced.current?.[StorageKeys.LIFETIME_CHUNKS]) return
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_SET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_CHUNKS,
				value: JSON.stringify(lifetimeChunks)
			}
		}, '*')
	}, [lifetimeChunks])

	useEffect(() => {
		if (!iframe.current || !synced.current?.[StorageKeys.LIFETIME_UPLOAD_BYTES]) return
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_SET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.LIFETIME_UPLOAD_BYTES,
				value: lifetimeUploadBytes
			}
		}, '*')
	}, [lifetimeUploadBytes])

	useEffect(() => {
		if (!iframe.current || !synced.current?.[StorageKeys.COUNTRY_STATS]) return
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.STORAGE_SET,
			[SIGNATURE]: true,
			data: {
				key: StorageKeys.COUNTRY_STATS,
				value: JSON.stringify(countryStats)
			}
		}, '*')
	}, [countryStats])

	useEffect(() => {
		if (!ready || !iframe.current?.contentWindow) return
		if (!loaded.current) {
			iframe.current.contentWindow.postMessage({type: MessageTypes.EVENT, [SIGNATURE]: true,
				data: {eventName: 'load', eventProperties: {target, site}}}, '*')
			loaded.current = true
		}
		// All embeds share the live counter and cursor; stored totals never enter it.
		while (reportedConnections < servedConnections) {
			iframe.current.contentWindow.postMessage({type: MessageTypes.EVENT, [SIGNATURE]: true,
				data: {eventName: 'helped', eventProperties: {site}}}, '*')
			reportedConnections++
		}
	}, [ready, servedConnections, target, site])

	useEffect(() => {
		// only report changes; the mount-time value is not a toggle
		if (!ready || !iframe.current?.contentWindow || sharing === reportedSharing.current) return
		iframe.current.contentWindow.postMessage({
			type: MessageTypes.EVENT,
			[SIGNATURE]: true,
			data: {
				eventName: 'sharing',
				eventProperties: {on: sharing}
			}
		}, '*')
		reportedSharing.current = sharing
	}, [ready, sharing, iframe])

	return (
		<iframe
			src={process.env.REACT_APP_STORAGE_URL}
			onLoad={onLoad}
			style={{display: 'none'}}
			title={`${SIGNATURE} iframe`}
			ref={iframe}
		/>
	)
}

export default Storage