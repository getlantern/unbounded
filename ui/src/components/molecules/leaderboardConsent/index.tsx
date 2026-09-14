import {useContext, useEffect, useState} from 'react'
import {Themes} from '../../../constants'
import {AppContext} from '../../../context'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import {sharingEmitter} from '../../../utils/wasmInterface'
import {leaderboardConsent, setLeaderboardConsent} from '../../../utils/leaderboard'

export default function LeaderboardConsent() {
  const [enabled, setEnabled] = useState(leaderboardConsent)
  const [error, setError] = useState('')
  const sharing = useEmitterState(sharingEmitter)
  const {wasmInterface, settings} = useContext(AppContext)
  useEffect(() => {
    const sync = () => {
      const consent = leaderboardConsent()
      setEnabled(consent)
      if (enabled && !consent && sharing) void wasmInterface?.stop()
    }
    window.addEventListener('storage', sync)
    window.addEventListener('unbounded-leaderboard-consent', sync)
    return () => {
      window.removeEventListener('storage', sync)
      window.removeEventListener('unbounded-leaderboard-consent', sync)
    }
  }, [enabled, sharing, wasmInterface])
  return <div style={{padding: '12px 16px', fontSize: 12, lineHeight: '18px', borderTop: '1px solid #bfbfbf', color: settings.theme === Themes.DARK ? '#f8fafb' : '#3e464e'}}>
    <label style={{display: 'block'}}>
      <input type="checkbox" checked={enabled} disabled={sharing} onChange={e => {
        if (setLeaderboardConsent(e.target.checked)) { setEnabled(e.target.checked); setError('') }
        else setError('Browser storage is unavailable; your contribution remains unlisted.')
      }} /> Include my anonymous contribution in this website’s team totals.
    </label>
    <small>Uses an identifier saved on this website. Only team totals are public. Stop sharing to change this choice.</small>
    {error && <p role="alert">{error}</p>}
  </div>
}
