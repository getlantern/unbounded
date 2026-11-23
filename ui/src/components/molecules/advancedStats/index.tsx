import {useContext} from 'react'
import {useTranslation} from 'react-i18next'
import {AppContext} from '../../../context'
import {Text} from '../../atoms/typography'
import {formatBytes, getIndex} from '../../../hooks/useBytesFormatLatch'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import useSample from '../../../hooks/useSample'
import {countries} from '../../../utils/countries'
import {
	countryStatsEmitter,
	downloadThroughputEmitter,
	lifetimeChunksEmitter,
	lifetimeUploadBytesEmitter,
	sessionBytesEmitter,
	sharingEmitter,
	uploadThroughputEmitter,
} from '../../../utils/wasmInterface'
import {Heading, Panel, StatRow, Value} from './styles'

const TOP_COUNTRIES = 5
const SAMPLE_MS = 1000

export const formatData = (bytes: number) => {
	if (bytes < 1000) return `${Math.round(bytes)} B`
	return formatBytes(bytes, getIndex(bytes)).toUpperCase()
}

const AdvancedStats = ({id}: {id: string}) => {
	const {t} = useTranslation()
	const {theme} = useContext(AppContext).settings
	const sharing = useEmitterState(sharingEmitter)
	// throughput events can still arrive while the client winds down, so a stopped widget shows zero
	const lifetimeChunks = useSample({emitter: lifetimeChunksEmitter, ms: SAMPLE_MS})
	const latestDown = useEmitterState(downloadThroughputEmitter)
	const latestUp = useEmitterState(uploadThroughputEmitter)
	const downSpeed = sharing ? latestDown : 0
	const upSpeed = sharing ? latestUp : 0
	const session = useEmitterState(sessionBytesEmitter)
	const lifetimeUp = useEmitterState(lifetimeUploadBytesEmitter)
	const countryStats = useEmitterState(countryStatsEmitter)

	const lifetimeDown = lifetimeChunks.reduce((total, chunk) => total + chunk.size, 0)
	const topCountries = [...countryStats]
		.sort((a, b) => (b.down + b.up) - (a.down + a.up))
		.slice(0, TOP_COUNTRIES)

	const countryName = (iso: string) => {
		const country = countries[iso as keyof typeof countries]
		if (!country) return t('unknownCountry')
		return <><span style={{marginRight: 6}} aria-hidden>{country.emojiFlag}</span>{country.name}</>
	}

	return (
		<Panel id={id}>
			<StatRow theme={theme}>
				<Text style={{fontSize: 12}}>{t('currentSpeed')}</Text>
				<Text style={{fontSize: 12}}>
					<Value>↓ {formatData(downSpeed)}/s · ↑ {formatData(upSpeed)}/s</Value>
				</Text>
			</StatRow>
			<StatRow theme={theme}>
				<Text style={{fontSize: 12}}>{t('sessionData')}</Text>
				<Text style={{fontSize: 12}}><Value>{formatData(session.down + session.up)}</Value></Text>
			</StatRow>
			<StatRow theme={theme}>
				<Text style={{fontSize: 12}}>{t('lifetimeData')}</Text>
				<Text style={{fontSize: 12}}><Value>{formatData(lifetimeDown + lifetimeUp)}</Value></Text>
			</StatRow>
			{
				!!topCountries.length && (
					<>
						<Heading theme={theme}>{t('countriesHelped')}</Heading>
						{
							topCountries.map(stat => (
								<StatRow theme={theme} key={stat.iso || 'unknown'}>
									<Text style={{fontSize: 12}}>{countryName(stat.iso)}</Text>
									<Text style={{fontSize: 12}}>
										<Value>
											{t('countryData', {data: formatData(stat.down + stat.up), count: stat.people})}
										</Value>
									</Text>
								</StatRow>
							))
						}
					</>
				)
			}
		</Panel>
	)
}

export default AdvancedStats
