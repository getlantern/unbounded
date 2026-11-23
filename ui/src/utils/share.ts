export const twitterLink = (text: string, url: string) => `https://twitter.com/intent/tweet?text=${encodeURIComponent(text)}&url=${encodeURIComponent(url)}`

export const connectedTwitterLink = (connected: number | string) => {
	const text = `I've helped${!!connected ? ` ${connected}` : ''} people from censored regions connect to the uncensored internet. Use your browser to fight global internet censorship. Join the #LanternNetwork at `
	const url = `actionmode.lantern.io`
	return twitterLink(text, url)
}