import {useContext} from 'react'
import {AppContext} from '../../../context'
import {LOGO_STACK_BREAKPOINT} from '../../../constants'
import {LogoHorizontal, LogoStacked} from '../icons'
import {Wrapper} from './styles'

const LogoLink = () => {
	const {width} = useContext(AppContext)
	return (
		<Wrapper>
			<a href={'https://actionmode.lantern.io'} target={'_blank'} rel={'noopener noreferrer'} aria-label={'Lantern Action Mode'}>
				{width < LOGO_STACK_BREAKPOINT ? <LogoStacked /> : <LogoHorizontal />}
			</a>
		</Wrapper>
	)
}

export default LogoLink
