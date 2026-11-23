import {useContext} from 'react'
import {AppContext} from '../../../context'
import {LOGO_STACK_BREAKPOINT} from '../../../constants'
import {LogoHorizontal, LogoStacked} from '../icons'
import {Wrapper} from './styles'

interface Props {
	align?: 'left' | 'center'
	stackBelow?: number
}

const LogoLink = ({align = 'center', stackBelow = LOGO_STACK_BREAKPOINT}: Props) => {
	const {width} = useContext(AppContext)
	return (
		<Wrapper $align={align}>
			<a href={'https://actionmode.lantern.io'} target={'_blank'} rel={'noopener noreferrer'} aria-label={'Lantern Action Mode'}>
				{width < stackBelow ? <LogoStacked /> : <LogoHorizontal />}
			</a>
		</Wrapper>
	)
}

export default LogoLink
