// @ts-nocheck @todo add types
/***
	This uses https://github.com/vasturiano/react-globe.gl to bootstrap the webgl globe for prototype/iteration.
	Ideally once we've tuned it to our liking - this should be build bespoke using three.js for decreased bundle
	size and better performance.

	Also see the patches dir for changes to the shader program that are not exposed through the react-globe api.
 ***/
import GlobeComponent from 'react-globe.gl'
import {Container} from './styles'
import {useContext, useEffect, useMemo, useRef, useState} from 'react'
import {AppContext} from '../../../context'
import {BREAKPOINT, COLORS, Layouts, Targets, Themes, UV_MAP_PATH_DARK, UV_MAP_PATH_LIGHT} from '../../../constants'
import Shadow from './shadow'
import ToolTip from '../toolTip'
import {useGeo} from '../../../hooks/useGeoFuture'
import {Notification} from '../notification'
import usePageVisibility from '../../../hooks/usePageVisibility'
import * as THREE from 'three'
import { useTranslation } from 'react-i18next';
// import {useEmitterState} from '../../../hooks/useStateEmitter'
// import {sharingEmitter} from '../../../utils/wasmInterface'
// import {countries} from "../../../utils/countries";
// import InteractAnim from './interactAnim'

interface Props {
	target: Targets
}

const calcOffset = (size: number, title: boolean, menu: boolean) => {
	if (size === 250) return -10
	let offset = -10
	if (title) offset += 40
	if (menu) offset -= 40
	return offset
}

// visual tuning for the simple layout: a 240px canvas with the camera pulled in
// (cameraAltitude) renders the sphere itself at ~193px, centered in (and
// overflowing) a 193px layout box. The light sits at 45° between overhead and
// camera-facing so the top highlight stays soft, and the dimmed ambient shades
// the sphere darker toward the bottom.
const SIMPLE_GLOBE = {
	canvasSize: 240,
	boxSize: 193,
	cameraAltitude: 1.85,
	minDistance: 240,
	directionalIntensity: .12,
	ambientDim: .8,
	lightPosition: [0, 350, 350],
}


const createGradientTexture = (blue = true) => {
	const size = 100; // The size of the texture
	const canvas = document.createElement('canvas');
	canvas.width = size;
	canvas.height = size;

	const context = canvas.getContext('2d');
	const gradient = context.createRadialGradient(
		size / 2,
		size / 2,
		0,
		size / 2,
		size / 2,
		size / 2
	);

	gradient.addColorStop(0, blue ? 'rgba(0, 188, 212, 1)' : 'rgba(255, 193, 7, 1)');
	gradient.addColorStop(1, blue ? 'rgba(0, 188, 212, 0)' : 'rgba(255, 193, 7, 0)');
	context.fillStyle = gradient;
	context.fillRect(0, 0, size, size);

	return new THREE.CanvasTexture(canvas);
}

const gradientTextureBlue = createGradientTexture(true);
const gradientTextureYellow = createGradientTexture(false);
const materialBlue = new THREE.MeshLambertMaterial({
	map: gradientTextureBlue,
	blending: THREE.NormalBlending,
	transparent: true,
	name: 'blue'
});
const materialYellow = new THREE.MeshLambertMaterial({
	map: gradientTextureYellow,
	blending: THREE.NormalBlending,
	transparent: true,
	name: 'yellow'
});

const Globe = ({target}: Props) => {
	const {t} = useTranslation()
	// const sharing = useEmitterState(sharingEmitter)
	const {width, settings} = useContext(AppContext)
	const {theme, title, menu} = settings
	const simple = settings.layout === Layouts.SIMPLE
	const size = simple ? SIMPLE_GLOBE.canvasSize : width < BREAKPOINT ? 250 : 400
	const isSetup = useRef(false)
	const [arc, setArc] = useState(null)
	const count = arc ? arc.count : 0
	const globe = useRef()
	const container = useRef()
	const {arcs, points} = useGeo()
	const [altitude, setAltitude] = useState(14)
	const isVisible = usePageVisibility()
	// const lastAnimation = useRef(0)
	// const [interacted, setInteracted] = useState(false)

	const ghostArcs = useMemo(() => {
		if (!arcs) return []
		return arcs.map(arc => {
			const {endLat, endLng, startLat, startLng, count, country, altitude, iso} = arc
			return ({endLat, endLng, startLat, startLng, count, country, altitude, iso, ghost: true})
		})
	}, [arcs])

	// useEffect(() => { // @todo re-enable this when we determine the better ux see https://github.com/getlantern/engineering/issues/216
	// 	if (!globe.current) return
	// 	const now = Date.now()
	// 	if (sharing && country && (now - lastAnimation.current) > 5000) {
	// 		const userLoc = countries[country]
	// 		globe.current.pointOfView({
	// 			lat: 20, // equator
	// 			lng: points.length === 0 ? userLoc.longitude : points[points.length - 1].lng,
	// 			altitude: 2.5
	// 		}, 1000)
	// 		lastAnimation.current = now
	// 	}
	// 	// alert(points[points.length - 1])
	// }, [sharing, country, points])

	useEffect(() => {
		if (!globe.current) return
		const controls = globe.current.controls()
		controls.autoRotate = !arc
	}, [arc])

	const setUp = () => {
		if (isSetup.current || !globe.current) return
		isSetup.current = true
		const controls = globe.current.controls()
		const camera = globe.current.camera()
		const scene = globe.current.scene()
		controls.enableZoom = target !== Targets.EXTENSION_POPUP && !simple // disable zoom on extension popup and simple layout
		controls.autoRotate = true
		controls.maxDistance = 1500
		controls.minDistance = simple ? SIMPLE_GLOBE.minDistance : 300
		controls.autoRotateSpeed = 1.5
		const ambientLight = scene.children.find(obj3d => obj3d.type === 'AmbientLight')
		if (simple && ambientLight) ambientLight.intensity *= SIMPLE_GLOBE.ambientDim
		const directionalLight = scene.children.find(obj3d => obj3d.type === 'DirectionalLight')
		if (directionalLight) {
			directionalLight.intensity = simple ? SIMPLE_GLOBE.directionalIntensity : .25
			const clonedLight = directionalLight.clone()
			if (simple) clonedLight.position.set(...SIMPLE_GLOBE.lightPosition)
			else clonedLight.position.set(0, 500, 0)
			camera.add(clonedLight)
			scene.add(camera)
			scene.remove(directionalLight)
		}
		if (simple) globe.current.pointOfView({altitude: SIMPLE_GLOBE.cameraAltitude}, 0)
	}

	const setRotateSpeed = (speed) => {
		if (!globe.current) return
		const controls = globe.current.controls()
		controls.autoRotateSpeed = speed
	}

	// useEffect(() => {
	// 	const animate = () => {
	// 		requestAnimationFrame(animate)
	// 		if (!globe.current) return
	// 		const scene = globe.current?.scene()
	// 		if (!scene) return
	// 		// scene.children.forEach(obj3d => console.log(obj3d.type))
	// 		const fromKapsule = scene.children.find(obj3d => obj3d.type === 'Group')
	// 		if (!fromKapsule) return
	// 		const lineSegmentGroups = fromKapsule.children[0].children
	// 		lineSegmentGroups.forEach(group => {
	// 			const lineSegments = group.children.find(obj3d => obj3d.type === 'LineSegments')
	// 			if (lineSegments) {
	// 				// @todo
	// 			}
	// 		})
	//
	// 		const pointMeshes = fromKapsule.children[0].children[1].children
	//
	// 		pointMeshes.forEach(mesh => {
	// 			if (mesh.material.color.r === 0 || mesh.material.name === 'blue') mesh.material = materialBlue
	// 			else mesh.material = materialYellow
	// 		})
	// 	}
	// 	animate()
	// }, [])

	useEffect(() => {
		if (!isVisible) return // don't update if not visible

		// @todo this timeout is hacky. basically there's a race with the scene updating. considering moving this to a mutation observer on the scene
		const timeout = setTimeout(() => {
			if (!globe.current) return
			const scene = globe.current?.scene()
			if (!scene) return
			const fromKapsule = scene.children.find(obj3d => obj3d.type === 'Group')
			if (!fromKapsule) return
			const pointMeshes = fromKapsule.children[0].children[1].children
			const uuids = pointMeshes.map(mesh => mesh.uuid)
			const clonedMeshes = scene.children.filter(obj3d => obj3d.name === 'clone')
			const clonedUuids = clonedMeshes.map(mesh => mesh.uuid)
			// clone pointMeshes with custom material and add to scene
			pointMeshes.forEach(mesh => {
				const uuid = mesh.uuid
				if (clonedUuids.map(id => id.replace('-clone', '')).includes(uuid)) return
				const clonedMesh = mesh.clone()
				if (mesh.material.color.r === 0 || mesh.material.name === 'blue') clonedMesh.material = materialBlue
				else clonedMesh.material = materialYellow
				clonedMesh.uuid = uuid + '-clone'
				clonedMesh.name = 'clone'
				scene.add(clonedMesh)
			})

			// remove clonedMeshes that are no longer in pointMeshes
			clonedMeshes.forEach(mesh => {
				const uuid = mesh.uuid
				if (!uuids.includes(uuid.replace('-clone', ''))) scene.remove(mesh)
			});
		}, 50)

		return () => {
			if (timeout) clearTimeout(timeout)
		}
	}, [points, isVisible])

	// const colorInterpolatorBlue = t => `rgba(0, 188, 212,${Math.sqrt(1-t)})`;
	// const colorInterpolatorYellow = t => `rgba(255, 193, 7,${Math.sqrt(1-t)})`;

	return (
		<Container
			ref={container}
			offset={simple ? (SIMPLE_GLOBE.boxSize - size) / 2 : calcOffset(size, title, menu)}
			size={size}
			active={!!arc}
			$simple={simple}
			style={simple ? {
				minHeight: SIMPLE_GLOBE.boxSize,
				maxHeight: SIMPLE_GLOBE.boxSize,
			} : {
				minHeight: 250, // sm breakpoint
				maxHeight: (!menu && title) ? 424 : (!menu || title) ? 400 : 350, // lg breakpoint
			}}
			// onMouseDown={() => setInteracted(true)}
			// onTouchStart={() => setInteracted(true)}
			onMouseEnter={() => setRotateSpeed(1)}
			onMouseLeave={() => setRotateSpeed(1.5)}
		>
			{
				!simple && (
					<Shadow
						scale={1/(altitude/2)} // altitude is 2-14
					/>
				)
			}
			<GlobeComponent
				ref={globe}
				onGlobeReady={setUp}
				width={size}
				height={size}
				enablePointerInteraction={true}
				waitForGlobeReady={true}
				showAtmosphere={true}
				atmosphereColor={theme === Themes.LIGHT ? COLORS.altBrand : COLORS.brand}
				atmosphereAltitude={theme === Themes.LIGHT ? 0.2 : 0.25}
				backgroundColor={'rgba(0,0,0,0)'}
				backgroundImageUrl={null}
				globeImageUrl={theme === Themes.DARK ? UV_MAP_PATH_DARK : UV_MAP_PATH_LIGHT}
				arcsData={[...arcs, ...ghostArcs]}
				arcColor={arc => arc.ghost ? 'rgba(255, 255, 255, 0)' : ['rgba(0, 188, 212, 0.75)', 'rgba(255, 193, 7, 0.75)']}
				arcDashLength={1}
				arcDashGap={0.5}
				arcDashInitialGap={1}
				arcDashAnimateTime={arc => arc.ghost ? 0 : 1000}
				arcsTransitionDuration={0}
				arcStroke={arc => arc.ghost ? 10 : 2}
				arcAltitudeAutoScale={arc => arc.altitude}
				onArcHover={setArc}
				pointsData={points}
				pointColor={p => p.origin ? 'rgba(0, 188, 212, 0.05)' : 'rgba(255, 193, 7, 0.05)'}
				pointRadius={4}
				pointAltitude={0}
				pointsTransitionDuration={500}
				onZoom={zoom => {
					let smooth = Math.round(zoom.altitude * 10) / 10
					if (smooth !== altitude) setAltitude(smooth)
				}}
				// ringsData={rings}
				// ringColor={p => p.origin ? colorInterpolatorBlue : colorInterpolatorYellow}
				// // ringMaxRadius={4}
				// // ringRepeatPeriod={1000}
				// ringPropagationSpeed={1}
			/>
			<ToolTip
				// text={!!arc && `${count} ${count === 1 ? 'person' : 'people'} from ${arc.country.split(',')[0]}`}
				text={!!arc && (() => {
					const country = arc.country.split(',')[0]
					const plural = count > 1
					if (plural) return t('peers', { country, count })
					return t('peer', { country, count })
				})}
				show={!!arc}
				container={container}
			/>
			<Notification />
			{/*@todo re-enable this after we determine better UX see https://github.com/getlantern/engineering/issues/215*/}
			{/*{*/}
			{/*	isSetup && !interacted && (*/}
			{/*		<InteractAnim />*/}
			{/*	)*/}
			{/*}*/}
		</Container>
	)
}

export default Globe