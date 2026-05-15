import {useCallback, useContext, useEffect, useRef, useState} from 'react'
import {Connection, connectionsEmitter, sharingEmitter} from '../utils/wasmInterface'
import {useEmitterState} from './useStateEmitter'
import {countries} from '../utils/countries'
import {pushNotification} from '../components/molecules/notification'
import {AppContext} from '../context'
import {Targets} from '../constants'
import {useTranslation} from 'react-i18next'

type ISO = keyof typeof countries

export interface Arch {
	startLat: number
	startLng: number
	endLat: number
	endLng: number
	country: string
	iso: string
	workerIdx: number
	count: number
	altitude: number
}

export interface Point {
	lng: number
	lat: number
	origin?: boolean
	id: number
}

export interface GeoLookup {
	iso: string
	workerIdx: number
}

const CENSORED_ISO_FALLBACK = 'IR'
const UNCENSORED_ISO_FALLBACK = 'US'
const ARCH_ALTITUDE_GAP = 0.05
const ARCH_ALTITUDE_MIN = 0.3

// this function is a fallback if geo lookup fails. It uses the navigator's language to determine the country
const getCountryFromNavigator = (): ISO => {
	const lang = navigator.language
	console.warn(
		`Uncensored geo lookup failed, using fallback. Navigator language: ${lang}, ISO: ${lang.split('-')?.[1]}, fallback: ${UNCENSORED_ISO_FALLBACK}`
	)
	return lang.split('-')?.[1] as ISO || UNCENSORED_ISO_FALLBACK
}

// a null ip results in a self lookup
export const geoLookup = async (ip: string | null): Promise<ISO> => {
	const isSelf = ip === null
	try {
		const res = await fetch(`${process.env.REACT_APP_GEO_LOOKUP_URL}/${isSelf ? '' : ip}`)
		const data = await res.json()
		return data.Country.IsoCode
	} catch (e) {
		if (!isSelf) console.warn(`Censored geo lookup failed for ${ip}, using fallback. Fallback: ${CENSORED_ISO_FALLBACK}`)
		return isSelf ? getCountryFromNavigator() : CENSORED_ISO_FALLBACK
	}
}

const geoLookupAll = async (connections: Connection[]): Promise<GeoLookup[]> => {
	const res = await Promise.all(connections.map(connection => geoLookup(connection.addr)))
	return res.flat().map((iso, index) => ({iso, workerIdx: connections[index].workerIdx}))
}

const createArcs = (geos: GeoLookup[], isoCountMap: { [key: string]: number }, userIso: ISO) => {
	const newArcs: Arch[] = []
	geos.forEach((geo, index) => {
		const {workerIdx} = geo
		const iso = geo.iso as keyof typeof countries
		const country = countries[iso] || countries[CENSORED_ISO_FALLBACK]
		const userCountry = countries[userIso] || countries[UNCENSORED_ISO_FALLBACK]
		const count = isoCountMap[iso] || geos.filter(g => g.iso === iso).length // use count from map if it exists
		// set altitude based on count minus the number of geos with the same iso that come after this one
		const altitudeFactor = count - geos.filter((g, i) => g.iso === iso && i > index).length
		newArcs.push({
			startLng: userCountry.longitude,
			startLat: userCountry.latitude,
			endLng: country.longitude,
			endLat: country.latitude,
			country: country.name,
			iso: country.alpha2code,
			workerIdx: workerIdx,
			altitude: ARCH_ALTITUDE_MIN + (altitudeFactor * ARCH_ALTITUDE_GAP),
			count: count
		})
	})

	return newArcs
}

const decrementArcs = (arcs: Arch[], remove: Connection[]) => {
	const rmIds = remove.map(r => r.workerIdx)
	const arcsToRemove = arcs.filter(arc => {
		return rmIds.includes(arc.workerIdx)
	})
	for (const rmArch of arcsToRemove) {
		// remove arcs in place
		const index = arcs.findIndex(arc => arc.workerIdx === rmArch.workerIdx)
		arcs.splice(index, 1)
		// decrement count
		const iso = rmArch.iso
		for (const arc of arcs) {
			// decrement count for all arcs with the same iso
			if (arc.iso === iso) arc.count--
		}
	}
	// reset altitudes
	const isoCountMap: { [key: string]: number } = {};

	for (const arc of arcs) {
		isoCountMap[arc.iso] = (isoCountMap[arc.iso] ?? 0) + 1;
		arc.altitude = ARCH_ALTITUDE_MIN + (isoCountMap[arc.iso] * ARCH_ALTITUDE_GAP);
	}
}

const incrementArcs = (arcs: Arch[], geos: GeoLookup[]) => {
	for (const arc of arcs) {
		const geo = geos.filter(g => g.iso === arc.iso)
		if (geo.length > 0) {
			arc.count += geo.length
		}
	}
}

export const useGeo = () => {
	const {t} = useTranslation()
	const [arcs, setArcs] = useState<Arch[]>([])
	const [points, setPoints] = useState<Point[]>([])
	const country = useRef<ISO>()
	const connections = useEmitterState(connectionsEmitter)
	// const connections = useQueueState(rawConnections)
	const active = connections.some(c => c.state === 1)
	const sharing = useEmitterState(sharingEmitter)
	const startTs = useRef(performance.now())
	const {target} = useContext(AppContext).settings

	// Serialize concurrent updateArcs invocations so the async
	// geoLookupAll between mutations can't interleave between two
	// callers and clobber each other's view of `arcs`. Without this,
	// fast-arriving connections (3 within ~200 ms) raced and produced
	// an inconsistent arcs array that three.js then tried to render,
	// throwing "Cannot read properties of undefined (reading 'x')" 60×
	// per second until React's error boundary blanked the canvas.
	const updateLock = useRef<Promise<void>>(Promise.resolve())

	const updateArcs = useCallback((connections: Connection[]) => {
		/***
			The webgl lib mutates the arcs arr in place. These mutations must be retained so that existing arcs do not
		  re-animate on state changes. I.e. we can't simply return a new map here. Arcs must be removed/added using the
		  current arc state.

		  "Basically, internally the framework performs an equality comparison on the array items that were previously
		  included, and leaves them unaffected as there were already WebGL objects generated. This is done for performance
		  reasons between updates actually, because re-generating many objects when potentially only a subset is new would
		  be wasteful of resources. The added benefit is that their current rendering state (as in its animated position)
		  is also not reset in the process." - https://github.com/vasturiano
		 ***/
		const next = updateLock.current.then(async () => {
			if (!country.current) country.current = await geoLookup(null) // lookup user country first time

			const removedConnections = connections.filter(c => c.state === -1)
			const addedConnections = connections.filter(c => c.state === 1)
			const geos = await geoLookupAll(addedConnections)

			setArcs(prev => {
				// Operate on a shallow copy so the React state's underlying
				// array is never partially mutated mid-render. Inner arc
				// object identities are preserved — react-globe.gl uses
				// reference equality to skip WebGL re-creation for arcs that
				// didn't change.
				const drafted = [...prev]
				decrementArcs(drafted, removedConnections)
				incrementArcs(drafted, geos)

				const isoCountMap = {} as { [key: string]: number }
				drafted.forEach(arc => {
					if (!isoCountMap[arc.iso]) isoCountMap[arc.iso] = arc.count
				})

				const newArcs = createArcs(geos, isoCountMap, country.current!)
				return [...drafted, ...newArcs]
			})

			// dispatch push notifications. Use the freshly resolved geo →
			// country map directly rather than reaching back into the post-
			// update arcs state, which is racy under serialized updates.
			geos.forEach(geo => {
				const iso = geo.iso as keyof typeof countries
				const countryName = (countries[iso] || countries[CENSORED_ISO_FALLBACK]).name
				if (!countryName) return
				if (target === Targets.EXTENSION_POPUP && startTs.current + 1000 > performance.now()) return // don't show notifications on initial load because of initial sync w/ bg script
				pushNotification({
					id: geo.workerIdx,
					// text: `Helping a new person in ${countryName.split(',')[0]}`,
					text: `${t('helping', {country: countryName})}`,
					autoHide: true,
					heart: true
				})
			})
		}).catch(err => {
			// Swallow + log so a single update's failure doesn't poison the
			// chain — every subsequent updateArcs would be skipped otherwise
			// because the lock would stay rejected.
			console.error('updateArcs failed', err)
		})
		updateLock.current = next
		return next
	}, [target, t])

	useEffect(() => {
		// check if connections have changed since the last update (using arcs workerIdx)
		const updatedConnections = connections.filter(con => {
			if (con.state === -1) return arcs.some(arc => arc.workerIdx === con.workerIdx)
			return !arcs.some(arc => arc.workerIdx === con.workerIdx)
		})
		if (!updatedConnections.length) return // only update on changes
		updateArcs(updatedConnections)
	}, [connections, arcs, updateArcs])

	useEffect(() => {
		if (sharing && !active) pushNotification({
			id: -1,
			text: t('waiting'),
			ellipse: true
		})
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [sharing, active])

	useEffect(() => {
		// If no arcs, reset points and exit.
		if (!arcs.length) {
			setPoints([])
			return
		}

		const newPoints = [] as Point[]

		arcs.forEach(arc => {
			const isExistingPoint = points.some(p => arc.endLat === p.lat && arc.endLng === p.lng)
			const isNewPointDuplicate = newPoints.some(p => arc.endLat === p.lat && arc.endLng === p.lng)

			if (!isExistingPoint && !isNewPointDuplicate) {
				newPoints.push({
					lng: arc.endLng,
					lat: arc.endLat,
					id: arc.workerIdx
				})
			}
		})

		// If no origin point exists, add it.
		if (!points.some(p => p.origin)) {
			newPoints.push({
				lng: arcs[0].startLng,
				lat: arcs[0].startLat,
				origin: true,
				id: -1
			})
		}

		const oldPoints = points.filter(p =>
			p.id === -1 || arcs.some(a => a.endLat === p.lat && a.endLng === p.lng)
		)

		// Set points after a short delay.
		setTimeout(() => setPoints([...oldPoints, ...newPoints]), 0)

		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [arcs])

	return {
		arcs: arcs,
		points: points,
		country: country.current
	}
}