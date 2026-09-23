import {Container, Body, BodyWrapper, Header, Item, HeaderRight, CtaWrapper} from './styles'
import React, {useContext, useState, useEffect} from 'react'
import {AppContext} from '../../../context'
import {BREAKPOINT, COLORS, Targets, Themes} from '../../../constants'
import Col from '../../atoms/col'
import SafeGlobe from '../../molecules/globe/safe'
import Row from '../../atoms/row'
import Control from '../../molecules/control'
import UnsupportedNote from '../../molecules/unsupportedNote'
import Stats, {Connections} from '../../molecules/stats'
// import Footer from '../../molecules/footer'
import ExpandCollapse from '../../atoms/expandCollapse'
import {useLatch} from '../../../hooks/useLatch'
import useWindowSize from '../../../hooks/useWindowSize'
import About from '../../molecules/about'
import Menu from '../../molecules/menu'
import LogoLink from '../../atoms/logoLink'
import ExtensionCta from '../../molecules/extensionCta'
import Love from '../../molecules/love'

const Floating = () => {
	const {width, settings} = useContext(AppContext)
	const {theme, menu, target} = settings
	const {height} = useWindowSize()
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
					padding: width > BREAKPOINT ? '24px 32px' : expanded ? '24px 16px' : '8px 16px'
				}}
			>
				<Body
					mobile={width < BREAKPOINT}
				>
					<Header
						style={{
							paddingBottom: !expanded ? 8 : 0
						}}
					>
						<div
							style={{height: 48}} // this is a hack to create real estate for the logo which is positioned absolute because it overlaps the header right
						/>
						{ settings.branding && <LogoLink style={{position: 'absolute'}} /> }
						<HeaderRight>
							{
								settings.collapse && (
									<ExpandCollapse
										expanded={expanded}
										setExpanded={setExpanded}
										carrot={true}
									/>
								)
							}
							{ menu && <Menu setExpanded={setExpanded} /> }
						</HeaderRight>
					</Header>
					{
						!expanded && (
							<Col>
								<Item
									style={{backgroundColor: settings.theme === Themes.LIGHT ? COLORS.white : COLORS.grey6}}
									theme={settings.theme}
								>
									<Control
										onToggle={onToggle}
										info
									/>
								</Item>
							</Col>
						)
					}
					{
						settings.globe && expanded && (
							<Col>
								<SafeGlobe target={settings.target}/>
							</Col>
						)
					}
					{
						expanded && (
							<Col>
								<About
									style={{marginBottom: 20, marginTop: 10, fontSize: 12, lineHeight: '20px'}}
								/>
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
								{
									expanded && (
										<>
											{height > 650 ? <Stats/> : <Row><Connections/></Row>}
										</>
									)
								}
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
							</Col>
						)
					}
				</Body>
			</BodyWrapper>
		</Container>
	)
}

export default Floating