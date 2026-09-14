import React, {useCallback, useContext, useEffect, useRef} from 'react'
import {Chunk, lifetimeChunksEmitter, lifetimeConnectionsEmitter, sharingEmitter} from '../../../utils/wasmInterface'
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

const Storage = ({settings}: {settings: Settings}) => {
	const {target, leaderboard} = settings
	const site = window.location.hostname
	const {wasmInterface} = useContext(AppContext)
	const synced = useRef({[StorageKeys.LIFETIME_CONNECTIONS]: false, [StorageKeys.LIFETIME_CHUNKS]: false})
	const reportedConnections = useRef<number | null>(null)
	const iframe = useRef<HTMLIFrameElement>(null)
	const lifetimeConnections = useEmitterState(lifetimeConnectionsEmitter)
	const lifetimeChunks = useEmitterState(lifetimeChunksEmitter)
	const sharing = useEmitterState(sharingEmitter)

	const onMessage = useCallback((event: MessageEvent) => {
		const message = event.data
		if (!messageCheck(message)) return
		switch (message.type) {
			case MessageTypes.STORAGE_GET:
				const keys = Object.keys(message.data)
				// handle lifetime connections messages from the iframe local storage
				if (keys.includes(StorageKeys.LIFETIME_CONNECTIONS)) {
					const value = parseInt(message.data[StorageKeys.LIFETIME_CONNECTIONS]) || 0
					lifetimeConnectionsEmitter.update(lifetimeConnectionsEmitter.state + value)
					synced.current[StorageKeys.LIFETIME_CONNECTIONS] = true
					reportedConnections.current = lifetimeConnectionsEmitter.state
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
		const iframeRef = iframe.current
		window.addEventListener('message', onMessage)
		iframeRef.addEventListener('load', onLoad)

		return () => {
			if (iframeRef) iframeRef.removeEventListener('load', onLoad)
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
		// this timeout is dumb but needed to make sure the iframe plausible js is loaded
		// before we send the event. Ideally we should use a callback from the iframe
		// leaving like this for now because this may be refactored in the future to sandbox
		// the entire widget in the same iframe
		setTimeout(() => {
			if (!iframe.current) return;
			iframe.current.contentWindow?.postMessage({
				type: MessageTypes.EVENT,
				[SIGNATURE]: true,
				data: {
					eventName: 'load',
					eventProperties: {target, site, leaderboard}
				}
			}, '*')
		}, 1000)
	}, [target, site, leaderboard, iframe])

	// one 'helped' event per new consumer connection, attributed to the embedding site.
	// the leaderboard at unbounded.lantern.io ranks sites by these events.
	useEffect(() => {
		if (!iframe.current || reportedConnections.current === null) return
		const delta = lifetimeConnections - reportedConnections.current
		reportedConnections.current = lifetimeConnections
		for (let i = 0; i < delta; i++) {
			iframe.current.contentWindow?.postMessage({
				type: MessageTypes.EVENT,
				[SIGNATURE]: true,
				data: {
					eventName: 'helped',
					eventProperties: {site, leaderboard}
				}
			}, '*')
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [lifetimeConnections])

	useEffect(() => {
		if (!iframe.current) return;
		iframe.current.contentWindow?.postMessage({
			type: MessageTypes.EVENT,
			[SIGNATURE]: true,
			data: {
				eventName: 'sharing',
				eventProperties: {on: sharing}
			}
		}, '*')
	}, [sharing, iframe])

	return (
		<iframe
			src={process.env.REACT_APP_STORAGE_URL}
			style={{display: 'none'}}
			title={`${SIGNATURE} iframe`}
			ref={iframe}
		/>
	)
}

export default Storage