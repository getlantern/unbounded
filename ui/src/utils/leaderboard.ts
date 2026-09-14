const consentKey = 'unbounded.leaderboard.consent'
const donorKey = 'unbounded.leaderboard.installation'

export const leaderboardConsent = (): boolean => {
  try { return localStorage.getItem(consentKey) === 'yes' } catch { return false }
}

export const setLeaderboardConsent = (enabled: boolean): boolean => {
  try {
    localStorage.setItem(consentKey, enabled ? 'yes' : 'no')
    if (!enabled) localStorage.removeItem(donorKey)
    window.dispatchEvent(new Event('unbounded-leaderboard-consent'))
    return true
  } catch { return false }
}

export const leaderboardDonor = (): string => {
  if (!leaderboardConsent() || window.location.protocol !== 'https:') return ''
  try {
    let donor = localStorage.getItem(donorKey)
    if (!donor) {
      donor = crypto.randomUUID()
      localStorage.setItem(donorKey, donor)
    }
    return donor
  } catch { return '' }
}
