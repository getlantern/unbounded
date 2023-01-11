import {AppWrapper} from './styles'
import {AppContextProvider} from '../context'
import useElementSize from '../hooks/useElementSize'
import {GOOGLE_FONT_LINKS} from '../constants'
import {useEffect, useLayoutEffect, useRef} from 'react'
import {Layouts, Themes} from '../index'

interface Props {
	children: (JSX.Element | false)[] | JSX.Element | false
	theme: Themes
	layout: Layouts
}

const Layout = ({children, theme, layout}: Props) => {
	const [ref, { width}, handleSize] = useElementSize()
	const fontLoaded = useRef(false)

	useLayoutEffect(() => {
		// Dynamically add font links to document
		// <link rel="preconnect" href="https://fonts.googleapis.com">
		// <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
		// <link href="https://fonts.googleapis.com/css2?family=Urbanist&display=swap" rel="stylesheet">
		if (fontLoaded.current) return
		const addLink = ({href, rel}: {href: string, rel: string}) => {
			const link = document.createElement('link');
			link.rel = rel;
			link.href = href;
			document.getElementsByTagName('head')[0].appendChild(link);
		}
		GOOGLE_FONT_LINKS.forEach(addLink)
		fontLoaded.current = true
	}, [fontLoaded])

	useEffect(() => {
		// recalculate size on layout dynamic changes
		handleSize()
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [layout])

	return (
		<AppContextProvider value={{width, theme}}>
			<AppWrapper
				layout={layout}
				theme={theme}
				ref={ref}
			>
				{children}
			</AppWrapper>
		</AppContextProvider>
	)
}

export default Layout