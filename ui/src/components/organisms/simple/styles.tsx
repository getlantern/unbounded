import styled from 'styled-components'
import {COLORS} from '../../../constants'

// palette private to the simple layout (from the Figma spec)
export const SIMPLE_COLORS = {
	green: '#0A8638',
	lightGreen: '#A2DDAF',
	heart: '#ED4C5C',
	glow: '#00BDD6',
}

const Container = styled.div`
  box-sizing: border-box;
  width: 100%;
  height: 344px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 24px;
  padding: 24px;
  border: 1px solid ${COLORS.grey2};
  border-radius: 16px;
`

const OffPill = styled.div`
  position: relative;
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 8px 16px;
  background-color: ${COLORS.blue5};
  border-radius: 9999px;

  @keyframes breathe {
    0% {
      opacity: 1;
    }
    50% {
      opacity: 0;
    }
    100% {
      opacity: 1;
    }
  }

  // the glow lives on a pseudo-element so the breathing animates opacity
  // (compositor-only) instead of re-rasterizing a drop-shadow filter each frame
  &::before {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: inherit;
    box-shadow: 0 0 10px ${SIMPLE_COLORS.glow};
    animation: breathe 2.75s ease-in-out infinite;
  }
`

const OnPanel = styled.div`
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px 16px;
  width: 100%;
  background-color: ${SIMPLE_COLORS.green};
  border-radius: 8px;
`

const PanelRow = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
`

const PanelLeft = styled.div`
  display: flex;
  align-items: center;
  gap: 8px;
`

const BarText = styled.p`
  margin: 0;
  font-weight: 600;
  font-size: 16px;
  line-height: 24px;
  color: ${COLORS.white};
  white-space: nowrap;
`

const StatusDot = styled.span`
  width: 12px;
  height: 12px;
  margin: 6px;
  border-radius: 50%;
  background-color: ${SIMPLE_COLORS.lightGreen};
  box-shadow: 0 0 0 4px rgba(255, 255, 255, 0.2);
`

export {Container, OffPill, OnPanel, PanelRow, PanelLeft, BarText, StatusDot}
