export const BREAKPOINT = 800
export const MAX_WIDTH = 1000
export const COLORS = {
	black: '#000000',
	white: '#ffffff',
	green: '#00A83E',
	green2: '#006163',
	grey1: '#F8FAFB',
	grey2: '#EDEFEF',
	grey3: '#EBEBEB',
	grey4: '#3E464E',
	grey5: '#1B1C1D',
	grey6: '#040404',
	grey: '#707070',
	blue5: '#012D2D',
	blue4: '#007A7C',
	error: '#DB1C1C',
	brand: 'rgba(0, 122, 124, 1)',
	altBrand: '#00BCD4',
	transparent: 'transparent'
}
export const SHADOWS = {
	light: '0 4px 32px rgba(0, 97, 99, 0.1)',
	dark: '0px 2px 2px rgba(0, 0, 0, 0.16)'
}
export const UV_MAP_PATH_LIGHT = 'https://embed.lantern.io/uv-map.png'
export const UV_MAP_PATH_DARK = 'https://embed.lantern.io/uv-map-dark.png'
export const GOOGLE_FONT_LINKS = [
	{href: 'https://fonts.googleapis.com', rel: 'preconnect'},
	{href: 'https://fonts.gstatic.com', rel: 'preconnect'},
	{href: 'https://fonts.googleapis.com/css2?family=Urbanist:wght@400;500&display=swap', rel: 'stylesheet'}
]

export const SIGNATURE = 'lanternNetwork'
export enum MessageTypes {
	STORAGE_GET = 'storageGet',
	STORAGE_SET = 'storageSet',
	WASM_START = 'wasmStart',
	WASM_STOP = 'wasmStop',
	HYDRATE_STATE = 'hydrateState',
	STATE_UPDATE = 'stateUpdate',
	POPUP_OPENED = 'popupOpened',
	EVENT = 'event',
}

export enum Targets {
	WEB = 'web',
	EXTENSION_POPUP = 'extension-popup',
	EXTENSION_OFFSCREEN = 'extension-offscreen',
}

export enum Layouts {
	'BANNER' = 'banner',
	'PANEL' = 'panel',
	'FLOATING' = 'floating',
}

export enum Themes {
	'DARK' = 'dark',
	'LIGHT' = 'light',
	'AUTO' = 'auto'
}

export interface Settings {
	globe: boolean
	exit: boolean
	toast: boolean
	mobileBg: boolean
	desktopBg: boolean
	layout: Layouts
	theme: Themes
	editor: boolean
	collapse: boolean
	branding: boolean
	target: Targets
	mock: boolean
	keepText: boolean
	menu: boolean
	title: boolean
	share: boolean
	infoLink: string
}

export const defaultSettings: Settings = {
	mobileBg: true,
	desktopBg: true,
	exit: true,
	toast: false,
	globe: true,
	layout: Layouts.BANNER,
	theme: Themes.LIGHT,
	editor: false,
	collapse: true,
	branding: true,
	target: Targets.WEB,
	mock: false,
	keepText: true,
	menu: false, // @todo make true after extension is ready
	title: false,
	share: false,
	infoLink: ''
}

export const POPUP = 'popup'

export const AUTO_UPDATE_URL = 'https://embed.lantern.io/asset-manifest.json'

// XXX nelson 7/6/2023: We've found that widget concurrency (cTableSz and pTableSz) should be 
// <= the browser's limit on concurrent HTTP requests, which is ~6. That's because each widget
// worker creates HTTP requests, and we can't trust the browser to properly handle excess requests.
export const WASM_CLIENT_CONFIG = {
	type: 'widget',
	cTableSz: 5,
	pTableSz: 5,
	busBufSz: 4096,
	netstated: process.env.REACT_APP_NETSTATED_URL!,
	discoverySrv: process.env.REACT_APP_DISCOVERY_SRV!,
	discoverySrvEndpoint: process.env.REACT_APP_DISCOVERY_ENDPOINT!,
	stunBatchSize: 2,
	tag: '',
	egressAddr: process.env.REACT_APP_EGRESS_ADDR!,
	egressEndpoint: process.env.REACT_APP_EGRESS_ENDPOINT!,
}

// @todo rm stubbing out store urls until extension is ready
export const APP_STORE_LINKS = {
	chrome: 'https://chromewebstore.google.com/detail/unbounded/jonhnkjdlimggpmbehgkgpjgphoepfdj',
	// chrome: 'https://unbounded.lantern.io/coming-soon',
	firefox: 'https://addons.mozilla.org/en-US/firefox/addon/unbounded/',
	// firefox: 'https://unbounded.lantern.io/coming-soon',
}
