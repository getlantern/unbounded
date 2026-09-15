import React, {useCallback, useContext, useEffect, useRef, useState} from 'react'
import {Chunk, servedConnectionsEmitter, lifetimeChunksEmitter, lifetimeConnectionsEmitter, sharingEmitter} from '../../../utils/wasmInterface'
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
}

let reportedConnections = 0

const Storage = ({settings}: {settings: Settings}) => {
	const {target} = settings
	const site = window.location.hostname
	const {wasmInterface} = useContext(AppContext)
	const synced = useRef({[StorageKeys.LIFETIME_CONNECTIONS]: false, [StorageKeys.LIFETIME_CHUNKS]: false})
	const [ready, setReady] = useState(false)
	const loaded = useRef(false)
	const servedConnections = useEmitterState(servedConnectionsEmitter)
	const iframe = useRef<HTMLIFrameElement>(null)
	const lifetimeConnections = useEmitterState(lifetimeConnectionsEmitter)
	const lifetimeChunks = useEmitterState(lifetimeChunksEmitter)
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
					lifetimeConnectionsEmitter.update(lifetimeConnectionsEmitter.state + value)
				}
				// handle lifetime chunks messages from the iframe local storage
				if (keys.includes(StorageKeys.LIFETIME_CHUNKS)) {
					let value = []
					try {
						value = JSON.parse(message.data[StorageKeys.LIFETIME_CHUNKS]) || []
					} catch (e) {
						console.error(e)
					}
					value.forEach((chunk: Chunk) => wasmInterface && wasmInterface.handleChunk({detail: chunk}))
					synced.current[StorageKeys.LIFETIME_CHUNKS] = true
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
		if (!ready || !iframe.current) return;
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.EVENT,
			[SIGNATURE]: true,
			data: {
				eventName: 'sharing',
				eventProperties: {on: sharing}
			}
		}, '*')
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