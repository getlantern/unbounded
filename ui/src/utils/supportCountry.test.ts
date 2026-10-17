import {ANYWHERE, resolveSupportCountry, sanitizeSupportCountry, storeSupportCountry} from './supportCountry'

afterEach(() => {
	window.localStorage.clear()
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
		expect(() => storeSupportCountry('IR')).not.toThrow()
		expect(resolveSupportCountry('RU')).toBe('RU')
	})
})
