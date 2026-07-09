import styled from 'styled-components'
import {COLORS, Layouts, SHADOWS, Themes} from '../constants'

interface Props {
  theme: Themes
  $menu: boolean
  layout: Layouts
}

export const getBorderRadius = (layout: Layouts) => {
  switch (layout) {
    case Layouts.PANEL:
    case Layouts.SIMPLE:
      return '16px';
    case Layouts.FLOATING:
      return '16px 16px 0 0';
    case Layouts.BANNER:
      return '0';
    default:
      return '32px';
  }
};

const AppWrapper = styled.section`
  font-family: 'Urbanist', sans-serif;
  display: flex;
  width: 100%;
  max-width: ${({layout}: Props) => layout === Layouts.PANEL ? '330px' : layout === Layouts.FLOATING ? '360px' : layout === Layouts.SIMPLE ? '344px' : 'unset'};
  border-radius: ${({ layout }: Props) => getBorderRadius(layout)};
  background-color: ${({theme, $menu}: Props) => theme === Themes.DARK ? COLORS.grey5 : $menu ? COLORS.grey1 : COLORS.white };
  box-sizing: content-box;
  position: ${({layout}: Props) => layout !== Layouts.FLOATING ? 'relative' : 'fixed'};
  ${({layout}: Props) => layout === Layouts.FLOATING ? 'bottom: 0;\n  right: 10px;\n z-index: 2147483646;' : ''} // 2147483647 is largest positive value of a signed integer on a 32 bit system
  margin: 0 auto;
  box-shadow: ${({layout}: Props) => layout === Layouts.FLOATING ? SHADOWS.light : ''};
  * {
    box-sizing: unset;
    //all: revert;
    -webkit-font-smoothing: initial;
    -moz-osx-font-smoothing: initial;
  }
`

export {AppWrapper}