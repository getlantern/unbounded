import styled from 'styled-components'
import {COLORS, Themes} from '../../../constants'

interface Props {
	theme: Themes
	$menu: boolean
}
export const Container = styled.div`
	display: flex;
	background-color: ${(props: Props) => props.theme === Themes.LIGHT ? props.$menu ? COLORS.white : COLORS.grey1 : COLORS.grey6};
	border-radius: 16px;
	padding: 16px;
	margin-top: 16px;
  justify-content: center;
  align-items: center;
  flex-direction: column;
	gap: 8px;
  border: 1px solid ${(props: Props) => props.theme === Themes.LIGHT ? COLORS.grey2 : COLORS.grey4};
`
export const Column = styled.div`
	display: flex;
	flex-direction: column;
	align-items: center;
	gap: 8px;
	width: 100%;
`

export const Note = styled.p`
	&& {
		font-weight: 400;
		font-size: 14px;
		line-height: 23px;
		text-align: center;
		color: ${({theme}: {theme: Themes}) => theme === Themes.DARK ? COLORS.grey2 : COLORS.grey4};
		margin: 0;
		padding: 0;
		text-transform: none;
	}
`
