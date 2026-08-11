// freezeWatchdog.ts is the page-side half of the freeze watchdog. The Go half
// (clientcore/watchdog_wasm_impl.go) only publishes a heartbeat; all detection
// lives here, because the failure this exists to catch is one Go cannot report.
//
// Both freezes observed on unbounded.lantern.io on 2026-08-03 were invisible in
// telemetry, and they were not the same failure:
//
//   - the first took the netstate vertex and all four of its edges away, i.e. the
//     engine stopped
//   - the second left the vertex current and all four edges intact while the page
//     was visually frozen, i.e. the engine was fine and the main thread was not
//
// Telling those apart is the whole job, and it is possible only from here: Go/wasm
// and JS share one thread but fail independently, so comparing a JS timer against
// Go's tick counter isolates which side stopped.
//
// The harder problem is that a page which freezes and stays frozen cannot report
// anything — no timer, no beacon, no console. So detection alone is not enough.
// Every tick also writes a breadcrumb to localStorage, and startup reads back
// records left by tabs that stopped beating without ever saying goodbye. That is
// the only path that catches a permanent freeze, a renderer crash, or an OS kill
// (the iOS jetsam kills behind eng#3698 look exactly like this).
//
// Deliberately not OpenTelemetry: the otel Go SDK was found to abuse the call
// stack in ways mobile Safari does not tolerate, so widget-side telemetry stays
// primitive on both sides of the boundary.
//
// Runs on other people's sites. The widget is embedded by third parties with a
// cross-origin script tag, not an iframe:
//
//	<script defer src="https://embed.lantern.io/static/js/main.js"></script>
//
// A cross-origin script executes in the *embedder's* origin, so everything below
// happens on their page, not ours. Two consequences worth knowing before changing
// any of it:
//
//   - localStorage, window.__unboundedWatchdog and console output all belong to the
//     embedding site. Breadcrumbs therefore recover per-site, which is correct — a
//     freeze on site X is evidence about site X — but it also means this writes keys
//     into storage we do not own. Hence the narrow prefix, the 24h expiry, the cap
//     on retained records, and deleting each record as it is read: the footprint on
//     a host we are a guest on should stay small and self-cleaning.
//   - FreezeReport carries `url`, which on an embedding site is that site's URL.
//     REACT_APP_FREEZE_BEACON_URL is now set in production, so we do receive the
//     addresses of pages that embed the widget. That was a deliberate decision about
//     third-party data rather than an incidental config change: the URL is the field
//     that says *which page froze*, which is what makes a report actionable at all,
//     and it is the same field that tells us who embeds the widget. Both follow from
//     collecting it; neither is an accident.
//
//     What bounds it: the egress truncates the URL before logging, never promotes it
//     to a metric label (only `kind` becomes one), and throttles the log. So the URL
//     lands in a DEBUG line on the egress host, not in a queryable index of embedders.
//     Anyone widening that — a url label, a longer retention — is making the
//     third-party-data decision again, and should say so.

// tickMs is how often the watchdog samples. Frequent enough that an 8s freeze is
// caught by a wide margin, cheap enough to be irrelevant: one Date.now(), one
// liveness() call, one small localStorage write.
const tickMs = 2000

// freezeMs is the gap between watchdog ticks that counts as a freeze while the
// tab is visible. Well above tickMs so ordinary scheduler jitter and GC pauses
// never qualify, and above the ~1s stalls a heavy WebGL frame can cause — the
// point is to catch what a user would call a freeze, not every long frame.
const freezeMs = 8000

// maxBlockMs is the longest gap still worth calling a block rather than a parked
// process, and it closes the last of the three false-positive classes this
// watchdog shipped with.
//
// The other two were asymmetric — one clock stopped and the other did not — so
// comparing them separated signal from noise. This one is symmetric: when a laptop
// sleeps, or Chrome freezes a backgrounded page, the whole context stops, so our
// timer and Go's heartbeat stall together and resume together. That is exactly the
// fingerprint of a genuine main-thread block, and no comparison between the two can
// tell them apart. Only the magnitude can.
//
// Measured over six hours, the split was clean and bimodal: 11 gaps between 10.5s
// and 45.2s, then 48 clustered at 13-17 minutes with gap and goStale within ~1.5s
// of each other. Nothing in between. The upper cluster is not a thread blocked for
// a quarter of an hour, which is not a thing that happens and recovers; it is a
// process that was not running.
//
// Two minutes sits in the empty band — 2.6x above the longest observed real block,
// 6x below the shortest observed suspension — deliberately not tight against either
// edge, because a threshold fitted to one day of data is how the donor-wedge alert
// ended up firing twice on ordinary variation.
//
// A page genuinely wedged for longer than this is missed. It is also, from here,
// indistinguishable from a closed lid.
const maxBlockMs = 2 * 60 * 1000

// deadTabMs is how stale a breadcrumb must be before startup calls that tab dead.
// Much larger than freezeMs on purpose: a *live* tab in another window that has
// been backgrounded is throttled to roughly one timer per minute, so anything
// near freezeMs would report healthy background tabs as casualties.
const deadTabMs = 5 * 60 * 1000

// maxDeadTabAgeMs is the upper bound on that window, and exists because the lower
// bound alone was wrong in production. The first five real page_died reports had
// breadcrumbs 6.2, 9.3, 11.0, 11.0 and 15.3 hours old — closed laptops and
// overnight suspends, reported as casualties every one.
//
// Past some age a stale breadcrumb stops carrying information. A tab that died and
// a machine that slept look identical from the next page load, and the longer the
// gap the more certainly it is the latter. Reporting them anyway does not merely
// add noise, it inverts the signal: the benign case is far more common, so
// page_died came to mean "someone shut their laptop".
//
// Half an hour keeps the case the kind exists for — a crash the user notices and
// reloads from — and drops the case we cannot distinguish. It does mean a genuine
// death nobody returns to for an hour is missed. That is the right trade here: a
// missed report costs one data point, while a false one costs trust in the metric,
// and this whole path exists because deaths were invisible *and worth finding*.
const maxDeadTabAgeMs = 30 * 60 * 1000

const storagePrefix = 'unbounded.watchdog.'
// Bounds on the breadcrumb sweep. Records are normally deleted as they are read,
// so these only matter when something goes wrong repeatedly and nobody reloads.
const storageTtlMs = 24 * 60 * 60 * 1000
const maxStoredTabs = 8

// maxTaskNameLen bounds the long-task attribution string. It originates from the
// DOM (a container's id or name) and survives a round trip through localStorage,
// so by the time it is read back it is just bytes on the origin — and it ends up in
// a console line and a beacon body. A real container name is a handful of
// characters; this only truncates something that was never a name.
const maxTaskNameLen = 128

// maxReports caps what we retain in memory for window.__unboundedWatchdog. A page
// that freezes in a loop must not turn its own diagnostics into the leak.
const maxReports = 20

// buildId is the commit the widget page was published from, baked in at build time by
// the publish workflow. Read once at module load rather than per report: it cannot
// change during a page life, and that is exactly the property that makes it useful.
//
// Trimmed and length-capped here as well as on the egress. The value is compiled into
// a world-readable bundle so it is not a secret, but it arrives at the server as
// peer-supplied bytes like everything else in a report, and bounding it at the source
// keeps an over-long value from being something only the server notices.
const buildId: string | null = (process.env.REACT_APP_BUILD || '').trim().slice(0, 64) || null

// repeatSuppressMs throttles repeats of a kind already reported.
//
// A wedged Go runtime is a *standing* condition, not an event: goLastTickMs stays
// stale forever while our own ticks remain punctual, so every single tick matches
// and would report. At tickMs that is one report every 2s, which fills maxReports
// in well under three minutes and evicts the 'page_died' record recovered at
// startup — the most valuable report we hold, and the only evidence of a freeze
// nobody survived. Suppression is per-kind so a *different* failure appearing
// during a standing one is still news and passes straight through.
const repeatSuppressMs = 60_000

// minPunctualTicks is how many consecutive on-time ticks must precede a
// 'go_scheduler_wedged' verdict.
//
// Blaming Go requires positive evidence that the thread is healthy, and the tick
// immediately after a block is not that evidence: when the thread frees up, our
// timer callback can run before Go's ticker goroutine is rescheduled, so Go looks
// stale while actually being fine. Without this guard every main-thread block is
// followed by a spurious Go-wedge report — precisely the confusion between the two
// failure modes that this watchdog exists to remove.
//
// Two ticks is 2 * tickMs of demonstrably healthy thread, several times Go's own
// heartbeat interval, so a heartbeat still stale by then really is wedged.
const minPunctualTicks = 2

// FreezeKind is what stopped, not merely that something did. These map onto the
// truth table in watchdog_wasm_impl.go, and they have different fixes — which is
// exactly why the original "the widget froze" reports were not actionable.
export type FreezeKind =
	// Our timer was starved and Go's tick stalled with it. Both starve together
	// when the shared thread is blocked, so this points at the page: a long task,
	// a synchronous layout, a WebGL stall.
	| 'main_thread_blocked'
	// Our timer fired on schedule and Go's tick did not advance. The thread was
	// fine, so the Go scheduler is wedged — a deadlock, or a goroutine looping
	// without yielding.
	| 'go_scheduler_wedged'
	// NO LONGER PRODUCED. Retained because it is a wire value: widgets built before
	// 2026-08-08 still send it and the egress still counts it, so removing it from
	// this union would only hide that.
	//
	// It meant "our timer was starved while Go kept ticking", and was specified as a
	// distinct failure. It is not one. Go/wasm and JS share a thread, so a block deep
	// enough to miss our tick also stalls Go's heartbeat — which makes this
	// combination proof the thread was *alive* and merely not scheduling us. There is
	// no row for it in the truth table in watchdog_wasm_impl.go, and there never
	// should have been a kind for it here.
	//
	// In production it was the most common report and meant nothing: 16 of the first
	// 29, all from an offscreen document whose timers the browser was throttling
	// while Go stayed current to within a second.
	| 'js_starved'
	// Reconstructed at startup from a breadcrumb: a previous page life stopped
	// beating and never fired pagehide. Unlike the three above, this one was never
	// survived — see `recovered`.
	//
	// Bounded at both ends: too recent and it is a live tab in another window, too
	// old and it is a machine that slept. See deadTabMs and maxDeadTabAgeMs.
	| 'page_died'

export interface FreezeReport {
	kind: FreezeKind
	detectedAt: number
	// recovered distinguishes a freeze the page came back from — which is every
	// live detection, since detecting it requires running again — from one it never
	// came back from. Only 'page_died' is unrecovered, and it is the severe case:
	// a hiccup annoys a user, a death silently removes a donor from the network.
	recovered: boolean
	// gapMs is the observed interval between watchdog ticks (for 'page_died', the
	// staleness of the final breadcrumb). Compare against tickMs.
	gapMs: number
	// goStaleMs is how old Go's last tick was when we noticed. null when the wasm
	// binary predates the Go heartbeat, or when the engine was never started.
	goStaleMs: number | null
	goTicks: number | null
	// clockSkewMs is our Date.now() minus Go's, sampled around the same call. A
	// large value means the liveness() call itself queued behind a blocked event
	// loop — a freeze signal no counter delta reveals, since the counter is only
	// read once the queue drains.
	clockSkewMs: number | null
	// The worst long task seen in this page life. This is the attribution signal:
	// a 30-second long task *is* the freeze, and its container name is the suspect.
	longestTaskMs: number | null
	longestTaskName: string | null
	// hidden records whether the tab was backgrounded, so a reader can discount a
	// report that slipped past the visibility gate below.
	hidden: boolean
	// sharing separates a freeze while idle from one while actually proxying. The
	// two implicate different code, and the observed freezes happened while live.
	sharing: boolean
	url: string
	userAgent: string
	// build identifies the bundle that produced this report — the commit the widget
	// page was published from, injected at build time. null in a local build, where
	// the answer is "whatever is on your disk".
	//
	// Added because its absence cost a real diagnosis. Donor pages are long-lived and
	// keep running whatever bundle they loaded with, so after a watchdog fix ships the
	// fleet contains several versions at once for days. When reports kept arriving that
	// the newest code could not possibly produce — gaps past a ceiling that rejects
	// them — there was no way to tell an old client from broken logic except by
	// watching whether the rate decayed over 48 hours. It was old clients. With this
	// field it is one query instead of an afternoon.
	build: string | null
}

// Breadcrumb is the localStorage record. Field names are short because this is
// rewritten every tick.
interface Breadcrumb {
	t: string // tab id
	b: number // last beat, epoch ms
	s: number // page start, epoch ms
	c: boolean // closed cleanly (pagehide fired)
	h: boolean // hidden at last beat
	p: boolean // sharing at last beat
	l: number | null // longest task ms
	n: string | null // longest task name
	v: string | null // build id of the bundle that wrote this
}

// Liveness mirrors the object returned by Go's liveness(). Field names are a
// contract with watchdog_wasm_impl.go's snapshot(); renaming one there silently
// disables Go-side detection here, which is why they are typed rather than read
// loosely.
interface Liveness {
	goTicks: number
	goLastTickMs: number
	goStartedMs: number
	goIntervalMs: number
	goNowMs: number
}

// ValidatedLiveness is what readLiveness returns, and the shape the rest of this
// file is allowed to touch.
//
// Only goTicks and goLastTickMs carry the `number` guarantee, because only those
// two are worth rejecting a whole snapshot over. Every other field is explicitly
// nullable so no use site can do arithmetic on a missing one — the failure mode
// being designed out is silent, not loud: reading an absent field yields NaN, every
// NaN comparison is false, and the value lands in a report looking like data.
//
// Enforcing this at the boundary rather than at each use site is the point. The
// first version validated only the two required fields and then read goNowMs
// anyway, three lines below a comment claiming NaN had been designed out.
type ValidatedLiveness = {
	goTicks: number
	goLastTickMs: number
	goStartedMs: number | null
	goIntervalMs: number | null
	goNowMs: number | null
}

// asNumber narrows an unknown to a usable number, rejecting NaN and the
// infinities: those are numbers by typeof but poison every comparison downstream
// exactly like a missing field would.
const asNumber = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null)

// WatchdogContext supplies what only the caller knows. Passed in rather than
// imported so this module stays free of app dependencies — wasmInterface imports
// it, and importing back would be a cycle.
export interface WatchdogContext {
	// liveness returns Go's heartbeat, or undefined before the wasm client exists.
	// Called through a getter every tick because the client is built asynchronously
	// and may be rebuilt; capturing it once would read a stale object forever.
	liveness?: () => Liveness | undefined
	// sharing reports whether the widget is currently proxying.
	sharing?: () => boolean
	// onReport is called for each freeze. Defaults to console + beacon.
	onReport?: (report: FreezeReport) => void
}

// safeStorage centralizes the fact that localStorage is not always usable: it
// throws outright in Safari private browsing and when the quota is exhausted. A
// watchdog that crashes the page it is watching would be worse than no watchdog,
// so every access is guarded and failure is silent by design.
const safeStorage = {
	get(): Storage | null {
		try {
			return window.localStorage
		} catch {
			return null
		}
	},
	read(key: string): string | null {
		try {
			return this.get()?.getItem(key) ?? null
		} catch {
			return null
		}
	},
	write(key: string, value: string): void {
		try {
			this.get()?.setItem(key, value)
		} catch {
			// Quota or private mode. Live detection still works; only the
			// crash-recovery path is lost.
		}
	},
	remove(key: string): void {
		try {
			this.get()?.removeItem(key)
		} catch {
			// ignore
		}
	},
	keys(): string[] {
		try {
			const s = this.get()
			if (!s) return []
			const out: string[] = []
			for (let i = 0; i < s.length; i++) {
				const k = s.key(i)
				if (k && k.startsWith(storagePrefix)) out.push(k)
			}
			return out
		} catch {
			return []
		}
	},
}

const newTabId = (): string =>
	// Not security-sensitive: this only has to avoid two tabs of the same page
	// sharing a storage key.
	`${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`

export class FreezeWatchdog {
	private readonly ctx: WatchdogContext
	private readonly tabId = newTabId()
	private readonly startedAt = Date.now()
	private readonly storageKey: string

	private timer: ReturnType<typeof setInterval> | undefined
	// Whether start() has run. Distinct from `timer`, which is legitimately absent
	// while the page sits in the back/forward cache.
	private started = false
	private observer: PerformanceObserver | undefined
	private lastTickAt = Date.now()
	// hiddenSinceLastTick makes the visibility gate correct across a full
	// hide/show cycle between two ticks. Checking visibilityState at tick time
	// alone would miss it: the tab is visible again by then, but the gap it
	// produced was throttling, not a freeze.
	private hiddenSinceLastTick = false
	// frozenSinceLastTick is the same idea for the Page Lifecycle 'freeze' event,
	// which fires when the browser parks a backgrounded page outright. Unlike being
	// hidden, a frozen page runs nothing at all — including Go — so the gap it
	// produces is invisible to every other check here. See maxBlockMs.
	private frozenSinceLastTick = false
	private longestTaskMs: number | null = null
	private longestTaskName: string | null = null
	// Consecutive on-time ticks, i.e. how much evidence we have that the thread is
	// currently healthy. See minPunctualTicks.
	private punctualTicks = 0
	// When each kind was last reported, so a standing condition is throttled
	// without muting a newly-appearing one. See repeatSuppressMs.
	private lastReportAt = new Map<FreezeKind, number>()

	readonly reports: FreezeReport[] = []

	constructor(ctx: WatchdogContext = {}) {
		this.ctx = ctx
		this.storageKey = storagePrefix + this.tabId
	}

	// start begins watching and reports anything left behind by a previous page
	// life. Safe to call twice; the second call is ignored.
	start(): void {
		// Guarded on `started` rather than on `timer`, because the timer is legitimately
		// absent between pagehide and pageshow. Keying on the timer would let a second
		// start() in that window re-run the breadcrumb sweep.
		if (this.started) return
		this.started = true

		// Sweep first, so a death is reported even if this page life is short.
		this.recoverBreadcrumbs()

		this.observeLongTasks()
		document.addEventListener('visibilitychange', this.onVisibilityChange)
		// Page Lifecycle. Not universally supported — Chrome fires these, Safari and
		// Firefox largely do not — which is why maxBlockMs backstops it rather than
		// this being the only guard.
		document.addEventListener('freeze', this.onFreeze)
		document.addEventListener('resume', this.onResume)
		// pagehide rather than unload: unload does not fire reliably on mobile
		// Safari, and treating a normal navigation as a death would drown the real
		// signal in false positives.
		window.addEventListener('pagehide', this.onPageHide)
		window.addEventListener('pageshow', this.onPageShow)

		this.armTimer()
	}

	stop(): void {
		this.started = false
		this.clearTimer()
		this.observer?.disconnect()
		this.observer = undefined
		document.removeEventListener('visibilitychange', this.onVisibilityChange)
		document.removeEventListener('freeze', this.onFreeze)
		document.removeEventListener('resume', this.onResume)
		window.removeEventListener('pagehide', this.onPageHide)
		window.removeEventListener('pageshow', this.onPageShow)
		// An explicit stop is a clean exit; leaving the record un-flagged would
		// make the next load report this tab as dead.
		safeStorage.remove(this.storageKey)
	}

	// armTimer starts beating and resets the baselines the first tick will compare
	// against. Resetting them is not optional: wall-clock time advances while a page
	// sits frozen in the back/forward cache, and a stale lastTickAt turns that entire
	// interval into a fabricated freeze the moment the page is restored.
	private armTimer(): void {
		this.lastTickAt = Date.now()
		this.punctualTicks = 0
		this.resetIntervalLatches()
		this.writeBreadcrumb(false)
		this.timer = setInterval(this.tick, tickMs)
	}

	// resetIntervalLatches re-seeds both "something happened since the last tick" flags
	// for the interval starting now.
	//
	// They exist as a pair and must be maintained as a pair, which is exactly what went
	// wrong when the freeze latch was added: hiddenSinceLastTick was already reset in
	// three places and the new flag only in one, so it survived a re-arm and poisoned
	// the interval afterwards.
	//
	// Note it SEEDS rather than clears. onVisibilityChange only latches on the
	// transition *to* hidden, so a page that is already hidden when an interval begins
	// would otherwise start with a false latch and have its throttled gap judged as if
	// the tab had been visible throughout. That is reachable from every caller: start()
	// on a background tab, a bfcache restore into one, and a Page Lifecycle resume,
	// which commonly resumes still-hidden.
	private resetIntervalLatches(): void {
		this.hiddenSinceLastTick = document.visibilityState !== 'visible'
		this.frozenSinceLastTick = false
	}

	private clearTimer(): void {
		if (this.timer) {
			clearInterval(this.timer)
			this.timer = undefined
		}
	}

	// snapshot exposes current state for manual inspection from the console. The
	// most likely consumer is someone poking at a tab that just misbehaved.
	snapshot() {
		const live = this.readLiveness()
		return {
			tabId: this.tabId,
			startedAt: this.startedAt,
			lastTickAt: this.lastTickAt,
			sinceLastTickMs: Date.now() - this.lastTickAt,
			longestTaskMs: this.longestTaskMs,
			longestTaskName: this.longestTaskName,
			liveness: live ?? null,
			longTasksSupported: this.observer !== undefined,
			reports: this.reports,
		}
	}

	private readLiveness(): ValidatedLiveness | undefined {
		try {
			const live: Partial<Liveness> | undefined = this.ctx.liveness?.()
			if (!live) return undefined

			// Guard the shape rather than trusting it: this crosses the wasm boundary,
			// a binary predating the Go heartbeat returns undefined, and a future one
			// could change fields. Reading a missing field yields NaN, and because
			// every NaN comparison is false the result is a silently disabled detector
			// rather than a visible error.
			const goTicks = asNumber(live.goTicks)
			const goLastTickMs = asNumber(live.goLastTickMs)
			// These two are what detection depends on, so a snapshot without them is
			// no snapshot at all.
			if (goTicks === null || goLastTickMs === null) return undefined

			// The rest degrade individually. Rejecting the whole snapshot because a
			// supplementary field was missing would throw away the primary staleness
			// signal to protect a diagnostic one.
			return {
				goTicks,
				goLastTickMs,
				goStartedMs: asNumber(live.goStartedMs),
				goIntervalMs: asNumber(live.goIntervalMs),
				goNowMs: asNumber(live.goNowMs),
			}
		} catch {
			// A wedged Go runtime can make the call itself throw.
			return undefined
		}
	}

	private sharing(): boolean {
		try {
			return this.ctx.sharing?.() ?? false
		} catch {
			return false
		}
	}

	private onVisibilityChange = (): void => {
		if (document.visibilityState !== 'visible') this.hiddenSinceLastTick = true
	}

	// A frozen page is not running, so the gap across a freeze is the browser parking
	// us rather than anything failing. Latched the same way as the visibility gate,
	// because by the time we tick again the page is thawed and nothing else says it
	// ever stopped.
	private onFreeze = (): void => {
		this.frozenSinceLastTick = true
	}

	// 'resume' carries no gap information on its own — the next tick measures it —
	// but the baseline has to be reset here, or that tick reports the whole frozen
	// stretch as one enormous gap.
	private onResume = (): void => {
		this.lastTickAt = Date.now()
		this.punctualTicks = 0
		// And drop the freeze latch, because resetting the baseline above has already
		// dealt with the frozen stretch: the next gap is measured from now, so it does
		// not span the freeze and there is nothing left to discount. Keeping the latch
		// set would suppress one legitimate interval — and the interval right after a
		// thaw is a plausible place for a real block, since a resuming page often has
		// catch-up work to do.
		//
		// The latch still protects the freeze-without-resume case: nothing clears it
		// until either this handler or the next tick runs.
		this.resetIntervalLatches()
	}

	private onPageHide = (): void => {
		// Mark the exit clean so the next load does not mistake this navigation for
		// a death. This is the one write that must not be skipped.
		this.writeBreadcrumb(true)
		// Then stop beating. A tick that runs after this point would rewrite the
		// record with c:false and resurrect a cleanly-closed page as a casualty —
		// the exact false positive that would make every ordinary navigation look
		// like a crash.
		//
		// Not a one-way shutdown: pagehide is not necessarily terminal. The
		// back/forward cache fires it, freezes the page, and may restore it later,
		// which is what onPageShow re-arms for.
		this.clearTimer()
	}

	// onPageShow resumes after a back/forward-cache restore.
	//
	// Re-arming is required, not merely tidy. A restored page is a live donor again,
	// and leaving it stopped would mean it beats no more breadcrumbs — so if it is
	// later killed, the record still carries pagehide's clean flag and the death goes
	// unreported. That is the failure this whole path exists to catch, so a naive
	// "stop the timer on pagehide" would trade one false positive for a false
	// negative on the case that matters most.
	private onPageShow = (): void => {
		if (!this.started || this.timer) return
		this.armTimer()
	}

	private tick = (): void => {
		const now = Date.now()
		const gapMs = now - this.lastTickAt
		this.lastTickAt = now

		const hidden = document.visibilityState !== 'visible'
		// A tab that was hidden at any point since the last tick had its timers
		// throttled, so its gap says nothing about freezing. Reset the baselines
		// and wait for a clean interval rather than reporting throttling as a bug.
		//
		// A gap spanning a Page Lifecycle freeze is the same story with a stronger
		// cause: the page was not running at all, so nothing about the interval is
		// evidence of anything. Latched at the event because by tick time the page is
		// thawed and looks entirely normal.
		// Snapshot both latches, then clear both together. Reading one inline and
		// assigning the other separately is what allowed them to drift apart.
		const frozen = this.frozenSinceLastTick
		const hiddenSince = this.hiddenSinceLastTick
		this.resetIntervalLatches()

		// gapMs beyond maxBlockMs is a parked process, not a blocked thread. This has
		// to be judged on magnitude alone: when the whole context stops, our clock and
		// Go's stop together, which is byte-for-byte the fingerprint of a real block.
		// It is the backstop for every case the freeze event misses — OS sleep, and
		// browsers that do not implement Page Lifecycle at all.
		const parked = gapMs > maxBlockMs

		const trustworthy = !hidden && !hiddenSince && !frozen && !parked

		const live = this.readLiveness()
		// Go stamps goLastTickMs with Date.now() from inside wasm specifically so it
		// can be compared against ours without clock-skew arithmetic. Staleness is
		// measured on its own terms rather than against our gap: tying it to our gap
		// would make 'go wedged' imply 'we were starved too', collapsing the two
		// independent failures into one and losing the distinction entirely.
		const goStaleMs = live ? now - live.goLastTickMs : null
		const goStalled = goStaleMs !== null && goStaleMs > freezeMs

		const jsStarved = gapMs > freezeMs

		// A hidden interval tells us nothing about thread health either way, so it
		// neither builds nor keeps the streak.
		this.punctualTicks = trustworthy && !jsStarved ? this.punctualTicks + 1 : 0

		// Every live verdict requires Go to have stalled too. That is not caution, it
		// is what the shared-thread model permits: Go/wasm and JS run on one thread
		// (see the truth table in watchdog_wasm_impl.go), so a thread blocked long
		// enough to miss our tick necessarily stalls Go's one-second heartbeat with
		// it. A starved timer beside a current heartbeat therefore proves the thread
		// was alive and simply not scheduling us — which is a browser throttling
		// timers, not a freeze.
		//
		// This is the question `hidden` was meant to answer and could not. The widget
		// overwhelmingly runs inside an offscreen document, where visibilityState
		// reports 'visible' while timers are throttled exactly as in a background tab,
		// so the visibility gate above passes. Of the first 29 real reports, 26 came
		// from that context and 16 were this false positive — gaps of 42s to 17min
		// against a Go heartbeat current to within a second.
		//
		// The cost is that a build whose wasm publishes no heartbeat gets no live
		// detection at all, since there is then nothing to corroborate with. That is
		// the honest outcome rather than a regression: without the heartbeat a gap is
		// genuinely uninterpretable, and guessing is what produced the noise. page_died
		// still works there, and it is the severe case.
		const actionable = goStalled && (jsStarved || this.punctualTicks >= minPunctualTicks)

		if (trustworthy && actionable) {
			this.report(this.classify(jsStarved), {
				gapMs,
				goStaleMs,
				goTicks: live?.goTicks ?? null,
				clockSkewMs: live?.goNowMs != null ? now - live.goNowMs : null,
				hidden,
				recovered: true,
				longestTaskMs: this.longestTaskMs,
				longestTaskName: this.longestTaskName,
				sharing: this.sharing(),
				build: buildId,
			})
		}

		this.writeBreadcrumb(false)
	}

	// Only reached when goStalled, which tick() now requires. The two remaining
	// verdicts are the two the shared-thread truth table actually distinguishes:
	// our timer stalled with Go's (the thread), or ours kept time while Go's did not
	// (the runtime).
	private classify(jsStarved: boolean): FreezeKind {
		return jsStarved ? 'main_thread_blocked' : 'go_scheduler_wedged'
	}

	// report takes every piece of evidence explicitly rather than reading any of it
	// from instance state.
	//
	// That is deliberate and load-bearing: a 'page_died' report describes a
	// *previous* page life, so its long-task attribution and sharing flag must come
	// from that life's breadcrumb, not from this one. An earlier version filled them
	// in from `this`, which at startup means null attribution and sharing=false —
	// silently discarding the only evidence explaining the death it was reporting.
	// Requiring them as arguments makes that mistake a compile error.
	private report(
		kind: FreezeKind,
		fields: Omit<FreezeReport, 'kind' | 'detectedAt' | 'url' | 'userAgent'>
	): void {
		const now = Date.now()
		// 'page_died' is exempt: it is only ever produced by the startup sweep,
		// which is already bounded, and each record represents a distinct casualty.
		if (kind !== 'page_died') {
			const last = this.lastReportAt.get(kind)
			if (last !== undefined && now - last < repeatSuppressMs) return
			this.lastReportAt.set(kind, now)
		}

		const report: FreezeReport = {
			kind,
			detectedAt: now,
			url: window.location.href,
			userAgent: navigator.userAgent,
			...fields,
		}

		this.reports.push(report)
		if (this.reports.length > maxReports) this.reports.shift()

		if (this.ctx.onReport) {
			this.ctx.onReport(report)
			return
		}
		defaultReport(report)
	}

	// observeLongTasks records the worst long task of this page life, which is the
	// only signal that says *what* froze rather than merely that something did.
	//
	// Feature-detected because Safari does not implement the longtask entry type,
	// and mobile Safari is a target. The rest of the watchdog works without it;
	// reports simply carry a null attribution.
	private observeLongTasks(): void {
		try {
			if (typeof PerformanceObserver === 'undefined') return
			const supported = (PerformanceObserver as any).supportedEntryTypes
			if (Array.isArray(supported) && !supported.includes('longtask')) return

			const observer = new PerformanceObserver(list => {
				for (const entry of list.getEntries()) {
					if (this.longestTaskMs === null || entry.duration > this.longestTaskMs) {
						this.longestTaskMs = Math.round(entry.duration)
						// Prefer the attribution container name — for a freeze inside a
						// third-party widget that names the culprit, where the entry name
						// is almost always the generic 'self'.
						const attribution = (entry as any).attribution?.[0]
						const name: unknown = attribution?.containerName || attribution?.name || entry.name
						// Bounded on the way in as well as on the way out: this is written
						// to localStorage every tick, so an unbounded value would be paid
						// for repeatedly rather than once.
						this.longestTaskName = typeof name === 'string' ? name.slice(0, maxTaskNameLen) : null
					}
				}
			})
			observer.observe({entryTypes: ['longtask']})
			this.observer = observer
		} catch {
			// Unsupported or blocked; detection degrades rather than failing.
		}
	}

	private writeBreadcrumb(closedCleanly: boolean): void {
		const crumb: Breadcrumb = {
			t: this.tabId,
			b: Date.now(),
			s: this.startedAt,
			c: closedCleanly,
			h: document.visibilityState !== 'visible',
			p: this.sharing(),
			l: this.longestTaskMs,
			n: this.longestTaskName,
			v: buildId,
		}
		safeStorage.write(this.storageKey, JSON.stringify(crumb))
	}

	// recoverBreadcrumbs reports page lives that stopped beating without firing
	// pagehide. This is the only detector for a freeze that never ended, and it
	// also catches renderer crashes and OS kills — absence of the clean-exit flag
	// is the signal, which is precisely why pagehide is the flag and not unload.
	private recoverBreadcrumbs(): void {
		const now = Date.now()
		const crumbs: Breadcrumb[] = []

		for (const key of safeStorage.keys()) {
			if (key === this.storageKey) continue

			const raw = safeStorage.read(key)
			if (!raw) {
				safeStorage.remove(key)
				continue
			}

			let crumb: Breadcrumb | undefined
			try {
				crumb = JSON.parse(raw) as Breadcrumb
			} catch {
				// Truncated by a quota failure mid-write, or written by an older
				// version. Unusable either way.
				safeStorage.remove(key)
				continue
			}

			if (!crumb || typeof crumb.b !== 'number') {
				safeStorage.remove(key)
				continue
			}

			// Age is computed once and every decision below reads it, because each of
			// those decisions is a comparison against it and three separate
			// subtractions invite exactly one of them to disagree.
			const ageMs = now - crumb.b

			// A record stamped in the future is undecidable. The wall clock can move
			// backwards between write and read — an NTP correction, a VM restore, a
			// dual-boot machine, a user setting it by hand — and this runs on other
			// people's computers, so that is a matter of time rather than a
			// hypothetical.
			//
			// Dropped rather than kept, because keeping it is the one outcome with no
			// exit: a negative age is below deadTabMs, so it reads as a live tab and is
			// left in place, and it is also below storageTtlMs, so it never expires. The
			// record would sit in an embedder's localStorage indefinitely — precisely
			// the footprint this file promises not to leave on a host it is a guest on.
			if (ageMs < 0) {
				safeStorage.remove(key)
				continue
			}

			// Expire before deciding: a week-old death is not news, and reporting it
			// on every load would bury current problems.
			if (ageMs > storageTtlMs) {
				safeStorage.remove(key)
				continue
			}
			// Strictly === true, not truthy. A corrupt or hand-edited value like the
			// string "false" is truthy, and treating it as a clean exit *suppresses* a
			// real death report. Of the two ways to be wrong about a malformed record,
			// inventing a casualty is far cheaper than silently dropping one: this
			// whole path exists because deaths were invisible, and an anomalous record
			// from a tab that never said goodbye is worth surfacing anyway.
			if (crumb.c === true) {
				// Clean exit. Nothing to report, and nothing to keep.
				safeStorage.remove(key)
				continue
			}
			if (ageMs <= deadTabMs) {
				// Still beating recently, so this is a live tab in another window.
				// Leave its record alone — it owns that key.
				continue
			}
			if (ageMs > maxDeadTabAgeMs) {
				// Too old to mean anything. See maxDeadTabAgeMs: past this point a
				// dead tab and a sleeping machine are the same record, and the
				// sleeping machine is the likelier one by far. Dropped rather than
				// kept, since no later load will find it any more decipherable.
				safeStorage.remove(key)
				continue
			}
			crumbs.push(crumb)
			safeStorage.remove(key)
		}

		// Newest first, then bounded: if many tabs died, the recent ones are the
		// ones worth looking at.
		crumbs.sort((a, b) => b.b - a.b)
		for (const crumb of crumbs.slice(0, maxStoredTabs)) {
			this.report('page_died', {
				gapMs: now - crumb.b,
				// The dead tab published no heartbeat we can read now. Nulls rather
				// than zeros, so this never reads as "Go was fine".
				goStaleMs: null,
				goTicks: null,
				clockSkewMs: null,
				// === true rather than truthy, for the same reason as crumb.c above: a
				// non-boolean would otherwise mislabel the report it is meant to explain.
				hidden: crumb.h === true,
				recovered: false,
				// From the breadcrumb, not from this page life. The long task the dead
				// tab recorded is the whole reason it was recorded — it is the only
				// account of what that tab was doing when it stopped.
				//
				// Types are checked rather than trusted: a record truncated by a quota
				// failure mid-write, or written by an older version of this file, can
				// carry anything. A wrong type here would reach console and the beacon.
				longestTaskMs: typeof crumb.l === 'number' ? crumb.l : null,
				longestTaskName: typeof crumb.n === 'string' ? crumb.n.slice(0, maxTaskNameLen) : null,
				sharing: crumb.p === true,
				// From the record, not from this page life. A death is recovered by
				// whatever bundle loads next, which after a widget deploy is a
				// *different* one — so reading buildId here would label every death
				// with the version that found it rather than the version that died,
				// and blame each fix for the failures it was shipped to fix.
				build: typeof crumb.v === 'string' ? crumb.v.slice(0, 64) : null,
			})
		}
	}
}

// defaultReport is the sink used when the caller supplies none.
//
// console.warn is unconditional and deliberately first: it is the only sink that
// works with no infrastructure, and someone staring at a misbehaving tab is the
// most likely reader. It also stays the durable record when the beacon fails:
// sendBeacon returns only whether the browser queued the payload, never whether it
// arrived, so anything that goes wrong after queueing is invisible from here.
//
// The beacon is opt-in via REACT_APP_FREEZE_BEACON_URL, which production points at
// the egress's POST /freeze. Unset — as in any local build — the watchdog still
// diagnoses freezes and still reports them to the console and the window global;
// only the network hop is skipped.
export const defaultReport = (report: FreezeReport): void => {
	const detail = report.recovered
		? `recovered after ${report.gapMs}ms`
		: `page never recovered (last beat ${report.gapMs}ms before this load)`
	console.warn(`[unbounded watchdog] ${report.kind}: ${detail}`, report)

	const url = process.env.REACT_APP_FREEZE_BEACON_URL
	if (!url) return
	try {
		// The one failure the browser will tell us about. sendBeacon returns false
		// when it declines to queue at all — typically the payload exceeding its
		// limit — and that is the only signal we get, since it reports nothing about
		// what happens afterwards. Discarding it would make the single detectable
		// drop as silent as the undetectable ones, which is the failure mode this
		// whole path exists to avoid.
		const queued = navigator.sendBeacon?.(url, JSON.stringify(report))
		if (queued === false) {
			console.warn('[unbounded watchdog] beacon not queued; report kept to console only')
		}
	} catch {
		// A failed beacon must never surface to the user; the console line above
		// is the durable record.
	}
}

let installed: FreezeWatchdog | undefined

// installFreezeWatchdog starts the watchdog once per page and exposes it as
// window.__unboundedWatchdog for console inspection.
export const installFreezeWatchdog = (ctx: WatchdogContext = {}): FreezeWatchdog => {
	if (installed) return installed
	const watchdog = new FreezeWatchdog(ctx)
	installed = watchdog
	watchdog.start()
	try {
		;(window as any).__unboundedWatchdog = watchdog
	} catch {
		// ignore
	}
	return watchdog
}
