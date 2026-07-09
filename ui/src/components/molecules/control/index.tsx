import {Text} from '../../atoms/typography'
import Switch from '../../atoms/switch'
import Info from '../info'
import {TextInfo} from './styles'
import {useContext} from 'react'
import {AppContext} from '../../../context'
import {COLORS, Layouts} from '../../../constants'
import {useTranslation} from 'react-i18next'
import {useSharingToggle} from '../../../hooks/useSharingToggle'
import Modal from '../modal'

interface Props {
	onToggle?: (s: boolean) => void
	info?: boolean
}

const Control = ({onToggle, info = false}: Props) => {
	const {t} = useTranslation()
	const {layout} = useContext(AppContext).settings
	const {sharing, isCensored, onIgnore, switchProps} = useSharingToggle(onToggle)

	return (
		<>
			<Modal isCensored={isCensored} onIgnore={onIgnore} />
			<TextInfo>
				<Text
					style={{minWidth: 90, fontWeight: 'bold', fontSize: layout === Layouts.BANNER ? 14 : 12}}
				>
					{t('status')} <span style={{color: sharing ? COLORS.green : COLORS.error}}>{sharing ? t('on') : t('off')}</span>
				</Text>
				{ info && <Info /> }
			</TextInfo>
			<Switch {...switchProps} />
		</>
	)
}

export default Control
