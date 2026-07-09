import styled from 'styled-components'
import {COLORS, SHADOWS, Themes} from '../../../constants'

interface ContainerProps {
	theme: Themes
	$simple: boolean
}

export const Container = styled.div`
  position: absolute;
  bottom: 0;
  top: unset;
  border-radius: 100px;
  background: ${({theme}: ContainerProps) => theme === Themes.LIGHT ? COLORS.grey1 : COLORS.grey6};
  border: 1px solid ${({theme}: ContainerProps) => theme === Themes.LIGHT ? COLORS.grey2 : COLORS.grey6};
  box-shadow: ${({theme}: ContainerProps) => theme === Themes.LIGHT ? SHADOWS.light : SHADOWS.dark};
	padding: 14px 16px;
  transition: opacity 300ms ease-out, bottom 300ms ease-out;
	pointer-events: none;
	display: flex;
	gap: 16px;
	align-items: center;
	justify-content: center;
  white-space: nowrap;

  ${({$simple}: ContainerProps) => $simple ? `
    box-sizing: border-box;
    height: 39px;
    padding: 0 16px;
    box-shadow: 0 0 16px rgba(0, 97, 99, 0.1);
  ` : ''}
`

export const Text = styled.p`
  margin: 0;
  font-style: normal;
  font-weight: 500;
  font-size: 14px;
  line-height: 16px;
  color: ${({theme}: { theme: Themes }) => theme === Themes.LIGHT ? COLORS.blue5 : COLORS.grey2};
`

export const LottieContainer = styled.div`
  position: relative;
  width: 32px;
  height: 27px;
`

export const LottieWrapper = styled.div`
  position: absolute;
  bottom: -55px;
  left: -105px;
  width: 420px;
`