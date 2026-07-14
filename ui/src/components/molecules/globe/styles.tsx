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
  }` : ''}

  > span.shadow {
    position: absolute;
    bottom: 0;
    width: ${({size}: {size: number}) => size}px;
	  height: 30px;
  }
`

export {Container}