import {Info as InfoIcon} from '../../atoms/icons'
import {LinkWrapper, StyledButton, Wrapper} from './styles'
import {Text} from '../../atoms/typography'
import {useContext, useState} from 'react'
import { Popover } from 'react-tiny-popover'
import {AppContext} from '../../../context'
import {useTranslation} from 'react-i18next'

const Info = () => {
	const {t} = useTranslation()
	const {theme, infoLink} = useContext(AppContext).settings
	const [active, setActive] = useState(false)
	return (
		<>
			<Popover
				isOpen={active}
				positions={['bottom', 'top', 'left', 'right']}
				onClickOutside={() => setActive(false)}
				containerStyle={{zIndex:'2147483647'}} // 2147483647 is largest positive value of a signed integer on a 32 bit system
				content={
					<Wrapper
						theme={theme}
					>
						<Text>
							{t('intro')}
							{/*<a href={'https://lantern.io'} target={'_blank'} rel={'noreferrer'}>Lantern</a>.*/}
							{ !!infoLink.length && <LinkWrapper dangerouslySetInnerHTML={{__html: infoLink}} />}
						</Text>
					</Wrapper>
				}
			>
				<StyledButton
					onClick={() => setActive(!active)}
				>
					<InfoIcon/>
				</StyledButton>
			</Popover>
		</>
	)
}

export default Info