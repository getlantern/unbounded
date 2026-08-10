import {defaultReport, FreezeReport, FreezeWatchdog} from './freezeWatchdog'

// These mirror the constants in freezeWatchdog.ts. Duplicated rather than
// exported: the thresholds are an implementation choice, and a test that imported
// them would pass even if freezeMs were set to a value that never fires.
const TICK_MS = 2000
const FREEZE_MS = 8000
const DEAD_TAB_MS = 5 * 60 * 1000

// Advancing time is the whole point of these tests, so the clock is driven
// explicitly rather than with jest's fake timers alone — the watchdog compares
// Date.now() against its own last tick, and jest's timer mocks move the timer
// queue without moving Date.now() unless told to.
let now = 1_700_000_000_000

const setNow = (t: number) => {
	now = t
}
const advance = (ms: number) => {
	now += ms
}

// fireTick runs the interval callback exactly once after moving the wall clock by
// `gapMs`, simulating a tick that came due and fired late — which is what a
// blocked event loop produces.
//
// Timer time advances by only TICK_MS regardless of gapMs, deliberately: browsers
// do not replay intervals missed during a block, they fire once and resume.
// Advancing timer time by the full gap would replay them and test behavior no
// browser produces.
const fireTick = (gapMs: number) => {
	advance(gapMs)
	jest.advanceTimersByTime(TICK_MS)
}

let visibility: DocumentVisibilityState = 'visible'

beforeEach(() => {
	jest.useFakeTimers()
	jest.spyOn(Date, 'now').mockImplementation(() => now)
	setNow(1_700_000_000_000)
	visibility = 'visible'
	Object.defineProperty(document, 'visibilityState', {
		configurable: true,
		get: () => visibility,
	})
	window.localStorage.clear()
})

afterEach(() => {
	jest.useRealTimers()
	jest.restoreAllMocks()
	window.localStorage.clear()
})

const capture = () => {
	const reports: FreezeReport[] = []
	return {reports, onReport: (r: FreezeReport) => reports.push(r)}
}

// stalled returns a liveness() whose heartbeat stopped at the moment it was built.
//
// Most tests below need one because a live verdict now requires Go to have stalled
// too. That is not incidental scaffolding: Go/wasm and JS share a thread, so a
// starved timer beside a *current* heartbeat proves the thread was alive and merely
// unscheduled — a throttled timer, not a freeze. Tests that used a bare JS gap were
// asserting on the most common false positive in production.
// sharedThread models the real relationship rather than a permanent wedge: Go's
// heartbeat advances whenever the thread runs, so it is stale by exactly the gap
// the JS timer just observed. A block stalls both and a recovery clears both.
//
// Use this for transient blocks. stalled() below never recovers, which is a wedged
// runtime — a different failure, and using it for a transient block makes calm
// intervals report go_scheduler_wedged, correctly.
const sharedThread = () => {
	let lastRan = now
	return () => {
		const wasAt = lastRan
		// Reading this means our timer fired, so the thread is running now and Go's
		// ticker runs with it.
		lastRan = now
		return {
			goTicks: 42,
			goLastTickMs: wasAt,
			goStartedMs: wasAt - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}
	}
}

const stalled = () => {
	const stoppedAt = now
	return () => ({
		goTicks: 42,
		goLastTickMs: stoppedAt,
		goStartedMs: stoppedAt - 60_000,
		goIntervalMs: 1000,
		goNowMs: stoppedAt,
	})
}

// A healthy page must stay silent. This is the test that matters most in
// practice: a watchdog that cries freeze during normal operation gets muted, and
// then reports nothing when a real freeze happens.
test('stays silent while ticks and Go heartbeat are both on time', () => {
	const {reports, onReport} = capture()
	let goTicks = 0
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: ++goTicks,
			goLastTickMs: now,
			goStartedMs: now - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	for (let i = 0; i < 20; i++) fireTick(TICK_MS)

	expect(reports).toEqual([])
	wd.stop()
})

// Both sides starve together when the shared thread is blocked, which is the
// second freeze observed on 2026-08-03 — engine alive in netstate, page frozen.
test('classifies a stalled Go heartbeat plus a starved timer as main_thread_blocked', () => {
	const {reports, onReport} = capture()
	const frozenAt = now
	const wd = new FreezeWatchdog({
		// Go's last tick stops advancing, as it must when the thread it shares is
		// blocked.
		liveness: () => ({
			goTicks: 5,
			goLastTickMs: frozenAt,
			goStartedMs: frozenAt - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	expect(reports).toHaveLength(1)
	expect(reports[0].kind).toBe('main_thread_blocked')
	expect(reports[0].gapMs).toBeGreaterThan(FREEZE_MS)
	expect(reports[0].goStaleMs).toBeGreaterThan(FREEZE_MS)
	// Every live detection is by definition survived — we had to run again to
	// notice. Only a breadcrumb can report an unrecovered freeze.
	expect(reports[0].recovered).toBe(true)
	wd.stop()
})

// The crisp case, and the only reason the Go heartbeat exists: our timer is
// perfectly on schedule, so the thread is fine and the Go scheduler is not. An
// earlier version of this file gated Go staleness on our own gap, which made this
// classification unreachable.
test('classifies a stalled Go heartbeat with punctual ticks as go_scheduler_wedged', () => {
	const {reports, onReport} = capture()
	const frozenAt = now
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: 5,
			goLastTickMs: frozenAt,
			goStartedMs: frozenAt - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	// Punctual ticks throughout: no gap ever exceeds FREEZE_MS.
	for (let i = 0; i < 8; i++) fireTick(TICK_MS)

	expect(reports.length).toBeGreaterThan(0)
	expect(reports[0].kind).toBe('go_scheduler_wedged')
	expect(reports[0].gapMs).toBeLessThanOrEqual(TICK_MS)
	wd.stop()
})

// When the thread frees up after a block, our timer callback can run before Go's
// ticker goroutine is rescheduled, so Go's heartbeat still looks stale while
// actually being fine. Reporting that would relabel every main-thread block as
// also being a Go wedge moments later — collapsing exactly the two failure modes
// this watchdog exists to separate.
test('does not blame Go on the punctual tick right after a block', () => {
	const {reports, onReport} = capture()
	// Go's ticker cannot run while the thread is blocked, so its last tick stays
	// pinned at stuckAt. Once the thread frees up it resumes and tracks the clock,
	// which is what stuckAt = null models.
	let stuckAt: number | null = now
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: 7,
			goLastTickMs: stuckAt ?? now,
			goStartedMs: now - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)
	expect(reports.map(r => r.kind)).toEqual(['main_thread_blocked'])

	// The thread recovers, but Go's heartbeat is momentarily still stale on our
	// next tick: its goroutine has not been rescheduled yet. This single tick is
	// the false positive being guarded against.
	fireTick(TICK_MS)
	// Now Go's ticker is running again.
	stuckAt = null

	for (let i = 0; i < 10; i++) fireTick(TICK_MS)

	expect(reports.map(r => r.kind)).toEqual(['main_thread_blocked'])
	wd.stop()
})

// Our timer was starved while Go kept ticking: not a full block, something
// monopolized the task queue ahead of us.
// The single most important case, and the one this watchdog got wrong in
// production: our timer starved while Go's heartbeat stayed current.
//
// That combination cannot be a freeze. The two share a thread, so a block deep
// enough to miss our tick stalls Go's heartbeat with it — a current heartbeat is
// therefore positive evidence the thread was running and simply not scheduling us,
// which is what browsers do to background and offscreen documents.
//
// It used to report 'js_starved' here. In the field that was 16 of the first 29
// reports, every one from an offscreen document being throttled.
test('does not report a starved timer while Go keeps ticking', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({
		// goLastTickMs tracks the current clock, i.e. Go never stopped.
		liveness: () => ({
			goTicks: 99,
			goLastTickMs: now,
			goStartedMs: now - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	expect(reports).toEqual([])

	// Even at throttling scale — 15 minutes is typical for an offscreen document.
	fireTick(15 * 60 * 1000)
	expect(reports).toEqual([])
	wd.stop()
})

// The third false-positive class, and the one no clock comparison can catch. When a
// laptop sleeps or Chrome parks a backgrounded page, our timer and Go's heartbeat
// stop together and resume together — which is precisely the fingerprint of a real
// main-thread block. Only the magnitude separates them.
//
// Modeled on the field data: 48 of 59 sampled reports were 13-17 minute gaps with
// gap and goStale within ~1.5s of each other, against 11 real blocks of 10-45s.
test('does not report a gap that parked the whole context', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	// 15 minutes, the median of the suspended cluster. sharedThread stalls Go by the
	// same gap, so this is indistinguishable from a block except by size.
	fireTick(15 * 60 * 1000)

	expect(reports).toEqual([])
	wd.stop()
})

// ...and the ceiling must not swallow a real block, which is the whole point of
// putting it in the empty band between the two populations rather than near either.
test('still reports a block short enough to be one', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	// 45s: the longest real block observed in the field, well under the 2min ceiling.
	fireTick(45_000)

	expect(reports).toHaveLength(1)
	expect(reports[0].kind).toBe('main_thread_blocked')
	wd.stop()
})

// Chrome fires 'freeze' when it parks a backgrounded page outright. Such a page runs
// nothing at all, so the gap is meaningless — but by the time we tick again it is
// thawed and looks entirely normal, which is why the event has to be latched.
test('does not report a gap spanning a Page Lifecycle freeze', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	document.dispatchEvent(new Event('freeze'))
	// Deliberately under maxBlockMs, so this proves the freeze gate rather than the
	// size ceiling — the two guards are independent and browsers without Page
	// Lifecycle rely on the ceiling alone.
	fireTick(30_000)

	expect(reports).toEqual([])
	wd.stop()
})

// CodeRabbit's finding on #411: onResume resets the timing baseline, so the next gap
// no longer spans the freeze — but the latch stayed set and suppressed that interval
// anyway. The tick right after a thaw is a plausible place for a real block, since a
// resuming page often has catch-up work.
test('reports a block in the first interval after a resume', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	document.dispatchEvent(new Event('freeze'))
	advance(10 * 60 * 1000) // frozen for ten minutes
	document.dispatchEvent(new Event('resume'))

	// A genuine block, measured entirely after the resume reset the baseline.
	fireTick(20_000)

	expect(reports).toHaveLength(1)
	expect(reports[0].kind).toBe('main_thread_blocked')
	// And the gap is the post-resume interval, not the frozen stretch.
	expect(reports[0].gapMs).toBeLessThan(60_000)
	wd.stop()
})

// Copilot's finding on the same PR, and the same root cause seen from the other side:
// the latch was cleared only in tick(), so a re-arm between a freeze and the next tick
// carried it into the following interval. armTimer() already reset the sibling latch.
//
// Driven through pagehide/pageshow on ONE instance, which is the path that actually
// occurs: the watchdog is a per-page singleton, so a fresh object cannot carry stale
// state and a test that built one would pass no matter what the code did.
test('does not carry the freeze latch across a re-arm', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	// Frozen, then cached and restored before any tick consumes the latch.
	document.dispatchEvent(new Event('freeze'))
	window.dispatchEvent(new Event('pagehide'))
	advance(10 * 60 * 1000)
	window.dispatchEvent(new Event('pageshow'))

	fireTick(20_000)

	expect(reports).toHaveLength(1)
	expect(reports[0].kind).toBe('main_thread_blocked')
	wd.stop()
})

// Background tabs have their timers throttled to roughly one per minute, so a
// hidden tab produces gaps far beyond FREEZE_MS while being perfectly healthy.
// Reporting those would make the signal useless — most donor tabs sit in the
// background, which is the entire point of the widget.
test('does not report a gap produced while the tab was hidden', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({onReport})
	wd.start()

	visibility = 'hidden'
	document.dispatchEvent(new Event('visibilitychange'))
	fireTick(90_000)

	expect(reports).toEqual([])
	wd.stop()
})

// The gate has to survive a full hide/show cycle between two ticks. Checking
// visibilityState at tick time alone would miss this: the tab is visible again by
// the time we look, but the gap it produced was throttling, not a freeze.
test('does not report a gap spanning a hide/show cycle', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({onReport})
	wd.start()

	visibility = 'hidden'
	document.dispatchEvent(new Event('visibilitychange'))
	advance(90_000)
	visibility = 'visible'
	document.dispatchEvent(new Event('visibilitychange'))
	fireTick(TICK_MS)

	expect(reports).toEqual([])

	// ...and the very next real freeze is still caught, i.e. the gate suppresses
	// one interval rather than latching off. A real freeze means Go stalled too.
	wd.stop()

	const second = capture()
	const wd2 = new FreezeWatchdog({liveness: stalled(), onReport: second.onReport})
	wd2.start()
	fireTick(FREEZE_MS + 4000)
	expect(second.reports).toHaveLength(1)
	expect(second.reports[0].kind).toBe('main_thread_blocked')
	wd2.stop()
})

// Without a heartbeat there is nothing to corroborate a gap with, so a gap is
// uninterpretable: a blocked thread and a throttled timer look identical. Staying
// silent is the honest answer, and guessing is what produced the production noise.
//
// This used to report 'js_starved', justified by the deployed widget.wasm predating
// the heartbeat. That justification has expired — the wasm was republished with it
// on 2026-08-07 and every field report since carries go_stale_ms. page_died still
// works without liveness, and it is the severe case.
test('stays silent on a gap it cannot corroborate', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({onReport})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	expect(reports).toEqual([])
	wd.stop()
})

// A wedged Go runtime can make the boundary call itself throw, and a watchdog
// that dies on the failure it is watching for is worse than none.
test('survives liveness() throwing', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({
		liveness: () => {
			throw new Error('wasm call failed')
		},
		onReport,
	})
	wd.start()

	// The guarantee is that the watchdog survives; a throwing boundary yields no
	// snapshot, so like any uncorroborated gap it produces no verdict.
	expect(() => fireTick(FREEZE_MS + 4000)).not.toThrow()
	expect(reports).toEqual([])
	wd.stop()
})

// A malformed liveness object must not be treated as data. Reading a missing
// field yields NaN, and NaN comparisons are all false — so a shape change in the
// Go snapshot would silently disable detection rather than fail loudly.
test('ignores a liveness object missing required fields', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({
		liveness: () => ({goTicks: 1} as any),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	// Treated as no snapshot at all rather than as a stalled runtime, so there is
	// nothing to corroborate the gap and nothing is reported. The failure mode this
	// guards against is the opposite one: reading NaN as a stalled heartbeat would
	// turn every shape change into a flood of invented freezes.
	expect(reports).toEqual([])
	wd.stop()
})

// goNowMs feeds only the diagnostic clockSkewMs, so losing it must not cost us the
// primary staleness signal — and must not leak NaN into a report, where it would
// look like a measurement rather than an absence.
test('degrades a missing goNowMs without discarding the staleness signal', () => {
	const {reports, onReport} = capture()
	const stuckAt = now
	const wd = new FreezeWatchdog({
		liveness: () =>
			({
				goTicks: 5,
				goLastTickMs: stuckAt,
				goStartedMs: stuckAt - 60_000,
				goIntervalMs: 1000,
				// goNowMs deliberately absent
			} as any),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	expect(reports).toHaveLength(1)
	// The primary signal survives, so the classification is still correct...
	expect(reports[0].kind).toBe('main_thread_blocked')
	expect(reports[0].goStaleMs).toBeGreaterThan(FREEZE_MS)
	// ...and the diagnostic one is absent rather than NaN.
	expect(reports[0].clockSkewMs).toBeNull()
	wd.stop()
})

// NaN and the infinities are numbers by typeof but poison every comparison exactly
// like a missing field would, so typeof alone is not a sufficient guard.
test('rejects non-finite numbers in a liveness snapshot', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: NaN,
			goLastTickMs: Infinity,
			goStartedMs: 0,
			goIntervalMs: 1000,
			goNowMs: NaN,
		}),
		onReport,
	})
	wd.start()

	fireTick(FREEZE_MS + 4000)

	// Treated as no snapshot at all, not as a stalled Go runtime — so no verdict.
	// Reading Infinity as a stalled heartbeat would manufacture freezes out of a
	// malformed field.
	expect(reports).toEqual([])
	wd.stop()
})

// A wedged Go runtime is a standing condition, not an event: it matches on every
// single tick. Unthrottled that is one report every TICK_MS, which fills the
// retained-report ring in under three minutes and evicts the 'page_died' record
// recovered at startup — the only evidence of a freeze nobody survived.
test('throttles a standing condition instead of reporting it every tick', () => {
	const {reports, onReport} = capture()
	const frozenAt = now
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: 5,
			goLastTickMs: frozenAt,
			goStartedMs: frozenAt - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	// Five minutes of punctual ticks with Go wedged the whole time.
	const ticks = 150
	for (let i = 0; i < ticks; i++) fireTick(TICK_MS)

	expect(reports.every(r => r.kind === 'go_scheduler_wedged')).toBe(true)
	// One per suppression window, give or take a boundary — emphatically not one
	// per tick.
	expect(reports.length).toBeLessThanOrEqual(6)
	expect(reports.length).toBeGreaterThanOrEqual(2)
	expect(reports.length).toBeLessThan(ticks / 10)
	wd.stop()
})

// Throttling is per-kind: a new failure surfacing while another is standing is
// news, and muting it would hide a main-thread block behind an ongoing Go wedge.
test('does not let a throttled kind suppress a different kind', () => {
	const {reports, onReport} = capture()
	// Already well past the threshold at start, so the verdict lands as soon as the
	// punctual-tick evidence accumulates rather than waiting for staleness to grow.
	const frozenAt = now - 20_000
	const wd = new FreezeWatchdog({
		liveness: () => ({
			goTicks: 5,
			goLastTickMs: frozenAt,
			goStartedMs: frozenAt - 60_000,
			goIntervalMs: 1000,
			goNowMs: now,
		}),
		onReport,
	})
	wd.start()

	// Two punctual ticks to earn the Go verdict (see minPunctualTicks).
	fireTick(TICK_MS)
	fireTick(TICK_MS)
	expect(reports.map(r => r.kind)).toEqual(['go_scheduler_wedged'])

	// Now our timer starves too, well inside the suppression window.
	fireTick(FREEZE_MS + 1000)
	expect(reports.map(r => r.kind)).toEqual(['go_scheduler_wedged', 'main_thread_blocked'])
	wd.stop()
})

// Distinct gaps separated by more than the suppression window are distinct
// episodes, and collapsing them would hide that a page is freezing repeatedly.
test('reports separate episodes separated by more than the suppression window', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: sharedThread(), onReport})
	wd.start()

	fireTick(FREEZE_MS + 1000)
	for (let i = 0; i < 40; i++) fireTick(TICK_MS) // 80s of calm
	fireTick(FREEZE_MS + 1000)

	expect(reports.map(r => r.kind)).toEqual(['main_thread_blocked', 'main_thread_blocked'])
	wd.stop()
})

describe('breadcrumb recovery', () => {
	const KEY = 'unbounded.watchdog.deadtab'

	const writeCrumb = (crumb: Record<string, unknown>) =>
		window.localStorage.setItem(KEY, JSON.stringify(crumb))

	// The only detector for a freeze that never ended. A page that stays frozen
	// cannot report anything, so the evidence has to be read back on the next load.
	// This also catches renderer crashes and OS kills, which is how the iOS jetsam
	// kills behind eng#3698 would present.
	test('reports a tab that stopped beating without firing pagehide', () => {
		writeCrumb({
			t: 'deadtab',
			b: now - DEAD_TAB_MS - 60_000,
			s: now - DEAD_TAB_MS - 600_000,
			c: false,
			h: false,
			p: true,
			l: 31_000,
			n: 'three-globe',
		})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toHaveLength(1)
		expect(reports[0].kind).toBe('page_died')
		// The distinction that matters: a hiccup annoys a user, a death silently
		// removes a donor from the network.
		expect(reports[0].recovered).toBe(false)
		// The dead tab's own evidence, not this page life's. This is the entire
		// reason the breadcrumb carries them: they are the only account of what that
		// tab was doing when it stopped. Reading them from `this` instead yields null
		// attribution and sharing=false at startup, which silently discards the
		// explanation for the death being reported.
		expect(reports[0].longestTaskMs).toBe(31_000)
		expect(reports[0].longestTaskName).toBe('three-globe')
		expect(reports[0].sharing).toBe(true)
		// Reported once, then cleared, so reloading does not re-report it forever.
		expect(window.localStorage.getItem(KEY)).toBeNull()
		wd.stop()
	})

	// A record truncated mid-write or left by an older version can carry any type,
	// and a wrong type here reaches the console and the beacon body.
	test('does not trust the types of recovered evidence fields', () => {
		writeCrumb({
			t: 'deadtab',
			b: now - DEAD_TAB_MS - 60_000,
			s: now - DEAD_TAB_MS - 600_000,
			c: false,
			h: false,
			p: 'yes', // not a boolean
			l: 'thirty seconds', // not a number
			n: {evil: true}, // not a string
		})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toHaveLength(1)
		expect(reports[0].longestTaskMs).toBeNull()
		expect(reports[0].longestTaskName).toBeNull()
		// Not coerced: 'yes' is truthy, and reporting sharing=true off a non-boolean
		// would assert something about the dead tab that the record never said.
		expect(reports[0].sharing).toBe(false)
		wd.stop()
	})

	// The attribution string round-trips through storage and lands in a console line
	// and a beacon body, so it is bounded on the way back out.
	test('bounds a recovered long-task name', () => {
		writeCrumb({
			t: 'deadtab',
			b: now - DEAD_TAB_MS - 60_000,
			s: now - DEAD_TAB_MS - 600_000,
			c: false,
			h: false,
			p: false,
			l: 9000,
			n: 'x'.repeat(5000),
		})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toHaveLength(1)
		expect(reports[0].longestTaskName!.length).toBeLessThanOrEqual(128)
		wd.stop()
	})

	// pagehide is the clean-exit flag precisely so ordinary navigation is not
	// mistaken for a death. If this leaked, every reload would report a freeze.
	test('ignores a tab that exited cleanly', () => {
		writeCrumb({t: 'deadtab', b: now - DEAD_TAB_MS - 60_000, s: now - 600_000, c: true, h: false, p: false, l: null, n: null})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toEqual([])
		expect(window.localStorage.getItem(KEY)).toBeNull()
		wd.stop()
	})

	// A live tab in another window that has been backgrounded beats only about once
	// a minute. Its record is stale but it is not dead, and reporting it would
	// manufacture freezes out of healthy background donors.
	test('leaves a recently-beating tab alone', () => {
		writeCrumb({t: 'deadtab', b: now - 60_000, s: now - 600_000, c: false, h: true, p: true, l: null, n: null})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toEqual([])
		// Still owned by that tab, so it must survive our sweep.
		expect(window.localStorage.getItem(KEY)).not.toBeNull()
		wd.stop()
	})

	// A record truncated by a quota failure mid-write, or left by an older version,
	// is unusable — and must not throw on the startup path.
	test('discards unparseable and malformed records without throwing', () => {
		window.localStorage.setItem('unbounded.watchdog.garbage', '{not json')
		window.localStorage.setItem('unbounded.watchdog.noBeat', JSON.stringify({t: 'x', c: false}))

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		expect(() => wd.start()).not.toThrow()

		expect(reports).toEqual([])
		expect(window.localStorage.getItem('unbounded.watchdog.garbage')).toBeNull()
		expect(window.localStorage.getItem('unbounded.watchdog.noBeat')).toBeNull()
		wd.stop()
	})

	// A week-old death is not news, and reporting it on every load would bury
	// current problems.
	// A breadcrumb far in the past is a machine that slept, not a tab that died, and
	// the two are indistinguishable from here. The first five real page_died reports
	// were 6.2 to 15.3 hours old — every one an overnight suspend, every one reported
	// as a casualty. Without a ceiling this kind means "someone shut their laptop".
	// The wall clock moves backwards on real machines — NTP corrections, VM
	// restores, dual-boot, a user setting it by hand — and this runs on other
	// people's computers. A future-dated record is the one case with no exit: a
	// negative age reads as a live tab so it is left in place, and it is also below
	// the retention window so it never expires. It would sit in an embedder's
	// localStorage forever.
	test('drops a future-dated breadcrumb instead of keeping it forever', () => {
		const future = now + 60 * 60 * 1000 // clock moved back an hour since the write
		window.localStorage.setItem(KEY, JSON.stringify({b: future, c: false}))

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toEqual([])
		expect(window.localStorage.getItem(KEY)).toBeNull()
		wd.stop()
	})

	test('ignores a breadcrumb too old to distinguish death from suspend', () => {
		const stale = now - 6 * 60 * 60 * 1000 // 6h, the shortest real false positive
		window.localStorage.setItem(KEY, JSON.stringify({b: stale, c: false}))

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toEqual([])
		// Dropped rather than kept: no later load will find it any more decipherable,
		// and leaving it would re-litigate the same undecidable record every startup.
		expect(window.localStorage.getItem(KEY)).toBeNull()
		wd.stop()
	})

	// The ceiling must not swallow the case the kind exists for.
	test('still reports a death recent enough to be a crash', () => {
		const stale = now - 10 * 60 * 1000 // 10m: past deadTabMs, well inside the ceiling
		window.localStorage.setItem(KEY, JSON.stringify({b: stale, c: false}))

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toHaveLength(1)
		expect(reports[0].kind).toBe('page_died')
		expect(reports[0].recovered).toBe(false)
		wd.stop()
	})

	test('expires records older than the retention window', () => {
		writeCrumb({t: 'deadtab', b: now - 48 * 60 * 60 * 1000, s: now - 49 * 60 * 60 * 1000, c: false, h: false, p: false, l: null, n: null})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toEqual([])
		expect(window.localStorage.getItem(KEY)).toBeNull()
		wd.stop()
	})

	// pagehide flags the exit so the next load does not report this navigation as a
	// death. This is the one write that must not be skipped.
	test('marks its own record clean on pagehide', () => {
		const wd = new FreezeWatchdog({})
		wd.start()
		const own = Object.keys(window.localStorage).find(k => k.startsWith('unbounded.watchdog.'))
		expect(own).toBeDefined()

		window.dispatchEvent(new Event('pagehide'))

		expect(JSON.parse(window.localStorage.getItem(own!)!).c).toBe(true)
		wd.stop()
	})

	// A tick running after pagehide would rewrite the record with c:false and
	// resurrect a cleanly-closed page as a casualty — turning every ordinary
	// navigation into a reported crash.
	test('does not let a later tick undo the clean flag', () => {
		const wd = new FreezeWatchdog({})
		wd.start()
		const own = Object.keys(window.localStorage).find(k => k.startsWith('unbounded.watchdog.'))!

		window.dispatchEvent(new Event('pagehide'))
		fireTick(TICK_MS)
		fireTick(TICK_MS)

		expect(JSON.parse(window.localStorage.getItem(own)!).c).toBe(true)
		wd.stop()
	})

	// A back/forward-cache restore makes the page a live donor again. Leaving it
	// stopped would keep pagehide's clean flag in place forever, so a later kill
	// would go unreported — trading a false positive for a false negative on the one
	// case this path exists to catch.
	test('resumes beating after a back/forward-cache restore', () => {
		const wd = new FreezeWatchdog({})
		wd.start()
		const own = Object.keys(window.localStorage).find(k => k.startsWith('unbounded.watchdog.'))!

		window.dispatchEvent(new Event('pagehide'))
		expect(JSON.parse(window.localStorage.getItem(own)!).c).toBe(true)

		// Frozen in the cache for ten minutes, then restored.
		advance(10 * 60 * 1000)
		window.dispatchEvent(new Event('pageshow'))

		// Beating again, and no longer flagged as cleanly exited.
		expect(JSON.parse(window.localStorage.getItem(own)!).c).toBe(false)
		const beforeTick = JSON.parse(window.localStorage.getItem(own)!).b
		fireTick(TICK_MS)
		expect(JSON.parse(window.localStorage.getItem(own)!).b).toBeGreaterThan(beforeTick)
		wd.stop()
	})

	// Ten minutes of wall clock passed while the page sat frozen in the cache, but
	// nothing was wrong. An un-reset baseline would turn that whole interval into a
	// fabricated freeze the instant the page came back.
	test('does not report a freeze for time spent in the back/forward cache', () => {
		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		window.dispatchEvent(new Event('pagehide'))
		advance(10 * 60 * 1000)
		window.dispatchEvent(new Event('pageshow'))

		for (let i = 0; i < 5; i++) fireTick(TICK_MS)

		expect(reports).toEqual([])
		wd.stop()
	})

	// Of the two ways to be wrong about a malformed record, inventing a casualty is
	// far cheaper than silently dropping one — so the clean-exit check is strict.
	// The string "false" is truthy, and treating it as clean would suppress a real
	// death report.
	test('does not treat a truthy non-boolean clean flag as a clean exit', () => {
		writeCrumb({
			t: 'deadtab',
			b: now - DEAD_TAB_MS - 60_000,
			s: now - DEAD_TAB_MS - 600_000,
			c: 'false',
			h: 'false',
			p: 'false',
			l: null,
			n: null,
		})

		const {reports, onReport} = capture()
		const wd = new FreezeWatchdog({onReport})
		wd.start()

		expect(reports).toHaveLength(1)
		expect(reports[0].kind).toBe('page_died')
		// And the same strictness applies to the report fields, so a non-boolean does
		// not mislabel the report it is meant to explain.
		expect(reports[0].hidden).toBe(false)
		expect(reports[0].sharing).toBe(false)
		wd.stop()
	})

	// A watchdog that crashes the page it is watching would be worse than no
	// watchdog. Safari private browsing throws on the property access itself, not
	// just on setItem, so that is what is simulated here.
	test('runs with localStorage unavailable', () => {
		const real = Object.getOwnPropertyDescriptor(window, 'localStorage')
		Object.defineProperty(window, 'localStorage', {
			configurable: true,
			get() {
				throw new Error('SecurityError: localStorage is not available')
			},
		})

		try {
			const {reports, onReport} = capture()
			const wd = new FreezeWatchdog({liveness: stalled(), onReport})
			expect(() => wd.start()).not.toThrow()
			expect(() => fireTick(FREEZE_MS + 4000)).not.toThrow()

			// Live detection still works; only crash recovery is lost.
			expect(reports).toHaveLength(1)
			wd.stop()
		} finally {
			if (real) Object.defineProperty(window, 'localStorage', real)
		}
	})
})

// Reports are retained in memory for console inspection, so a page that freezes
// in a loop must not turn its own diagnostics into the leak.
test('bounds retained reports', () => {
	const wd = new FreezeWatchdog({})
	jest.spyOn(console, 'warn').mockImplementation(() => {})
	wd.start()

	for (let i = 0; i < 40; i++) fireTick(FREEZE_MS + 4000)

	expect(wd.reports.length).toBeLessThanOrEqual(20)
	wd.stop()
})

// start() is called from WasmInterface.initialize, which React's dev server can
// invoke more than once on hot reload — the same hazard the initializing flag
// there guards against.
test('start is idempotent', () => {
	const {reports, onReport} = capture()
	const wd = new FreezeWatchdog({liveness: stalled(), onReport})
	wd.start()
	wd.start()

	fireTick(FREEZE_MS + 4000)

	expect(reports).toHaveLength(1)
	wd.stop()
})

// defaultReport is the sink used when no onReport is supplied, which is every
// production install. Its beacon half had no coverage until the boolean below
// started being read.
describe('defaultReport beacon', () => {
	const realBeacon = navigator.sendBeacon
	const realUrl = process.env.REACT_APP_FREEZE_BEACON_URL
	let warn: jest.SpyInstance

	const report: FreezeReport = {
		kind: 'js_starved',
		detectedAt: now,
		recovered: true,
		gapMs: 9000,
		goStaleMs: null,
		goTicks: null,
		clockSkewMs: null,
		longestTaskMs: null,
		longestTaskName: null,
		hidden: false,
		sharing: false,
		url: 'https://example.test/',
		userAgent: 'test',
	}

	const setBeacon = (fn: unknown) =>
		Object.defineProperty(navigator, 'sendBeacon', {value: fn, configurable: true})

	beforeEach(() => {
		warn = jest.spyOn(console, 'warn').mockImplementation(() => {})
		process.env.REACT_APP_FREEZE_BEACON_URL = 'https://egress.test/freeze'
	})

	afterEach(() => {
		warn.mockRestore()
		setBeacon(realBeacon)
		if (realUrl === undefined) delete process.env.REACT_APP_FREEZE_BEACON_URL
		else process.env.REACT_APP_FREEZE_BEACON_URL = realUrl
	})

	// The report line always goes to the console; the beacon is the second sink.
	const beaconWarnings = () =>
		warn.mock.calls.filter(c => String(c[0]).includes('beacon not queued'))

	test('sends to the configured URL', () => {
		const sent: unknown[] = []
		setBeacon((url: string, body: string) => {
			sent.push([url, body])
			return true
		})

		defaultReport(report)

		expect(sent).toHaveLength(1)
		const [url, body] = sent[0] as [string, string]
		expect(url).toBe('https://egress.test/freeze')
		expect(JSON.parse(body).kind).toBe('js_starved')
		expect(beaconWarnings()).toHaveLength(0)
	})

	// false means the browser declined to queue at all — the only delivery failure
	// it will ever tell us about, so it must not be swallowed.
	test('warns when the browser declines to queue', () => {
		setBeacon(() => false)

		defaultReport(report)

		expect(beaconWarnings()).toHaveLength(1)
	})

	// Absent sendBeacon yields undefined through the optional call. That is "no
	// beacon support", not "refused", and must not be reported as a drop — the
	// same strict-comparison trap as the recovered breadcrumb flags.
	test('stays quiet when sendBeacon is unavailable', () => {
		setBeacon(undefined)

		expect(() => defaultReport(report)).not.toThrow()
		expect(beaconWarnings()).toHaveLength(0)
	})

	// Unset URL is the local-build case: still diagnosed, still logged, not sent.
	test('does not beacon when no URL is configured', () => {
		delete process.env.REACT_APP_FREEZE_BEACON_URL
		let called = false
		setBeacon(() => {
			called = true
			return true
		})

		defaultReport(report)

		expect(called).toBe(false)
		expect(beaconWarnings()).toHaveLength(0)
	})
})
