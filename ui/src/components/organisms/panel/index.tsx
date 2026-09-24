import {Container, Body, BodyWrapper, ExpandWrapper, Header, CtaWrapper} from './styles'
import React, {useContext, useState, useEffect} from 'react'
import {AppContext} from '../../../context'
import {BREAKPOINT, COLORS, Targets, Themes} from '../../../constants'
import Col from '../../atoms/col'
import SafeGlobe from '../../molecules/globe/safe'
import Row from '../../atoms/row'
import Control from '../../molecules/control'
import UnsupportedNote from '../../molecules/unsupportedNote'
import Stats from '../../molecules/stats'
import About from '../../molecules/about'
// import Footer from '../../molecules/footer'
import {ExpandCollapsePanel} from '../../atoms/expandCollapse'
import {useLatch} from '../../../hooks/useLatch'
import Menu from '../../molecules/menu'
import LogoLink from '../../atoms/logoLink'
import ExtensionCta from '../../molecules/extensionCta'
import Love from '../../molecules/love'

const Panel = () => {
	const {width, settings} = useContext(AppContext)
	const {theme, menu, branding, target} = settings
	const [expanded, setExpanded] = useState(!settings.collapse)
	const interacted = useLatch(expanded)
	const onToggle = (share: boolean) => !interacted && share ? setExpanded(share) : null
	useEffect(() => setExpanded(!settings.collapse), [settings.collapse]) // hydrate on settings change

	return (
		<Container
			theme={theme}
		>
			<BodyWrapper
				style={{
					padding: width > BREAKPOINT ? '24px 32px' : '24px 16px'
				}}
			>
				<Body
					mobile={width < BREAKPOINT}
				>
					<Header>
						{ branding ? <LogoLink /> : <div /> }
						{ menu && <Menu /> }
					</Header>
					{ !expanded && <About style={{padding: '24px 0 16px 0', fontSize: 12, lineHeight: '20px'}} /> }
					{
						settings.globe && expanded && (
							<Col>
								<SafeGlobe target={settings.target}/>
							</Col>
						)
					}
					{ expanded && <About style={{padding: '24px 0 16px 0', fontSize: 12, lineHeight: '20px'}} /> }
					<Col>
						<Row
							borderTop
							borderBottom
							backgroundColor={settings.theme === Themes.DARK ? COLORS.grey6 : COLORS.white}
						>
							<Control
								onToggle={onToggle}
							/>
						</Row>
						<UnsupportedNote/>
						<Stats/>
						{
							!menu && (target === Targets.WEB) && (
								<CtaWrapper>
									<ExtensionCta isSmall={true}/>
								</CtaWrapper>
							)
						}
						<div style={{marginTop: 16}}>
							<Love/>
						</div>
						{
							settings.collapse && settings.globe && (
								<ExpandWrapper
									style={{margin: '24px 0 0'}}
								>
									<ExpandCollapsePanel
										expanded={expanded}
										setExpanded={setExpanded}
									/>
								</ExpandWrapper>
							)
						}
					</Col>
				</Body>
			</BodyWrapper>
		</Container>
	)
}

export default Panel