import styled from 'styled-components'
import {APP_STORE_LINKS, COLORS} from '../../../constants'
import {Text} from '../typography'
import {ChromeColor, FirefoxColor} from '../icons'
import {useContext} from 'react'
import {AppContext} from '../../../context'
import {isFirefox} from '../../../utils/userAgent'
import {useTranslation} from 'react-i18next'

const StyledLink = styled.a`
	display: flex;
	align-items: center;
	justify-content: center;
	flex-direction: row;
	background-color: ${COLORS.blue5};
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

const ExtensionButton = ({isSmall}: Props) => {
	const {t} = useTranslation()
	const {width} = useContext(AppContext)

	const largePadding = width < 900 ? '12px 16px' : '12px 40px'
	const smallPadding = width < 275 ? '12px 16px' : '12px 40px'

	return (
		<StyledLink
			style={{
				padding: isSmall ? smallPadding : largePadding
			}}
			href={isFirefox() ? APP_STORE_LINKS.firefox : APP_STORE_LINKS.chrome}
			target="_blank"
			rel="noreferrer"
		>
			{ isFirefox() ? <FirefoxColor /> : <ChromeColor /> }
			<Text
				style={{
					color: COLORS.grey2,
					fontWeight: 500,
					fontSize: 16,
					lineHeight: '24px',
				}}
			>
				{t('installCtaBtnSm', {browser: isFirefox() ? 'Firefox' : 'Chrome'})}
			</Text>
		</StyledLink>
	)
}

export default ExtensionButton