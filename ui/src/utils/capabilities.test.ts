// hasWebGL caches its answer, so each case loads a fresh copy of the module.
const load = (): typeof import('./capabilities') => {
	let mod: typeof import('./capabilities')
	jest.isolateModules(() => { mod = require('./capabilities') })
	return mod!
}

describe('hasWebGL', () => {
	const getContext = HTMLCanvasElement.prototype.getContext
	afterEach(() => { HTMLCanvasElement.prototype.getContext = getContext })

	// What iOS Lockdown Mode does: every getContext('webgl*') returns null.
	test('is false when the browser refuses a WebGL context', () => {
		HTMLCanvasElement.prototype.getContext = jest.fn(() => null) as any
		expect(load().hasWebGL()).toBe(false)
	})

	test('is false when requesting a context throws', () => {
		HTMLCanvasElement.prototype.getContext = jest.fn(() => { throw new Error('blocked') }) as any
		expect(load().hasWebGL()).toBe(false)
	})

	// The probe must hand its context back: browsers cap live contexts per page,
	// and a leaked probe would count against the globe's own.
	test('is true for a WebGL context, and releases the probe context', () => {
		const loseContext = jest.fn()
		HTMLCanvasElement.prototype.getContext = jest.fn(() => ({getExtension: () => ({loseContext})})) as any
		expect(load().hasWebGL()).toBe(true)
		expect(loseContext).toHaveBeenCalledTimes(1)
	})

	test('probes once and caches the answer', () => {
		const probe = jest.fn(() => null)
		HTMLCanvasElement.prototype.getContext = probe as any
		const {hasWebGL} = load()
		hasWebGL(); hasWebGL(); hasWebGL()
		// webgl2 then webgl on the first call, nothing after
		expect(probe).toHaveBeenCalledTimes(2)
	})
})

describe('hasWebAssembly', () => {
	const original = Object.getOwnPropertyDescriptor(globalThis, 'WebAssembly')
	afterEach(() => { if (original) Object.defineProperty(globalThis, 'WebAssembly', original) })

	// Lockdown Mode removes the WebAssembly global entirely.
	test('is false when the WebAssembly global is missing', () => {
		Object.defineProperty(globalThis, 'WebAssembly', {value: undefined, configurable: true, writable: true})
		expect(load().hasWebAssembly()).toBe(false)
	})

	test('is true when WebAssembly can instantiate', () => {
		Object.defineProperty(globalThis, 'WebAssembly', {value: {instantiate: () => {}}, configurable: true, writable: true})
		expect(load().hasWebAssembly()).toBe(true)
	})
})
