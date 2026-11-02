# Personal proxies: metrics

**Status:** draft 2026-11-02 · **Builds on:** [helper invites](2026-10-20-helper-invites-design.md) and [personal proxies UI](2026-10-29-personal-proxies-ui-design.md), as shipped

## Goal

Know how personal proxies are used. That means how many invites are created, claimed, approved and left to expire, how long each step takes, how many pairings are live and in use, and how much traffic they carry. Every number is broken down by inviter country and volunteer country. As a side effect, also measure traffic through ordinary direct-mode (residential) proxies, which nothing reports today.

Nothing records this today. lantern-cloud's helper handlers log errors only. Its database can't answer it after the fact either: expired invites are deleted, approved invites become tombstones that are swept, and ended pairings are removed. The app has no analytics library.

## Decisions

| Topic | Decision |
|---|---|
| Where it's counted | OpenTelemetry metrics from lantern-cloud, viewed in SigNoz. The api service already exports OTel metrics |
| Labels | Outcome labels, `inviter_cc` and `volunteer_cc` (ISO country, or `unknown`), and `mode` (`direct`, `rooms`) where it applies. Never a pairing ID, invite hash, device ID, name, IP or account |
| Inviter country | Stored. When the invite is created, lantern-cloud records the inviter's two-letter country on the invite row and copies it to the pairing on approval (user decision, 2026-11-02) |
| Volunteer country | Stored on the pairing at approval, from the volunteer's claim. Traffic uses the country of the reporting request instead, because a volunteer's location can change |
| Traffic | Counted on the volunteer's device, which already knows which relayed connections belong to a pairing. It's reported as deltas on calls the device already makes. lantern-cloud adds them to counters and discards them |
| Residential proxies | The same direct-mode heartbeat also reports total relayed bytes, both pool and paired |

## Country

`inviter_cc` and `volunteer_cc` come from the same request-country lookup the config handler uses (`clientCountry`). It has to resolve a censored user's country even when their API calls arrive through Lantern's own proxies. The plan must confirm the helper handlers get the same value, not the IP of a Lantern egress.

Schema: one migration adds a nullable `inviter_country char(2)` to `helper_invites`, and `inviter_country` and `volunteer_country` to `helper_pairings`. Existing rows stay NULL and are reported as `unknown`. lantern-cloud's deploy does not apply migrations. An operator applies them with `bin/migrator migrate up --live` on staging and production before the PR merges, because the merge deploys code that reads the new columns. Agents never run them. The tables are altered one per migration, each with `SET lock_timeout = '5s'`, following 000149-000152.

Privacy: until now the server knew an inviter's country only in passing, from requests it doesn't store. From now on, the invite and pairing rows record it next to the inviter's device ID. That is the accepted cost of the user's decision. Nothing else about the inviter is added, and the rows keep their existing lifetimes: invites are swept at expiry, pairings are deleted when they end.

## Funnel metrics (lantern-cloud, counters unless noted)

| Metric | Labels | Recorded in |
|---|---|---|
| `helper.invites.created` | `inviter_cc`, `result`: `ok`, `invite_cap`, `daily_cap`, `rate_limited`, `error` | CreateInvite |
| `helper.invites.claimed` | `inviter_cc` (`unknown` when there's no live invite), `volunteer_cc`, `result`: `ok`, `already_claimed`, `not_found`, `pair_cap`, `rate_limited`, `error` | ClaimInvite |
| `helper.invites.decided` | `inviter_cc`, `volunteer_cc`, `decision`: `approve`, `reject`, `result`: `ok`, `no_pending_claim`, `error` | DecideInvite |
| `helper.invites.expired` | `inviter_cc`, `stage`: `pending` (never claimed), `claimed` (never decided) | the existing invite sweep, which returns the states it deletes |
| `helper.invite.claim_seconds` (histogram) | `inviter_cc` | ClaimInvite, measured from the invite's `created_at` |
| `helper.invite.decide_seconds` (histogram) | `inviter_cc` | DecideInvite, measured from when the claim was made (stored on claim, or approximated from `created_at` if no claim timestamp exists) |
| `helper.pairings.ended` | `inviter_cc`, `volunteer_cc`, `by`: `inviter`, `volunteer` | EndPairing |
| `helper.pairings.active` (gauge) | `inviter_cc`, `volunteer_cc` | sampled from the database every 5 minutes |
| `helper.volunteers.online` (gauge) | none | sampled from presence every 5 minutes |
| `helper.tickets.issued` | `role`, `mode` | IssueTickets: a pairing in use |

Gauges are sampled by every api replica. The dashboard therefore takes the max across instances, never the sum. Alternatively, the plan may put sampling on whichever replica already runs the sweep.

## Traffic

### Volunteer device (radiance)

- **Direct mode.** The peer code already attributes each connection on the samizdat inbound to a pairing (`trackPairing`, `pairingByShortID`), and totals bytes per connection (`peer/snapshot.go`). Add accumulators that are never reset by snapshots: total relayed bytes, and relayed bytes per pairing.
- **Rooms mode.** The Unbounded widget credits slot bytes to a pairing once the consumer's ticket checks out (`unbounded/snapshot.go`). Add the same per-pairing accumulator.
- **Reporting.** Each report sends what has built up since the last report that succeeded. A failed report keeps the bytes for the next one. Restarting the app loses at most one interval. The two calls:
  - `POST /v1/peer/heartbeat`, every 150 s while direct mode runs. It gains `relayed_bytes` (all traffic) and `paired_bytes` (a list of pairing and byte count, direct mode only).
  - `IssueTickets`, refreshed about every 10 minutes while the volunteer has pairings, in either mode. Its request changes from `Empty` to a message carrying `paired_bytes` for rooms mode. This is the only regular call a rooms-only volunteer makes.
- Pairings are named in a report by the identifier the device already holds. The plan picks the pairing ID or the server-issued short ID, whichever the server can look up most cheaply.

### Server (lantern-cloud)

- `peer.relayed.bytes`, labelled `volunteer_cc` and `kind` (`pool`, `paired`): from the heartbeat. Pool bytes are the total minus paired bytes, floored at 0.
- `helper.relayed.bytes`, labelled `mode`, `inviter_cc` and `volunteer_cc`: from each reported pairing.
  - The server checks that the pairing belongs to the reporting device as volunteer, and takes `inviter_cc` from the pairing row. Unknown or foreign pairings are dropped.
  - `volunteer_cc` is the reporting request's country.
- Reported values are not stored, logged or traced. The pairing ID is used only to find the country, inside the request. That keeps the existing telemetry rule: no logs or traces tie a pairing ID to an IP address, a display name or an account.
- **Trust.** Byte counts come from the client. A device can only report on its own pairings, and each value is capped per report: anything over 50 GB per 150-second heartbeat or per ticket refresh is dropped and counted as `helper.relayed.rejected`. The numbers measure usage. They are not billing-grade and don't back rewards.

### Inviter-side cross-check

radiance already exports `downlink_bytes`/`uplink_bytes` for `outbound_name="helper"` from clients who opted into telemetry. The dashboard shows it next to the volunteer-reported totals as a sanity check. Nothing changes there.

## Dashboard

A SigNoz dashboard named "Personal proxies":
- the funnel: created → claimed → approved, plus what expired at each stage, by inviter country;
- time to claim and time to decide (p50/p90);
- active pairings and online volunteers;
- traffic by mode and by inviter country → volunteer country;
- residential-proxy traffic: pool vs paired, by volunteer country.

## Not in scope

- Taps in the app: entry points, Paste or Scan, the ask-a-friend share. The app has no analytics library.
- Counting at the egress or at freddie. The egress can't tell room traffic from pool traffic, and freddie carries no data.
- Changing what Advanced stats or the Helpers screen show.

## Testing

- lantern-cloud:
  - unit tests for the outcome label of each handler path, using the OTel SDK's manual reader;
  - the migration up and down;
  - a pairing reported by a device that isn't its volunteer is dropped;
  - the per-report cap;
  - the sweep's per-stage counts.
- radiance:
  - the accumulators add up paired and total bytes correctly;
  - a failed report keeps its delta, and a successful one clears it;
  - no report goes out with zero bytes;
  - nothing beyond the pairing identifier and bytes is sent.
- End to end, on a dev stack: pair two devices in each mode, move traffic, and see the counters in SigNoz.
