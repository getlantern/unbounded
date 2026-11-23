import {useContext} from 'react'
import {AppContext} from '../../../context'
import {Container, Column, Note} from './styles'
import DownloadButton from '../../atoms/downloadButton'
import {useTranslation} from 'react-i18next'

interface Props {
	isSmall?: boolean
}

const VpnCta = ({isSmall}: Props) => {
	const {t} = useTranslation()
	const {settings} = useContext(AppContext)
	const {theme, menu} = settings

	if (isSmall) return (
		<Column>
			<DownloadButton isSmall />
			<Note theme={theme}>{t('vpnCtaNote')}</Note>
		</Column>
	)

	return (
		<Container
			theme={theme}
			$menu={menu}
		>
			<DownloadButton />
			<Note theme={theme}>{t('vpnCtaNote')}</Note>
		</Container>
	)
}

export default VpnCta
