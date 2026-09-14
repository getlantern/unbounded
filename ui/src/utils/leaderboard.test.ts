import {leaderboardConsent, leaderboardDonor, setLeaderboardConsent} from './leaderboard'

beforeEach(() => localStorage.clear())

test('does not create an installation identifier without consent', () => {
  expect(leaderboardConsent()).toBe(false)
  expect(leaderboardDonor()).toBe('')
  expect(localStorage.getItem('unbounded.leaderboard.installation')).toBeNull()
})

test('withdrawal removes the saved identifier', () => {
  expect(setLeaderboardConsent(true)).toBe(true)
  expect(leaderboardConsent()).toBe(true)
  localStorage.setItem('unbounded.leaderboard.installation', 'previous-installation')
  expect(setLeaderboardConsent(false)).toBe(true)
  expect(leaderboardDonor()).toBe('')
  expect(localStorage.getItem('unbounded.leaderboard.installation')).toBeNull()
})

test('storage failure defaults to no attribution', () => {
  const get = jest.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  expect(leaderboardConsent()).toBe(false)
  expect(leaderboardDonor()).toBe('')
  get.mockRestore()
})

test('consenting HTTPS visitors reuse a random installation identifier', () => {
  const location = window.location
  const crypto = window.crypto
  Object.defineProperty(window, 'location', {configurable: true, value: new URL('https://example.org')})
  Object.defineProperty(window, 'crypto', {configurable: true, value: {randomUUID: () => '591e0499-22d8-4415-aa2c-cce49f37b106'}})
  try {
    setLeaderboardConsent(true)
    const first = leaderboardDonor()
    expect(first).toBe('591e0499-22d8-4415-aa2c-cce49f37b106')
    expect(leaderboardDonor()).toBe(first)
    setLeaderboardConsent(false)
    expect(leaderboardDonor()).toBe('')
  } finally {
    Object.defineProperty(window, 'location', {configurable: true, value: location})
    Object.defineProperty(window, 'crypto', {configurable: true, value: crypto})
  }
})
