import {
	ANYWHERE,
	applySupportCountryPreset,
	forgetSupportCountryChoice,
	resolveSupportCountry,
	sanitizeSupportCountry,
	storeSupportCountry,
	supportCountryEmitter,
} from './supportCountry'

afterEach(() => {
	supportCountryEmitter.update(ANYWHERE)
	window.localStorage.clear()
	forgetSupportCountryChoice()
	jest.restoreAllMocks()
})

describe('sanitizeSupportCountry', () => {
	test('accepts censored countries in any case', () => {
		expect(sanitizeSupportCountry('ir')).toBe('IR')
		expect(sanitizeSupportCountry(' RU ')).toBe('RU')
	})

	test('collapses anything else to anywhere', () => {
		expect(sanitizeSupportCountry('US')).toBe(ANYWHERE)
		expect(sanitizeSupportCountry('IRN')).toBe(ANYWHERE)
		expect(sanitizeSupportCountry(42)).toBe(ANYWHERE)
		expect(sanitizeSupportCountry(undefined)).toBe(ANYWHERE)
	})
})

describe('resolveSupportCountry', () => {
	test('falls back to the embed preset when the viewer never chose', () => {
		expect(resolveSupportCountry('ir')).toBe('IR')
		expect(resolveSupportCountry('')).toBe(ANYWHERE)
	})

	test("the viewer's choice beats the preset, including choosing anywhere", () => {
		storeSupportCountry('CN')
		expect(resolveSupportCountry('IR')).toBe('CN')
		storeSupportCountry(ANYWHERE)
		expect(resolveSupportCountry('IR')).toBe(ANYWHERE)
	})

	test('a tampered stored value is sanitized', () => {
		window.localStorage.setItem('unbounded.supportCountry', 'XX')
		expect(resolveSupportCountry('IR')).toBe(ANYWHERE)
	})

	test('survives storage that throws', () => {
		jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
		jest.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
		expect(resolveSupportCountry('RU')).toBe('RU')
		expect(() => storeSupportCountry('IR')).not.toThrow()
		expect(resolveSupportCountry('RU')).toBe('IR')
	})
})

describe('applySupportCountryPreset', () => {
	test("an embed without a preset doesn't erase another embed's, in either mount order", () => {
		applySupportCountryPreset('IR')
		applySupportCountryPreset(undefined)
		expect(supportCountryEmitter.state).toBe('IR')

		supportCountryEmitter.update(ANYWHERE)
		applySupportCountryPreset('')
		applySupportCountryPreset('IR')
		expect(supportCountryEmitter.state).toBe('IR')
	})

	test('clearing an embed preset resets to anywhere', () => {
		applySupportCountryPreset('IR')
		applySupportCountryPreset('', 'IR')
		expect(supportCountryEmitter.state).toBe(ANYWHERE)
	})

	test("the viewer's stored choice still wins over every embed", () => {
		storeSupportCountry('CN')
		applySupportCountryPreset('IR')
		applySupportCountryPreset(undefined)
		expect(supportCountryEmitter.state).toBe('CN')
	})

	test("with storage blocked, changing or clearing a preset keeps the viewer's choice", () => {
		jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
		jest.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('denied') })
		applySupportCountryPreset('IR')
		storeSupportCountry('RU')
		supportCountryEmitter.update('RU')
		applySupportCountryPreset('', 'IR')
		expect(supportCountryEmitter.state).toBe('RU')
		applySupportCountryPreset('CN', '')
		expect(supportCountryEmitter.state).toBe('RU')

		storeSupportCountry(ANYWHERE)
		supportCountryEmitter.update(ANYWHERE)
		applySupportCountryPreset('IR', 'CN')
		expect(supportCountryEmitter.state).toBe(ANYWHERE)
	})
})
