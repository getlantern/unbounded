import styled from 'styled-components'
import {COLORS} from '../../../constants'

export const Wrapper = styled.div`
	display: flex;
	justify-content: ${({$align}: {$align: 'left' | 'center'}) => $align === 'left' ? 'flex-start' : 'center'};
	width: ${({$align}: {$align: 'left' | 'center'}) => $align === 'left' ? 'auto' : '100%'};
	a {
		display: flex;
		cursor: pointer;
		border-radius: 4px;
	}
	a:focus:not(:focus-visible) {
		outline: none;
	}
	a:focus-visible {
		outline: 2px solid ${COLORS.altBrand};
		outline-offset: 4px;
	}
`
