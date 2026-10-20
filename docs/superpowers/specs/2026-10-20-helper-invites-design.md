# Helper invites: dedicated volunteers for users in Iran

Status: design approved in discussion, 2026-10-20. Not yet implemented.

## Goal

Let a Lantern user in a censored country (Iran first) invite someone they know
abroad to become a proxy volunteer **for them specifically**, over Telegram,
WhatsApp, email or any other channel. A volunteer can serve several people and
can also help the general pool. The invite flow doubles as a Western
acquisition channel for Lantern and Lantern Pro.

Security is a primary requirement: a dedicated pairing must not become a way
for an adversary to identify Lantern users, and a breach of Lantern's servers
must not reveal who is helping whom.

## Terms

- **Inviter**: the user in the censored country (called Sara below).
- **Volunteer**: the person abroad who proxies for her.
- **Pairing**: an approved inviter–volunteer relationship.
- **Direct mode** (SmC): the volunteer's own IP is the exit, via a forwarded port.
- **Unbounded**: WebRTC relay; the volunteer forwards to Lantern's egress.

## Decisions

| Topic | Decision |
|---|---|
| Volunteer surface | The Lantern app first; a browser page running Unbounded as a fallback |
| What a pairing binds to | The inviter's device; the inviter's account when they have one |
| Invite security | Single-use link, matching-code comparison, inviter approval, expiry, revocation |
| Serving | A volunteer serves their paired people first; "help others too" is on by default and uses spare capacity |
| Volunteer mode | Automatic: direct mode whenever it works, otherwise Unbounded. No picker in v1 |
| Multiple pairings | A volunteer can accept many invites; an inviter can have many volunteers |
| Links | Branded (`lantern.io/help/…`), shared through any channel or as a QR code |
| Server knowledge | Blind: the server stores opaque pairing IDs and device IDs, never names or the invite secret |
| Allowlist | The existing residential-peer allowlist applies to paired and pool traffic alike. Paired users never bypass it |
| Pro rewards | Earned by helping, not by signing up: a welcome week, then a week per 30 active days, capped. An account is needed only to claim |
| Rollout | Production pilot behind a feature flag; no staging phase |

## Threat model

A volunteer always sees the client IP of whoever they serve: Unbounded is
peer-to-peer WebRTC, and in direct mode the client connects to the
volunteer's IP. With anonymous volunteers that IP is one of thousands. With a
pairing, the volunteer knows it belongs to a specific person. The main risk
is therefore an adversary obtaining an invite (forwarded, a seized phone, a
monitored chat), claiming it, and learning that the inviter uses Lantern and
what her IP is.

The design defends against that with a single-use secret the server never
sees, a matching code both people compare, inviter approval before any
traffic flows, and visible "already claimed" warnings. It also keeps the
social graph off the server so a breach or legal demand cannot reveal it.

Out of scope: an adversary with full read-and-modify control of the channel
the invite is sent over. The app recommends comparing codes on a voice call or
a different app; that defeats it, but v1 does not enforce it.

## Section 1: Invite and pairing flow

### Creating an invite

1. Sara taps "Invite someone to help me". Her app generates a 32-byte random
   secret `s`.
2. The app registers `H(s)` (SHA-256) with lantern-cloud, authenticated by
   its device ID, with an expiry of 48 hours. It also registers the public
   half of an **approval keypair** derived from `s`
   (`Ed25519(seed = HKDF(s, "lantern-helper-invite-v1/approve"))`), so the
   server can later verify an approval without ever learning `s`.
3. The link is `https://lantern.io/help/#<base64url(s)>`. The secret is in the
   URL fragment, so browsers and link-preview fetchers never send it to any
   server.
4. Sara shares it through the OS share sheet (Telegram, WhatsApp, Signal,
   email, SMS) or shows it as a QR code. Every channel gets the same preview:
   "Sara wants your help staying online."

### Claiming

The volunteer opens the link:

- **Lantern installed:** the link opens the app at "Sara asked for your help".
- **Not installed:** the landing page keeps `s` in the browser, offers the
  install, and after install a "Continue" button reopens the link in the app.
  It does not use third-party deferred deep-link services, which would receive
  the secret. The page also offers "help from this browser now" (see Section 3).
- **In person:** the volunteer's app scans Sara's QR code.

The volunteer's app:

1. Creates a device keypair (Ed25519) if it has none.
2. Derives `K = HKDF-SHA256(s, info = "lantern-helper-invite-v1")`.
3. Encrypts the volunteer's display name with `K` (AEAD).
4. Sends lantern-cloud a claim: `H(s)`, its public key, its device ID and the
   encrypted name.

lantern-cloud records a **pending claim** if the invite exists, is unexpired
and unclaimed. It cannot read the name. A second claim on the same invite is
refused with "already claimed", and the claiming app shows that prominently.

### Matching code and approval

1. Sara's app fetches the pending claim and decrypts the volunteer's name with `K`.
2. Both apps compute the matching code locally:
   `code = truncate(HMAC-SHA256(K, "code" || volunteer_public_key))`, rendered
   as four digits or four emoji. The server cannot compute it, because it
   never has `s`.
3. Sara and the volunteer compare codes. The app suggests a call or a
   different app where possible.
4. Sara taps **Approve** or **Reject**. Approval carries a signature by the
   approval key over `"approve" || H(s) || volunteer_public_key`, which
   lantern-cloud checks against the registered public key. Only a device
   holding `s` can produce it, and Sara's device must also be the one that
   created the invite.
5. On approval, lantern-cloud creates a random 128-bit **pairing ID**, marks the
   invite used, and stores `pairing ID → (inviter device or account, volunteer device)`.
   No names are stored.
6. Sara's display name reaches the volunteer the same way: encrypted with `K`,
   relayed once as an opaque blob, then deleted from the server.

### When a link leaks

An adversary who claims first produces a code Sara's real friend never saw.
The friend's app says "this invite was already claimed". Sara rejects the
pending claim, the invite is burned, and she sends a new one.

### Lifecycle

- Unclaimed invites expire after 48 hours. Rejected and expired invites cannot
  be reused.
- Either side can end a pairing at any time. That deletes its server record and
  revokes access (Section 2).
- If the inviter has a Lantern account, the pairing binds to the account and
  works on every device signed in to it. Otherwise it binds to the device.
- Display names are stored only on the two devices, encrypted with the OS keystore.

### Limits

| Who | Limit |
|---|---|
| Inviter | 20 pending invites, 20 active pairings, 20 new invites per device per day |
| Volunteer | 20 pairings; still at most 10 concurrent clients (`MaxClientsPerPeer`) |
| Claims | Rate-limited per volunteer device and per source IP |

All limits are configuration and are expected to change after the pilot.

## Section 2: Routing

### Pairing tickets

lantern-cloud issues short-lived signed **pairing tickets**: "device D may use
pairing P until T". Lifetime is 15 minutes. freddie and the volunteer's proxy
verify tickets with lantern-cloud's public key and need no database. Revocation
means lantern-cloud stops issuing tickets for the pairing and pushes a
revocation notice where it can.

### Direct mode

- A volunteer's peer route gets an **audience**: the set of its pairing IDs,
  plus `everyone` if "help others too" is on.
- When Sara's app fetches config (`/v1/config-new`), lantern-cloud always
  includes her paired volunteers' online routes. They are added separately from
  the bandit's selections, so her helpers do not depend on exploration weights.
- A route whose audience does not include `everyone` is left out of the bandit
  catalog. A route that includes it enters the pool as today.
- The per-peer client cap reserves slots for paired users first, so the pool
  cannot crowd them out.
- Paired clients authenticate to the volunteer's samizdat inbound with
  **per-pairing credentials** that lantern-cloud issues to both sides and
  rotates on the ticket lifetime. This needs no change to the samizdat
  protocol, and revoking a pairing simply stops its credentials being renewed.

### Unbounded

freddie gains **rooms**:

- A volunteer's widget announces into each of its pairing rooms, presenting a
  ticket for each.
- Sara's client subscribes to her pairing rooms (with tickets) as well as to
  the general pool.
- Genesis messages addressed to a room go only to that room's consumers. A
  volunteer that helps others too also broadcasts as today.
- The volunteer gives a waiting room consumer the next free slot before any
  pool consumer.
- freddie verifies tickets with the public key and learns only opaque pairing IDs.

### Client behavior

Sara's client treats a paired volunteer like any other route: measured, ranked
and failed over. If the volunteer is offline or blocked, she falls back to
normal Lantern. A pairing adds capacity and is never a single point of failure.

### Revocation

lantern-cloud stops issuing tickets for the pairing. In direct mode the route
drops from Sara's config at the next fetch, and the volunteer's inbound rejects
her pairing credentials once they expire. In Unbounded, freddie drops the room membership.
Access ends within one ticket lifetime (15 minutes) at most, and within seconds
where a revocation can be pushed.

## Section 3: Volunteer experience and marketing

### Mode

The volunteer's app uses direct mode whenever it can open a port, and falls
back to Unbounded otherwise. This is the same automatic fallback Action Mode
already has. There is no mode picker in v1; Advanced stats shows which mode is
active.

### Helping

- One "You're helping" row per paired person, by name (decrypted on-device),
  with daily totals: data relayed and time active.
- No live "Sara is online now" indicator. It would expose her activity
  patterns to anyone holding the volunteer's phone.
- "Help others too" is shown alongside, on by default, with one sentence on
  what it does and how the allowlist protects the volunteer.
- Advanced stats works as today, counting paired and pool traffic together.

### Browser fallback

The landing page can run Unbounded in the browser immediately, scoped to the
pairing's room once approved, for as long as the tab is open. It prompts to
install the app for always-on help. The browser cannot do direct mode.

### Inviter side

Sara sees her helpers with online or offline status, can revoke any of them,
and can invite another. The app nudges her toward a second helper, since one
friend's computer is a single point of failure.

### Marketing hooks

1. **The invite is the ad.** The landing page tells the human story and leads
   to an install with a reason to keep the app running.
2. **Volunteering earns Pro** (see "Pro rewards" below).
3. **A share card** for social media ("I'm keeping someone in Iran online with
   Lantern") that links to general volunteering. It never includes the paired
   person's name or anything that identifies her.
4. **Pro offers at natural moments,** such as an earned trial ending, never
   during onboarding.

### Pro rewards

Rewards are earned by helping, not by creating an account, so pairing with a
second phone of one's own earns nothing.

1. **Welcome week.** One week of Pro once the volunteer has an approved
   pairing, has been helping for 24 hours, and the paired person has connected
   through them from a censored country.
2. **Ongoing.** One more week for every 30 active days. A day counts when the
   volunteer was online for at least 6 hours. Capped at 12 weeks a year.
3. **Claiming.** Volunteering needs no account. Claiming a reward does: the app
   shows "you've earned a free week — create an account to claim it". This is
   where volunteers are asked for an account (and an email for marketing), and
   it lets the Pro survive a reinstall. Unclaimed weeks wait on the device.
4. **Abuse guard.** Only pairings whose paired person connects from a censored
   country count, and at most 3 pairings per volunteer count toward rewards. A
   volunteer can still help up to 20 people.

lantern-cloud checks eligibility without learning identities: the pairing
exists, the volunteer's heartbeats report its uptime, and the paired client's
country comes from its IP at config fetch. Only the outcome ("eligible for a
week") is stored against the volunteer, not which pairing or country earned it.
Rewards are granted through the existing referral-reward mechanism.

## Section 4: Abuse, privacy and testing

### Threats

| Threat | Mitigation |
|---|---|
| Leaked or forwarded invite | Matching code, inviter approval, "already claimed" warnings, single use, expiry |
| Guessing or enumerating invites | 256-bit secret; the server sees only its hash; rate limits |
| Adversary as volunteer collecting IPs | Can only pair through an invite the inviter approves. Pool volunteers already see client IPs; that is unchanged |
| Adversary as inviter harvesting volunteer IPs to block | Invite and pairing caps. Direct-mode IPs are already discoverable by any client they serve |
| Paired user abusing the volunteer's connection | The existing allowlist applies, with no bypass for paired users; the volunteer can revoke |
| Farming Pro rewards with self-pairings | Rewards require the paired person to connect from a censored country; at most 3 pairings count; yearly cap |
| Inviter's phone seized | Helper names encrypted at rest; a "remove all helpers" action |
| lantern-cloud breach | Only opaque pairing IDs and device IDs; rotate the ticket key |
| Ticket key compromise | An attacker could join rooms or use routes, but learns no identities; rotate the key and reissue |

### Telemetry

No logs or traces may tie a pairing ID to an IP address, a display name or an
account. Spans for invite, claim and approval record outcomes and timings only.

### Testing

- **Crypto:** shared test vectors so the Go (lantern-cloud, radiance) and Dart
  (app) implementations derive identical keys, matching codes and approval MACs.
- **State machine:** invite → claim → approve / reject / expire → revoke,
  including two claims racing for one invite.
- **Tickets and credentials:** ticket issuance, expiry and verification in
  freddie; per-pairing credential issuance, rotation and rejection after
  revocation at the volunteer's inbound.
- **Routing:** paired routes always included in the inviter's config; routes
  without `everyone` absent from the bandit catalog; paired users get slots first.
- **Rewards:** welcome-week and 30-day eligibility, the 6-hour active-day rule,
  the censored-country and 3-pairing conditions, the yearly cap, and claiming
  an unclaimed week after creating an account.
- **Production pilot checks:** a deliberately leaked link claimed first is
  caught by the code comparison; revocation time is measured.

### Rollout

1. Implement behind a feature flag in production.
2. Pilot with Iranian users who already have friends abroad.
3. Start the volunteer marketing push once the pilot shows people pair and stay paired.

## Components to change

| Component | Change |
|---|---|
| lantern-cloud | Invite, claim, approve, revoke and pairing APIs; pairing tickets and per-pairing credentials; route audiences; config-new includes paired routes; reward eligibility and grants |
| freddie | Rooms keyed by pairing ID; ticket verification; scoped genesis delivery |
| radiance | Volunteer: pairings, tickets, room announcements, paired-first slots, per-pairing credentials on the samizdat inbound. Client: room subscription and paired routes |
| lantern (app) | Invite creation and sharing, QR, claim and approval UI with matching codes, helper lists, browser-to-app handoff, earned-reward prompts and claiming |
| Web | `lantern.io/help` landing page with the browser fallback |

## Open questions

- Whether to add a "discreet" link style without Lantern branding, if pilot
  partners say branded links put senders at risk.
