import styled from 'styled-components'

export const Wrapper = styled.div`
	display: flex;
	justify-content: ${({$align}: {$align: 'left' | 'center'}) => $align === 'left' ? 'flex-start' : 'center'};
	width: ${({$align}: {$align: 'left' | 'center'}) => $align === 'left' ? 'auto' : '100%'};
	a {
		display: flex;
		outline: none;
		cursor: pointer;
	}
`
