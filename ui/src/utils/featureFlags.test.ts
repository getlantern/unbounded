import {
	fetchFeatureFlags,
	loadFeatureFlags,
	resetFeatureFlagsForTest,
	supportCountryEnabledEmitter,
} from './featureFlags'

const URL = 'https://features.test/v1/features'

const respond = (body: unknown, init: {ok?: boolean, json?: () => Promise<unknown>} = {}) =>
	jest.fn().mockResolvedValue({ok: init.ok ?? true, json: init.json ?? (() => Promise.resolve(body))})

const setFetch = (fn: jest.Mock) => Object.defineProperty(window, 'fetch', {value: fn, configurable: true})

afterEach(() => {
	jest.useRealTimers()
	resetFeatureFlagsForTest()
	delete process.env.REACT_APP_FEATURES_URL
})

describe('fetchFeatureFlags', () => {
	it('reads the support country flag when it is on', async () => {
		const fetchMock = respond({features: {unbounded_support_country: true}})
		setFetch(fetchMock)
		await expect(fetchFeatureFlags(URL)).resolves.toEqual({supportCountry: true})
		expect(fetchMock).toHaveBeenCalledWith(URL, expect.objectContaining({credentials: 'omit'}))
	})

	it('is off when the flag is off', async () => {
		setFetch(respond({features: {unbounded_support_country: false}}))
		await expect(fetchFeatureFlags(URL)).resolves.toEqual({supportCountry: false})
	})

	it('makes no request and is off without a URL', async () => {
		const fetchMock = respond({features: {unbounded_support_country: true}})
		setFetch(fetchMock)
		await expect(fetchFeatureFlags('')).resolves.toEqual({supportCountry: false})
		await expect(fetchFeatureFlags(undefined)).resolves.toEqual({supportCountry: false})
		expect(fetchMock).not.toHaveBeenCalled()
	})

	it('is off on a network error', async () => {
		setFetch(jest.fn().mockRejectedValue(new TypeError('Failed to fetch')))
		await expect(fetchFeatureFlags(URL)).resolves.toEqual({supportCountry: false})
	})

	it('is off on a non-200 reply even if its body says on', async () => {
		setFetch(respond({features: {unbounded_support_country: true}}, {ok: false}))
		await expect(fetchFeatureFlags(URL)).resolves.toEqual({supportCountry: false})
	})

	it.each([
		['unparseable JSON', undefined, () => Promise.reject(new SyntaxError('bad json'))],
		['null', null, undefined],
		['no features', {}, undefined],
		['a truthy non-boolean', {features: {unbounded_support_country: 'true'}}, undefined],
		['features as an array', {features: ['unbounded_support_country']}, undefined],
	])('is off on a malformed reply: %s', async (_, body, json) => {
		setFetch(respond(body, {json}))
		await expect(fetchFeatureFlags(URL)).resolves.toEqual({supportCountry: false})
	})

	it('is off and aborts the request when it outlasts the timeout', async () => {
		jest.useFakeTimers()
		let signal: AbortSignal | undefined
		setFetch(jest.fn((_: string, init: RequestInit) => {
			signal = init.signal ?? undefined
			return new Promise(() => {})
		}))
		const flags = fetchFeatureFlags(URL, 3000)
		jest.advanceTimersByTime(3000)
		await expect(flags).resolves.toEqual({supportCountry: false})
		expect(signal?.aborted).toBe(true)
	})
})

describe('loadFeatureFlags', () => {
	it('fetches once per page and publishes the result', async () => {
		process.env.REACT_APP_FEATURES_URL = URL
		const fetchMock = respond({features: {unbounded_support_country: true}})
		setFetch(fetchMock)
		expect(supportCountryEnabledEmitter.state).toBe(false)
		await Promise.all([loadFeatureFlags(), loadFeatureFlags()])
		expect(fetchMock).toHaveBeenCalledTimes(1)
		expect(supportCountryEnabledEmitter.state).toBe(true)
	})

	it('leaves the feature off when the URL is unset', async () => {
		const fetchMock = respond({features: {unbounded_support_country: true}})
		setFetch(fetchMock)
		await loadFeatureFlags()
		expect(fetchMock).not.toHaveBeenCalled()
		expect(supportCountryEnabledEmitter.state).toBe(false)
	})
})
