import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import {StateEmitter} from './hooks/useStateEmitter'
import {defaultSettings, Settings, Themes} from './constants'

// Always register the headless API on window.LanternProxy
import './headlessApi'

export const settingsEmitter = new StateEmitter<{ [key: number]: Settings }>({})

const upperSnakeToCamel = (s: string | undefined) => {
	if (!s) return s
	return s.toLowerCase().replace(/([-_][a-z])/ig, ($1) => {
		return $1.toUpperCase()
			.replace('-', '')
			.replace('_', '')
	})
}
const hydrateSettings = (i: number, dataset: Settings) => {
	const settings = {...defaultSettings}
	Object.keys(settings).forEach(key => {
		try {
			// @ts-ignore
			settings[key] = JSON.parse(dataset[key])
		} catch {
			// @ts-ignore
			settings[key] = dataset[key] || settings[key]
		}
	})
	// optional settings overrides from env vars
	Object.keys(process.env).filter(key => {
		const settingsKeys = Object.keys(settings)
		const envKey = upperSnakeToCamel(key.split('REACT_APP_')[1])
		return envKey && settingsKeys.includes(envKey)
	}).forEach(key => {
		const settingsKey = upperSnakeToCamel(key.split('REACT_APP_')[1])
		try {
			// @ts-ignore
			settings[settingsKey] = JSON.parse(process.env[key])
		} catch {
			// @ts-ignore
			settings[settingsKey] = process.env[key]
		}
	})
	// set theme based on browser preference if using "dynamic"
	if (settings.theme === Themes.AUTO) {
		settings.theme = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? Themes.DARK : Themes.LIGHT
	}
	settingsEmitter.update({...settingsEmitter.state, [i]: settings})
}

const init = (embeds: NodeListOf<HTMLElement>) => {
	embeds.forEach((embed, i) => {
		const dataset = embed.dataset as unknown as Settings
		hydrateSettings(i, dataset)

		// Headless mode: skip all UI rendering, just expose window.LanternProxy
		// Usage: <browsers-unbounded data-headless="true"></browsers-unbounded>
		if ((embed.dataset as any).headless === 'true') {
			console.log('Unbounded: headless mode — UI rendering skipped, use window.LanternProxy')
			return
		}

		const root = ReactDOM.createRoot(
			embed
		)

		const observer = new MutationObserver((mutations) => {
			mutations.forEach((mutation) => {
				if (mutation.attributeName && mutation.attributeName.includes('data-')) {
					// @ts-ignore
					const dataset = mutation.target.dataset as unknown as Settings
					hydrateSettings(i, dataset)
				}
			})
		})

		observer.observe(embed, {
			attributes: true, childList: false, characterData: false
		})

		root.render(
			<React.StrictMode>
				<App
					appId={i}
					embed={embed}
				/>
			</React.StrictMode>
		)
	})
}

// get all embeds on page element named "lantern-network" or "browsers-unbounded"
const getEmbeds = () => {
	// @todo remove legacy embeds after full migration
	const legacy = document.querySelectorAll('lantern-network') as NodeListOf<HTMLElement>
	const modern = document.querySelectorAll('browsers-unbounded') as NodeListOf<HTMLElement>
	if (legacy.length) return legacy
	return modern
}

// try to load embeds immediately
const embeds = getEmbeds()
if (embeds.length) init(embeds)
else {
	// if embeds are not loaded yet, wait for them to load
	const observer = new MutationObserver((mutations, mutationInstance) => {
		const embeds = getEmbeds()
		if (embeds.length) {
			init(embeds)
			mutationInstance.disconnect()
		}
	})

	observer.observe(document, {
		childList: true,
		subtree: true
	})
}

