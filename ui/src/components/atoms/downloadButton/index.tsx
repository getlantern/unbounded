import styled from 'styled-components'
import {COLORS} from '../../../constants'
import {Text} from '../typography'
import {Download} from '../icons'
import {useContext} from 'react'
import {AppContext} from '../../../context'
import {useTranslation} from 'react-i18next'
import {vpnDownloadUrl} from '../../../utils/vpnDownload'

const StyledLink = styled.a`
	display: flex;
	align-items: center;
	justify-content: center;
	flex-direction: row;
	background-color: ${COLORS.blue5};
	color: ${COLORS.blue1};
	border-radius: 32px;
	gap: 8px;
	min-height: 56px;
  box-sizing: border-box;
	text-decoration: none;
	&:focus {
		outline: none;
	}
`

interface Props {
	isSmall?: boolean
}

const DownloadButton = ({isSmall}: Props) => {
	const {t} = useTranslation()
	const {width} = useContext(AppContext)

	const largePadding = width < 900 ? '12px 16px' : '12px 40px'
	const smallPadding = width < 275 ? '12px 16px' : '12px 40px'

	return (
		<StyledLink
			style={{
				padding: isSmall ? smallPadding : largePadding
			}}
			href={vpnDownloadUrl()}
			target="_blank"
			rel="noreferrer"
		>
			<Download />
			<Text
				style={{
					color: COLORS.blue1,
					fontWeight: 600,
					fontSize: 16,
					lineHeight: '24px',
				}}
			>
				{t('vpnCtaBtn')}
			</Text>
		</StyledLink>
	)
}

export default DownloadButton
