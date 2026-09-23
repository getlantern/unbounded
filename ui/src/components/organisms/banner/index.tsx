import {Body, BodyWrapper, Container, Header, HeaderRight, HeaderWrapper, Item} from './styles'
import Control from '../../molecules/control'
import UnsupportedNote from '../../molecules/unsupportedNote'
import Menu from '../../molecules/menu'
import React, {useContext, useEffect, useState} from 'react'
import Col from '../../atoms/col'
import SafeGlobe from '../../molecules/globe/safe'
import Row from '../../atoms/row'
import {BREAKPOINT, COLORS, Targets, Themes} from '../../../constants'
import Stats, {Connections} from '../../molecules/stats'
import About from '../../molecules/about'
// import Footer from '../../molecules/footer'
import {AppContext} from '../../../context'
import {useLatch} from '../../../hooks/useLatch'
import ExpandCollapse from '../../atoms/expandCollapse'
import LogoLink from '../../atoms/logoLink'
import Title from '../../molecules/title'
import ExtensionCta from '../../molecules/extensionCta'
import Love from '../../molecules/love'
// import Tutorial from '../../atoms/tutorial' // removing this at request of nelson


const Banner = () => {
	const {width, settings} = useContext(AppContext)
	const {collapse, menu, title, target} = settings
	const [expanded, setExpanded] = useState(!collapse)
	const interacted = useLatch(expanded)
	const onToggle = (share: boolean) => !interacted && share ? setExpanded(share) : null
	useEffect(() => setExpanded(!settings.collapse), [settings.collapse]) // hydrate on settings change
	return (
		<Container
			theme={settings.theme}
		>
			<HeaderWrapper
				style={{
					padding: width > BREAKPOINT ? '8px 32px' : '8px 16px'
				}}
			>
				<Header
					collapse={collapse}
				>
					{ settings.branding ? <LogoLink /> : <div /> }
					{
						!expanded && width > 650 && (
							<Item
								style={{backgroundColor: settings.theme === Themes.LIGHT ? menu ? COLORS.white : COLORS.grey1 : COLORS.grey6}}
								theme={settings.theme}
							>
								<Control
									onToggle={onToggle}
									info
								/>
							</Item>
						)
					}
					{
						!expanded && width > 1000 && (
							<Item
								theme={settings.theme}
							>
								<Connections/>
							</Item>
						)
					}
					<HeaderRight>
						{ menu && <Menu /> }
						{
							settings.collapse && (
								<ExpandCollapse
									expanded={expanded}
									setExpanded={setExpanded}
								/>
							)
						}
					</HeaderRight>
				</Header>
				{
					!expanded && width <= 650 && (
						<Item
							style={{backgroundColor: settings.theme === Themes.LIGHT ? menu ? COLORS.white : COLORS.grey1 : COLORS.grey6}}
							theme={settings.theme}
						>
							<Control
								info
								onToggle={onToggle}
							/>
						</Item>
					)
				}
			</HeaderWrapper>
			{
				expanded && (
					<BodyWrapper
						style={{
							padding: width > BREAKPOINT ? '24px 32px' : settings.globe ? '0 16px 24px' : '24px 16px'
						}}
					>
						<Body
							mobile={width < BREAKPOINT}
						>
							{
								settings.globe && (
									<Col>
										<SafeGlobe target={settings.target}/>
									</Col>
								)
							}
							<Col>
								{
									title && (
										<div
											style={{marginBottom: 16}}
										>
											<Title/>
										</div>
									)
								}
								<div
									style={{marginBottom: 16}}
								>
									<About
										style={{
											fontSize: 14,
											lineHeight: `28px`
										}}
									/>
								</div>
								<Row
									borderTop
									borderBottom
									backgroundColor={settings.theme === Themes.DARK ? COLORS.grey6 : menu ? COLORS.white : COLORS.grey1}
								>
									<>
										<Control
											onToggle={onToggle}
										/>
										{/*{(!menu && !collapse) && (*/}
										{/*	<Tutorial />*/}
										{/*)}*/}
									</>
								</Row>
								<UnsupportedNote/>
								<Stats/>
								{
									!menu && (target === Targets.WEB) && (
										<ExtensionCta/>
									)
								}
								<div style={{marginTop: 16}}>
									<Love/>
								</div>
								<div
									style={{width: '100%', height: !title && width > BREAKPOINT ? 80 : 24}}
								/>
								{/*<div*/}
								{/*	style={{paddingLeft: 8, paddingRight: 8}}*/}
								{/*>*/}
								{/*<Footer*/}
								{/*	social={true}*/}
								{/*	donate={settings.donate}*/}
								{/*/>*/}
								{/*</div>*/}
							</Col>
						</Body>
					</BodyWrapper>
				)
			}
		</Container>
	)
}

export default Banner