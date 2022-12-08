import {useRef} from 'react'
import useEventListener from './useEventListener'

const DELAY = 50
const THRESHOLD = -50

const useExitIntent = (callback: () => void) => {
	const isMobileDevice = /Android|webOS|iPhone|iPad|iPod|BlackBerry|IEMobile|Opera Mini/i.test(navigator.userAgent)
	const body = useRef(document.body)
	const lastPos = useRef<number | null>(null)
	const newPos = useRef<number | null>(null)
	const delta = useRef<number | null>(null)
	const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

	// desktop mouse leave top exit intent
	const desktopExitIntentCb = (event: MouseEvent) => {
		if (event.offsetY < 1) callback()
	}
	useEventListener('mouseleave', desktopExitIntentCb, body)


	// mobile fast scroll up exit intent
	const resetMobileFlags = () => {
		lastPos.current = null
		delta.current = 0
	}

	const mobileExitIntentCb = () => {
		if (!isMobileDevice) return

		newPos.current = window.scrollY
		if (lastPos.current !== null) delta.current = newPos.current - lastPos.current

		lastPos.current = newPos.current

		if (timer.current) clearTimeout(timer.current)
		timer.current = setTimeout(resetMobileFlags, DELAY)

		if (!!delta.current && delta.current < THRESHOLD) callback()
	}
	useEventListener('scroll', mobileExitIntentCb)
}

export default useExitIntent