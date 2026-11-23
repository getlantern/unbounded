import {isActionModeSite, vpnDownloadUrl} from './vpnDownload'

const parse = (href: string) => {
	const url = new URL(href)
	return {
		destination: `${url.origin}${url.pathname}`,
		source: url.searchParams.get('utm_source'),
		medium: url.searchParams.get('utm_medium'),
		campaign: url.searchParams.get('utm_campaign'),
	}
}

describe('vpnDownloadUrl', () => {
	test.each([
		['actionmode.lantern.io', 'site', 'card'],
		['unbounded.lantern.io', 'site', 'card'],
		['chinadigitaltimes.net', 'embed', 'chinadigitaltimes.net'],
		['', 'embed', 'embed'],
	])('host %p -> utm_medium=%s utm_campaign=%s', (host, medium, campaign) => {
		expect(parse(vpnDownloadUrl(host))).toEqual({
			destination: 'https://vpn.lantern.io/',
			source: 'actionmode',
			medium,
			campaign,
		})
	})

	test('defaults to the embedding page hostname', () => {
		// jsdom serves the test page from localhost
		expect(parse(vpnDownloadUrl()).campaign).toBe(window.location.hostname)
		expect(parse(vpnDownloadUrl()).medium).toBe('embed')
	})

	describe('when the hostname cannot be read', () => {
		const original = window.location
		beforeEach(() => {
			// @ts-ignore jsdom lets tests replace location once it is deleted
			delete window.location
			// @ts-ignore
			window.location = {get hostname() { throw new Error('blocked') }}
		})
		afterEach(() => {
			// @ts-ignore
			window.location = original
		})

		test('falls back to the embed campaign', () => {
			expect(parse(vpnDownloadUrl())).toEqual({
				destination: 'https://vpn.lantern.io/',
				source: 'actionmode',
				medium: 'embed',
				campaign: 'embed',
			})
		})
	})
})

describe('isActionModeSite', () => {
	test('matches only the Action Mode website hosts', () => {
		expect(isActionModeSite('actionmode.lantern.io')).toBe(true)
		expect(isActionModeSite('unbounded.lantern.io')).toBe(true)
		expect(isActionModeSite('lantern.io')).toBe(false)
		expect(isActionModeSite('evil-actionmode.lantern.io.example')).toBe(false)
	})
})
