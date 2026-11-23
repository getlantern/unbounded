import {CSSProperties, useContext, useMemo} from 'react'
import {useTranslation} from 'react-i18next'
import {AppContext} from '../../../context'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import {canShare} from '../../../utils/capabilities'
import {censoredCountryCodes, countries} from '../../../utils/countries'
import {ANYWHERE, storeSupportCountry, supportCountryEmitter} from '../../../utils/supportCountry'
import {supportCountryEnabledEmitter} from '../../../utils/featureFlags'
import {sendSupportCountry} from '../../../utils/wasmInterface'
import {Targets} from '../../../constants'
import {Select, Wrapper} from './styles'

// Localized country names where the browser can supply them, else the English names we ship.
const countryName = (code: string, displayNames?: Intl.DisplayNames) => {
	try {
		const name = displayNames?.of(code)
		if (name && name !== code) return name
	} catch {
		// unsupported code or locale
	}
	return (countries as Record<string, {name: string}>)[code]?.name || code
}

// Lets a volunteer prefer helping people from one censored country. Changing it while sharing
// needs no restart: the client applies it from each worker's next connection attempt.
const SupportCountry = ({style}: {style?: CSSProperties}) => {
	const {t, i18n} = useTranslation()
	const {theme, mock, target} = useContext(AppContext).settings
	const supportCountry = useEmitterState(supportCountryEmitter)
	const enabled = useEmitterState(supportCountryEnabledEmitter)

	const options = useMemo(() => {
		let displayNames: Intl.DisplayNames | undefined
		try {
			displayNames = new Intl.DisplayNames([i18n.language, 'en'], {type: 'region'})
		} catch {
			displayNames = undefined
		}
		return censoredCountryCodes
			.map(code => ({code, name: countryName(code, displayNames)}))
			.sort((a, b) => a.name.localeCompare(b.name, i18n.language))
	}, [i18n.language])

	if (!enabled || !canShare(mock)) return null

	return (
		<Wrapper theme={theme} style={style} title={t('supportCountryHint')}>
			{t('supportCountry')}
			<Select
				theme={theme}
				value={supportCountry}
				onChange={e => {
					storeSupportCountry(e.target.value)
					supportCountryEmitter.update(e.target.value)
					if (target === Targets.EXTENSION_POPUP) sendSupportCountry(e.target.value)
				}}
			>
				<option value={ANYWHERE}>{t('supportAnywhere')}</option>
				{options.map(({code, name}) => (
					<option key={code} value={code}>{name}</option>
				))}
			</Select>
		</Wrapper>
	)
}

export default SupportCountry
