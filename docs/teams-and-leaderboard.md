# Teams and leaderboards: design notes

Status: **planning only.** Nothing here is implemented. The purpose is to
record what already exists, which problems are actually hard, and which
step carries a cost to the people we are asking for help.

Three things decide whether a leaderboard helps or hurts: what can be
attributed without being lied to, what gets counted, and who is allowed
to see it. The first decides whether a team can be framed with traffic
it never sent; the third decides whether a publisher who did everything
right ends up on a target list.

## Teams have been built twice and never carried a real team

Worth knowing before designing the third attempt, because both previous
attempts stopped in the same place — the wire format.

- `0658b1f` (Apr 2025) sent a team ID from consumer to egress in the
  WebSocket subprotocol as `unbounded-team:<id>`. Every client observed
  in the wild sent the hardcoded placeholder `no_team`.
- The mechanism then moved to a QUIC datagram, where the value was the
  literal string `teamid-not-set`. That was deleted in `6561021`
  (Jul 2025).
- The egress now recognises those clients only to refuse them and signal
  an upgrade — `refusedLegacyTeamClient` in `egress/refusals.go`.

So the wire carried a team field for three months and it never held a
team. Nothing was built on top, because the field is not the hard part.

## What can be built on today

- **A carrier with an extension point.** The subprotocol is a positional
  list — `[cookie, csid, version, country?]` via
  `common.NewSubprotocolsRequestWithCountry`. It is not open-ended,
  though: `ParseSubprotocolsRequestWithCountry` accepts exactly three or
  four elements, and the fourth is positional, so a team element needs a
  tagged form (e.g. `team.<id>` — subprotocols must be RFC 6455 tokens, so no `=`, and the ID alphabet has to be token-safe) and the same egress-first rollout the
  country element needed.
- **An embed API and a snippet generator.** The widget hydrates `data-*`
  attributes into `Settings` (`ui/src/constants/index.ts`,
  `ui/src/index.tsx`), and the editor already rewrites the embed's
  `dataset` in place. A `data-team` attribute reuses that path; it still
  needs `team` added to `Settings` and `defaultSettings`, since hydration
  only reads keys present there, and then passed through to the client.
- **Server-side measurement.** The egress already counts per-session
  bytes (`broflake.session_ingress_bytes`) and per-country ingress.
  Points can be derived from what the egress observed, never from what a
  client claimed. Today's counters are diagnostics, not a ledger: the
  per-session attribute rides on spans sampled at 1%, the per-country
  observable resets each collection, and both count raw ingress before
  QUIC validates it. Scoring needs an unsampled, durable per-team
  aggregate of validated traffic.
- **An unforgeable origin, currently unused.** Origin checking is off —
  `InsecureSkipVerify=true` in `egress/egresslib.go`, with a standing
  TODO to add `OriginPattern`. Browsers send the header on every upgrade
  and do not let page JavaScript forge it. This is the most useful item
  in the list — with the caveat that only a browser enforces it; a
  non-browser client can omit `Origin` or send any value it likes. A
  connection with no `Origin` is unbound and earns no team credit.

### House constraint

Peer-chosen values must never become metric labels — that is unbounded
cardinality controlled by a stranger. Freeze reports validate `kind`
against a fixed set of five for exactly this reason (`egress/freeze.go`).
A team ID is peer-chosen, so it has to validate against a bounded
server-side registry before it can be counted, and leaderboard state
belongs in a store rather than in OpenTelemetry labels.

## Problem 1: the widget cannot hold a secret

Folding@home solves credit integrity with a *passkey* — a token tying
returned work to you rather than to anyone typing your username. Without
one, two donors picking the same name are indistinguishable, and if one
cheats they are both penalised. It works because the F@h client runs on
the donor's own machine, where a secret can live.

Our producer is JavaScript in a stranger's browser. Everything in the
page is readable and copyable. `data-team="some-outlet"` is a claim, not
a credential.

The failure mode people expect is inflation. The one that actually bites
is **sabotage**: attributing traffic you control to a team you want
discredited, or blocked. A leaderboard where anyone can assign traffic to
anyone else's team is worse than none, because it launders bad behaviour
into someone else's name.

| Signal | Set by | Forgeable? |
| --- | --- | --- |
| `data-team` attribute | whoever wrote the embed | by anyone |
| `Origin` on the upgrade | the browser | bound to a domain |
| team → origin registry | us, after a domain-control check | bound to an owner |
| account key | the donor's own client | bound to an account, not a person |

**Proposal:** treat `data-team` as a hint and accept it only when the
request's `Origin` is one the registry has bound to that team. Domain
control is proven once, by DNS TXT record or a file at a well-known path.
Forging another team's credit from a web page then requires controlling
their domain.

That stops the cheap attack — embedding the widget on your own site with
a rival's `data-team` — but not a custom client, which can set `Origin`
to anything. Origin binding raises the cost of sabotage from editing an
attribute to writing a client — or to framing the publisher's own page.
A page that allows framing can be iframed, hidden or clickjacked, and its
traffic still carries the publisher's genuine `Origin`; the freeze
watchdog's `document.visibilityState` says the tab is visible, not that
the widget is. Bound teams therefore need `frame-ancestors` set on the
pages that embed the widget, or the widget must refuse to run framed.
Past that there is no attribution proof:
per-origin ceilings and anomaly detection can limit how much a forged
session counts, but they cannot say which publisher supplied it, and an
attacker can still fill a rival's ceiling. This is an open risk, and it
is why forged-looking traffic should never become a penalty against the
team it names.

### Per-member attribution

There are two kinds of member and only one can be a named individual.

- **Site visitors are not individually attributable, and should not be.**
  Identifying every visitor who ran the widget means building a tracking
  identity for people who never asked for one, on a circumvention tool.
  Credit accrues to the *team*: a session adds to the team's aggregate,
  and no per-visitor or per-session record is kept beyond what the egress
  already emits.
- **Extension and VPN donors can be.** Their client runs on their own
  machine, so an account-bound key works much as a passkey does, and
  they can pick a team in settings. The key identifies an account, not a
  person: it can be shared or stolen, so it needs rotation and recovery,
  and individual credit is pseudonymous account credit.

That split is a feature. Sites compete as teams, individuals compete as
individuals, and anyone wanting personal credit installs something. Say
so in the UI rather than implying a precision the browser cannot deliver.

## Problem 2: bytes relayed is the obvious currency and is wrong alone

**It ranks donors on the scheduler's decisions.** A donor's byte count
depends on whether any censored user was routed through them. A widget
can sit open and ready all afternoon and score zero. Rank on bytes alone
and you partly rank routing luck — and you give teams a reason to try to
influence routing, which is the last thing they should be optimising.

So count **availability offered** — ready-and-online time — alongside
service delivered. Availability is the part a donor controls, and it is
what the network needs more of. It is also the easiest input to inflate,
so it can only be ranked once it is measured server-side (time the egress
saw the donor connected and ready), not reported by the client.

**The ranked metric displaces the real one.** In a randomised trial
(Chen, Dobrescu, Foster & Motta, *Labour Economics* 90, 2024,
doi:10.1016/j.labeco.2024.102602), students assigned to small
similar-score leaderboard groups improved at the bottom — low performers
scored 0.27 SD higher — but high performers overachieved on the *ranked*
task and scored **0.25 SD lower on the exam that actually mattered**.

Rank bytes and we will get bytes, including from tabs left open on
someone's battery. The top of the board is where that pressure is
strongest.

**Rate, not just totals.** A team's raw score is roughly
visitors × time-on-page × willingness, so totals rank site traffic and a
large outlet always beats a blogger. Add measures a small site can win:
contribution per session, or the share of sessions that stayed past a
minimum. The egress sees sessions, not page visitors, so any per-visitor
rate would rest on a site-supplied count and cannot be ranked on.
Those reward persuading your readers, which is the behaviour worth
spreading.

**Seasons, not all-time.** All-time cumulative means the first large
outlet to join wins permanently and a blogger arriving in month six has
no reachable goal. This matters most for the case that motivated the
idea — an outlet embedding the widget in an article *about* Unbounded.
Article traffic spikes and decays, so under all-time totals that team
sinks steadily forever, which is a strange reward for the best kind of
coverage. Under seasons they win the week they published.

## Problem 3: a public leaderboard is a target list

This is the one a naive design gets wrong, and the cost lands on the
publisher who agreed to help.

Tor keeps bridge addresses unenumerable because for circumvention
infrastructure enumerability *is* blockability. Our situation differs in
one way and is worse in another. Different: embedding sites are already
public, so a leaderboard reveals no secret. Worse: it **curates** —
participating sites ranked by how much they matter, sorted, in one place,
kept current. That is a better target list than a censor could cheaply
build, and we would maintain it for them.

Concrete harms:

- **The publisher gets blocked in-country.** A news outlet that tops the
  board has handed a censor a reason to block its domain, punishing the
  organisation that helped, in the country whose readers most need it.
- **Pressure follows rank.** Abuse complaints, hosting pressure and legal
  attention concentrate at the top of a visible list.
- **Ranked by contribution is ranked by value-to-block.**

Mitigations, none of which prevent the feature:

- Publicity is **opt-in per team**; the default is a private dashboard.
- **Pseudonyms are first-class.** A team can appear under a handle with
  no domain shown. The handle is a display label: the team is still
  origin-bound in the registry, and only the binding is kept private. F@h
  has allowed `anonymous` folding for two decades without hurting
  participation.
- **Publish bands, not figures.** Enough to compete on, less to
  prioritise from.
- **Never publish a view that answers "who is most worth blocking"** — no
  absolute throughput per site, no per-country breakdown of which teams
  serve which censored regions.
- **Leaving is immediate and total.** Withdrawing, a domain compromise
  or a change of ownership removes the team's current and past standings
  from the board and its feeds, not just future ones.
- **Say all of this in the signup flow**, before anyone opts in.

## Fraud model

Embedding a widget and crediting the site is structurally the same
problem as affiliate attribution, which has a mature fraud literature and
an unflattering benchmark: commonly quoted practitioner estimates put
losses to attribution fraud around 8–15% of affiliate commission budgets.
That figure is industry folklore rather than a measured result — useful
as an order of magnitude, not as a calibration target.

| Scheme | Analogue | What helps |
| --- | --- | --- |
| Hidden widget in bought ad inventory | cookie stuffing | **The serious one.** Cheap ad slots, widget in a 1×1 iframe, points farmed from readers who never consented — and if a visitor is in a censored country we have conscripted the person we exist to protect. Require visibility and consent; the freeze watchdog already reports `hidden`, `sharing` and visibility state. |
| Headless browser fleets | self-referral | Origin binding raises the cost to owning a domain; then per-origin ceilings against an independent traffic baseline, plus anomaly detection on session-length distribution. |
| Crediting a rival's team | attribution fraud | Origin binding stops it from a web page, which is why it precedes any public board. A custom client forging `Origin` is not stopped, only bounded by ceilings — an open risk (see Problem 1), so suspect traffic is discounted, never charged to the named team. |
| Claiming work never done | fake leads | Half-prevented: the egress measures and the client never reports a total — never accept a client-supplied score. But `broflake.session_ingress_bytes` counts raw WebSocket payload in `egress/websocket.go` before QUIC validates it, so garbage counts too. Credit needs accounting of delivered QUIC traffic, not raw ingress. |
| Farming, then cashing out | coupon abuse | Borrow F@h's shape: the Quick Return Bonus requires a passkey, ≥10 returned work units and an 80% completion rate. Gate bonuses and public standing on a clean history. |

## Suggested order of work

The first three steps deliver something useful and carry no public
exposure. Only the fourth carries a threat-model cost, and by then we will know
whether the rest works.

1. **Team ID on the wire, against a bounded registry.** A tagged
   subprotocol element, validated as freeze `kind` is validated, rolled
   out egress-first because today's parser rejects any arity but three or
   four. Logged, never a metric label. No UI, no scores.
2. **Capture and bind the origin.** Record `Origin` on the upgrade,
   verify domain control per team, accept a team claim only from a
   bound origin. Bindings need revocation and periodic re-verification
   from the start, so a sold or compromised domain stops carrying the old
   team's credit. Closes a TODO that predates this idea.
3. **Private per-team dashboard.** A publisher sees their own
   contribution, behind the same domain-control proof that bound the
   team. Most of the motivational value of a leaderboard is
   watching your own number move, and this step risks nothing.
4. **Opt-in public board** — seasonal, banded, bracketed by size. Small
   leagues of comparable teams rather than one global list, which is the
   arrangement the trial found helps the bottom instead of demoralising
   it.
5. **Individual credit in the extension and VPN,** via an account-bound
   key. This is where the F@h model transfers cleanly.

## Open questions

- What counts as one team for a publisher with many domains, and what
  happens when a domain is sold or compromised? Registries need
  revocation from day one.
- Do censored users ever see team information? The instinct is a firm no
  — a consumer learning which team carries them is a deanonymisation
  surface — but it should be a decision rather than an omission.
- Can availability be measured without trusting the client more than we
  do now? It is the fairest input and the easiest to inflate.
- What stops teams merging into one super-team, and is that bad if the
  goal is total bandwidth rather than a tidy ranking?
- Which number goes on the widget itself? "This site has helped 1,240
  people this week" is the growth loop, and also the number most worth
  inflating.
