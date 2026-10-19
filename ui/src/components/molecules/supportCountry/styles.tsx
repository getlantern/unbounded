import styled from 'styled-components'
import {COLORS, Themes} from '../../../constants'

export const Wrapper = styled.label`
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  padding: 8px 8px 0;
  font-family: 'Urbanist', sans-serif;
  font-size: 12px;
  line-height: 18px;
  color: ${({theme}: {theme: Themes}) => theme === Themes.DARK ? COLORS.grey2 : COLORS.blue5};
`

// Deliberately quiet: reads as a line of text with a dotted underline until it's focused, so it
// doesn't compete with the status switch above it.
export const Select = styled.select`
  && {
    appearance: none;
    -webkit-appearance: none;
    max-width: 100%;
    margin: 0;
    padding: 0 14px 0 0;
    border: none;
    border-bottom: 1px dotted currentColor;
    border-radius: 0;
    background-color: transparent;
    background-image: linear-gradient(45deg, transparent 50%, currentColor 50%),
      linear-gradient(135deg, currentColor 50%, transparent 50%);
    background-position: right 4px center, right 0 center;
    background-size: 4px 4px, 4px 4px;
    background-repeat: no-repeat;
    color: ${({theme}: {theme: Themes}) => theme === Themes.DARK ? COLORS.altBrand : COLORS.brand};
    color-scheme: ${({theme}: {theme: Themes}) => theme === Themes.DARK ? 'dark' : 'light'};
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  &&:focus-visible {
    outline: 2px solid currentColor;
    outline-offset: 2px;
  }
`
