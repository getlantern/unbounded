import {useContext} from 'react'
import {AppContext} from '../../../context'
import {Container, Column, Note} from './styles'
import DownloadButton from '../../atoms/downloadButton'
import {Text} from '../../atoms/typography'
import {useTranslation} from 'react-i18next'
import {COLORS, Themes} from '../../../constants'
import {isActionModeSite} from '../../../utils/vpnDownload'

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

	if (!isActionModeSite()) return (
		<Container
			theme={theme}
			$menu={menu}
		>
			<DownloadButton />
			<Note theme={theme}>{t('vpnCtaNote')}</Note>
		</Container>
	)

	return (
		<Container
			theme={theme}
			$menu={menu}
		>
			<Column $maxWidth={312}>
				<div style={{width: '100%'}}>
					<Text
						style={{
							fontWeight: 600,
							fontSize: 16,
							lineHeight: '26px'
						}}
					>
						{t('vpnCtaTitle')}
					</Text>
					<Text
						style={{
							fontWeight: 500,
							fontSize: 14,
							lineHeight: '23px',
							color: theme === Themes.DARK ? COLORS.grey2 : COLORS.grey4
						}}
					>
						{t('vpnCtaBody')}
					</Text>
				</div>
				<DownloadButton fullWidth />
			</Column>
		</Container>
	)
}

export default VpnCta
