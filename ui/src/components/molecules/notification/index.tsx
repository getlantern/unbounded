import {Container, Text} from './styles'
import {StateEmitter, useEmitterState} from '../../../hooks/useStateEmitter'
import {useContext, useEffect, useState} from 'react'
import {Ellipse} from '../../atoms/ellipse'
import Explosion from './explosion'
import {AppContext} from '../../../context'
import {Layouts} from '../../../constants'
import {sharingEmitter} from '../../../utils/wasmInterface'

interface NotificationType {
	id: number
	text: string
	autoHide?: boolean
	ellipse?: boolean
	heart?: boolean
	show?: boolean
	timeoutHide?: ReturnType<typeof setTimeout> | null
	timeoutRemove?: ReturnType<typeof setTimeout> | null
}

const notificationQueue = new StateEmitter<NotificationType[]>([])
export const pushNotification = (notification: NotificationType) => {
	const index = notificationQueue.state.findIndex(n => n.id === notification.id)
	if (index >= 0) {
		notificationQueue.state[index] = notification
		return notificationQueue.update([...notificationQueue.state])
	}
	notificationQueue.update([...notificationQueue.state, notification])
}

export const removeNotification = (id: number) => {
	notificationQueue.update(notificationQueue.state.filter(n => n.id !== id))
}

export const Notification = () => {
	const {theme, layout} = useContext(AppContext).settings
	const notifications = useEmitterState(notificationQueue)
	const sharing = useEmitterState(sharingEmitter)
	const [notification, setNotification] = useState<NotificationType | null>(null)
	const show = notification?.show ?? false
	const simple = layout === Layouts.SIMPLE
	const fontSize = layout === Layouts.BANNER ? 14 : 12
	// in the simple layout the sphere bottom sits at the container bottom and the
	// globe-to-control gap is 24px, so -12 puts the notification 12px above the control
	const bottomShown = simple ? -12 : 0
	const bottomHidden = simple ? -22 : -10

	// turning sharing off dismisses everything: flush the queue, cancel any
	// pending hide/remove timers, and drop the visible notification. Without
	// this, a non-autoHide notification (e.g. "waiting for connections") only
	// ever clears when another notification arrives, so it outlives a quick
	// on -> off toggle indefinitely.
	useEffect(() => {
		if (sharing) return
		setNotification(current => {
			if (current?.timeoutHide) clearTimeout(current.timeoutHide)
			if (current?.timeoutRemove) clearTimeout(current.timeoutRemove)
			return null
		})
		if (notificationQueue.state.length) notificationQueue.update([])
	}, [sharing])

	useEffect(() => {
		if (!notifications.length) return setNotification(null)

		// if the notification is already showing, set show to false and remove it
		if (notification && !notification.autoHide && !notification.timeoutRemove) {
			if (notifications.length > 1) {
				const timeoutRemove = setTimeout(() => removeNotification(notification.id), 750)
				return setNotification({...notification, show: false, timeoutRemove})
			}
		}

		if (notification && notification.id === notifications[0].id) return // same notification
		if (!notifications[0].autoHide) return setNotification({...notifications[0], show: true})

		const timeoutHide = setTimeout(() => {
			const timeoutRemove = setTimeout(() => removeNotification(notifications[0].id), 750)
			setNotification({...notifications[0], show: false, timeoutRemove})
		}, 3500)

		setNotification({...notifications[0], show: true, timeoutHide})
	}, [notification, notifications])

	return (
		<Container
			theme={theme}
			$simple={simple}
			style={{
				top: 'unset',
				bottom: show ? bottomShown : bottomHidden,
				opacity: show ? 1 : 0,
			}}
		>
			{
				notification?.heart && (
					<Explosion
						id={notification.id}
					/>
				)
			}
			<Text style={{fontSize}} theme={theme}>{notification?.text}{notification?.ellipse && <Ellipse />}</Text>
		</Container>
	)
}