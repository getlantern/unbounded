import {Text} from './styles'
import {useContext, CSSProperties} from 'react'
import {AppContext} from '../../../context'
import {COLORS, Themes} from '../../../constants'
import {useTranslation} from 'react-i18next'

interface Props {
	style?: CSSProperties
}

const About = ({style = {}}: Props) => {
	const { t } = useTranslation();
	const {theme, infoLink} = useContext(AppContext).settings
	const color = theme === Themes.DARK ? COLORS.grey2 : COLORS.blue5
	return(
		<Text
			style={{color, margin: 0, ...style}}
		>
			{t('intro')}
			{/*<a style={{color: brand}} href={'https://lantern.io'} target={'_blank'} rel={'noreferrer'}>Lantern</a>.*/}
			{ !!infoLink.length && <span dangerouslySetInnerHTML={{__html: infoLink}} />}
		</Text>
	)
}

export default About