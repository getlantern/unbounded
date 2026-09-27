import {CSSProperties, useContext} from 'react'
import {useTranslation} from 'react-i18next'
import {Text} from '../../atoms/typography'
import {canShare} from '../../../utils/capabilities'
import {AppContext} from '../../../context'

// Rendered under the status toggle rather than inside it: the toggle's row has a
// fixed height and also appears in the compact collapsed bars. Without
// WebAssembly (e.g. iOS Lockdown Mode) the switch is disabled; this says why.
const UnsupportedNote = ({style}: {style?: CSSProperties}) => {
	const {t} = useTranslation()
	const {mock} = useContext(AppContext).settings
	if (canShare(mock)) return null
	return (
		<Text style={{fontSize: 12, lineHeight: '18px', padding: '8px 8px 0', ...style}}>
			{t('unsupportedBrowser')}
		</Text>
	)
}

export default UnsupportedNote
