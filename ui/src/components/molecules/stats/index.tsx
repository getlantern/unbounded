import {Text} from '../../atoms/typography'
import Row from '../../atoms/row'
import React, {useContext, useId, useState} from 'react'
import {AppContext} from '../../../context'
// import {BREAKPOINT} from '../../../constants'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import {connectionsEmitter, lifetimeConnectionsEmitter} from '../../../utils/wasmInterface'
import {humanizeCount} from '../../../utils/humanize'
import {LifetimeConnectionsWrapper} from './styles'
// import {Layouts} from '../../../constants'
import {useTranslation} from 'react-i18next'
// import TwitterLink from '../../atoms/twitterLink'
import AdvancedStats from '../advancedStats'
import {Chevron, ToggleButton} from '../advancedStats/styles'

export const Connections = () => {
	const {t} = useTranslation()
	const connections = useEmitterState(connectionsEmitter)
	const currentConnections = connections.filter(c => c.state === 1).length
	return (
		<>
			<Text>{t('now')}</Text>
			<Text
				style={{minWidth: 10}}
			>
				{currentConnections}
			</Text>
		</>
	)
}


const Stats = () => {
	const {t} = useTranslation()
	const {settings} = useContext(AppContext)
	const {menu, theme} = settings
	const [advanced, setAdvanced] = useState(false)
	const panelId = useId()
	const connections = useEmitterState(connectionsEmitter)
	const lifetimeConnections = useEmitterState(lifetimeConnectionsEmitter)
	const currentConnections = connections.filter(c => c.state === 1).length

	// const fontSize = layout === Layouts.BANNER ? 14 : 12
	const fontSize = 14

	return (
		<>
			<Row
				borderBottom
			>
				<Text style={{fontSize}}>{(t('now'))}</Text>
				<Text style={{fontSize}}>{currentConnections}</Text>
			</Row>
			<Row
				borderBottom
			>
				{
					menu ? (
						<Text style={{fontSize}}>
							{t('total')}
						</Text>
					)
					: (
						<LifetimeConnectionsWrapper>
							<Text style={{fontSize}}>
								{t('total')}
							</Text>
							{/*<TwitterLink connections={lifetimeConnections} />*/}
						</LifetimeConnectionsWrapper>
					)
				}
				<Text style={{fontSize}}>{humanizeCount(lifetimeConnections)}</Text>
			</Row>
			{advanced && <AdvancedStats id={panelId}/>}
			<ToggleButton
				type={'button'}
				theme={theme}
				onClick={() => setAdvanced(!advanced)}
				aria-expanded={advanced}
				aria-controls={panelId}
			>
				{t(advanced ? 'hideAdvancedStats' : 'advancedStats')}
				<Chevron open={advanced} aria-hidden>▾</Chevron>
			</ToggleButton>
		</>
	)
}

export default Stats