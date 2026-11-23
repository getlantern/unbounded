import styled from 'styled-components'
import {COLORS, Themes} from '../../../constants'

interface ThemeProps {
	theme: Themes
}

export const ToggleButton = styled.button`
	&& {
		align-self: flex-end;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin: 8px 8px 0;
		padding: 2px 0;
		border: none;
		background: none;
		cursor: pointer;
		font-family: inherit;
		font-size: 12px;
		line-height: 20px;
		color: ${({theme}: ThemeProps) => theme === Themes.DARK ? COLORS.grey2 : COLORS.blue4};
		opacity: 0.8;
		transition: opacity 150ms ease;
		&:hover, &:focus-visible {
			opacity: 1;
			text-decoration: underline;
		}
	}
`

export const Chevron = styled.span`
	display: inline-block;
	font-size: 10px;
	transition: transform 200ms ease;
	transform: rotate(${({open}: { open: boolean }) => open ? 180 : 0}deg);
`

export const Panel = styled.div`
	display: flex;
	flex-direction: column;
	width: 100%;
	animation: advanced-stats-reveal 200ms ease-out;
	@keyframes advanced-stats-reveal {
		from { opacity: 0; transform: translateY(-4px); }
		to { opacity: 1; transform: translateY(0); }
	}
	@media (prefers-reduced-motion: reduce) {
		animation: none;
	}
`

export const StatRow = styled.div`
	display: flex;
	justify-content: space-between;
	align-items: center;
	gap: 16px;
	min-height: 32px;
	padding: 0 8px;
	box-sizing: border-box;
	border-bottom: 1px solid ${({theme}: ThemeProps) => theme === Themes.DARK ? COLORS.grey4 : COLORS.grey3};
`

export const Heading = styled.div`
	padding: 16px 8px 4px;
	font-size: 11px;
	font-weight: 600;
	letter-spacing: 0.06em;
	text-transform: uppercase;
	color: ${({theme}: ThemeProps) => theme === Themes.DARK ? COLORS.grey : COLORS.blue4};
`

export const Value = styled.span`
	font-variant-numeric: tabular-nums;
	white-space: nowrap;
`
