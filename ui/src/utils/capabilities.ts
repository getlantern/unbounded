// Browser features the widget depends on but cannot assume. iOS Lockdown Mode
// disables both WebGL and WebAssembly in every browser on the device, and WebGL
// can also be refused by GPU blocklists, per-page context limits or privacy
// extensions.

let webGL: boolean | undefined

// Probes once and caches: every probe creates a real GL context, and browsers
// cap how many a page may hold. The probe context is released immediately so it
// does not count against the globe's own.
export const hasWebGL = (): boolean => {
	if (webGL !== undefined) return webGL
	try {
		const canvas = document.createElement('canvas')
		// three.js tries webgl2 first and falls back to webgl, so either will do
		const gl = (canvas.getContext('webgl2') || canvas.getContext('webgl')) as WebGLRenderingContext | null
		gl?.getExtension('WEBGL_lose_context')?.loseContext()
		webGL = !!gl
	} catch {
		webGL = false
	}
	return webGL
}

export const hasWebAssembly = (): boolean =>
	typeof WebAssembly === 'object' && typeof WebAssembly.instantiate === 'function'
