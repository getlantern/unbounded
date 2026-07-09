import styled from 'styled-components'

interface Props {
	active: boolean
	offset: number
	size: number
	$simple?: boolean
}
const Container = styled.div`
  width: 100%;
  height: 100%;
  display: flex;
  justify-content: center;
  align-items: center;
  //overflow: hidden;
  position: relative;

  > div {
    position: absolute;
    top: ${({offset}: Props) => offset}px; // ugly offset to match figma @todo try to fix this with flexbox
    cursor: ${({active}: Props) => active ? 'pointer': 'all-scroll'};
  }

  ${({$simple}: Props) => $simple ? `
  canvas {
    filter: brightness(1.15);
  }

  // static stand-in for a drop-shadow on the canvas: the sphere is a 193px
  // circle centered in the box, so a shadowed circle behind it looks the same
  // without re-rasterizing a 64px blur on every animation frame
  &::before {
    content: '';
    position: absolute;
    width: 193px;
    height: 193px;
    border-radius: 50%;
    box-shadow: 0 4px 64px rgba(0, 97, 99, 0.26);
  }` : ''}

  > span.shadow {
    position: absolute;
    bottom: 0;
    width: ${({size}: {size: number}) => size}px;
	  height: 30px;
  }
`

export {Container}