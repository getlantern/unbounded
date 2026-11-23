import {ACTION_MODE_SITE_HOSTS, VPN_SITE_URL} from '../constants'

const currentHost = () => {
	try {
		return window.location.hostname.toLowerCase()
	} catch {
		return ''
	}
}

export const isActionModeSite = () => ACTION_MODE_SITE_HOSTS.includes(currentHost())

// The widget renders inline in the host page, so location.hostname is the embedding site
export const vpnDownloadUrl = () => {
	const url = new URL(VPN_SITE_URL)
	url.searchParams.set('utm_source', 'actionmode')
	if (isActionModeSite()) {
		url.searchParams.set('utm_medium', 'site')
		url.searchParams.set('utm_campaign', 'card')
	} else {
		url.searchParams.set('utm_medium', 'embed')
		url.searchParams.set('utm_campaign', currentHost() || 'embed')
	}
	return url.toString()
}
