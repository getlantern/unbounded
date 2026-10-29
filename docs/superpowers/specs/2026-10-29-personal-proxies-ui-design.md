# Personal proxies: app UI rework

**Status:** approved 2026-10-29 · **Builds on:** [helper invites](2026-10-20-helper-invites-design.md), as shipped in getlantern/lantern#9229 · **Design source:** Derek's "Personal proxies: invite flow design" (getlantern/engineering#4042; Figma `hNlyYToB5TnX9SDBFDYJTq`, nodes 7960-2449 and 7960-2465)

## Why

#9229 shipped helper invites behind the `helper_invites` flag with interim UI: Settings → "Get help from a friend" for the person in a censored country, and a conditional "Help a friend" button on the Action Mode tab for the volunteer. Derek's design puts the feature where people choose how they connect, the server list, and lets them pick their friends as a location. This rework adopts that design, with the deviations below, and keeps everything underneath #9229 (pairing, links, QR, approval, routing) unchanged.

## Decisions

| Topic | Decision | Derek's design |
|---|---|---|
| Invite format | Links and QR codes, as shipped. The 43-character secret is never typed | Short typed code (his open question, answered by links) |
| Naming | "Personal proxy" for the person in a censored country; the volunteer keeps "helping" wording | "Personal proxy" on both sides |
| Inviter entry points | Server list menu and a VPN tab banner; the Settings entry is removed | Same |
| Selecting | One selectable "Your personal proxies" row; each friend listed under it for information only | One selectable row per friend |
| Approval | #9229's approve sheet, after comparing the four-digit code | "Your personal proxy is ready" dialog |
| Managing | A third "Personal Proxies" tab in Manage Private Locations | Same |
| Volunteer side | #9229's "You're helping" section, plus a permanent "Have an invite link?" row | A card and bottom sheet with live connected status, and a code field |
| Auto-enable Action Mode | Unchanged | Moved into the welcome dialog |

## 1. Inviter: entry points

- **Server list menu.** The bottom sheet opened from the server list (`server_selection.dart`, today Set up / Join / Manage Private Servers) gains **"Invite a Personal Proxy"** while `helperInvitesAvailableProvider` is true. "Manage Private Servers" becomes **"Manage Private Locations"**.
- **VPN tab banner.** In the slot the Pro upsell uses, shown while helper invites are available and the device has no pairings as an inviter, and only after the view has loaded (it must not unlock the keystore to decide). A device that has never used helper invites cannot have pairings, so it shows the banner without loading anything. Copy: "Get a personal proxy" / "A friend abroad can keep you connected." Tapping it opens the invite screen. If the Pro upsell would also show, the upsell wins and the banner waits.
- **Settings.** The "Get help from a friend" entry and its `helperEntryVisibleProvider` gate are removed. A device that still has proxies or invites while the flag is off reaches them through Manage Private Locations, which shows the tab whenever there is something to manage (section 3).
- **Invite screen.** #9229's invite and share flow is reused, titled "Invite a Personal Proxy". The share message the friend receives keeps its "help me stay online" wording.
- **Approval.** Unchanged: the approve sheet after comparing the four-digit code.

## 2. Inviter: selecting personal proxies

- **Server list.** The "Private Servers" tab becomes **"Private Locations"**. When the device has at least one pairing as an inviter, the tab starts with a **"Personal proxies"** section: one selectable row, "Your personal proxies", then each friend by name with an online/offline label. The friend rows are not tappable. The row is disabled while no friend is online. The section is absent with no pairings.
- **Selecting.** Choosing the row connects through `ServerLocationType.personalProxy` with radiance's fixed group tag (below). The VPN tab's Selected Location row then shows "Personal proxy". Smart Location keeps including friends' routes, as it does today.
- **radiance group.** radiance builds a url-test group over every outbound whose tag satisfies `IsHelperTag`, direct and Unbounded alike, each time the config changes. The group's tag is the constant `personal-proxies`. It is selectable by that tag but never listed among servers, and it exists only while at least one helper outbound does. Selecting it while it does not exist fails like selecting a vanished private server, and the app falls back to Smart Location.
- **Privacy.** The constant tag marks a device as having personal proxies wherever outbound tags are recorded. Every path that applies `RedactHelperTags` also redacts `personal-proxies` (to "helper"), and it is listed beside `helper-` tags in the issue-report archive filter.

## 3. Inviter: managing

**Manage Private Locations** gains a third tab, **"Personal Proxies"**, beside My Servers and Joined Servers. It replaces #9229's GetHelp screen, which is removed:

- each friend: name, online/offline, **Rename** and **Remove**. Rename stores a nickname on this device only, in the sealed helper state, shown instead of the name the friend's app sent; Remove ends the pairing as GetHelp did;
- pending invites, with **Cancel**;
- **Invite another**, opening the invite screen;
- the keystore-reset notice and the "remove all" action from GetHelp.

The tab shows while helper invites are available or the device has proxies or invites (the rule `helperEntryVisibleProvider` implements today). The screen keeps GetHelp's lifecycle-gated polling while the tab is visible.

## 4. Volunteer side

- #9229's "You're helping" section is unchanged: names, today's totals, "Help others too". No live connected status, per the base spec.
- The Action Mode tab gets a permanent row, **"Have an invite link?"**, opening HelpFriend with a field to paste a link into (accepting `https://lantern.io/help/#…`, `/help#…` and `lantern://help#…`, normalized as the deep-link path does). It replaces the conditional "Help a friend" button. The row is hidden where `helperAutoStartAllowed(CountryCode.current)` is false (unknown or censored country) and in stealth builds.

## Out of scope

- Selecting an individual friend (per-pairing tags from lantern-cloud and per-pairing ticket filtering on the shared Unbounded outbound). Possible later.
- Derek's Action Mode auto-enable change, and engineering#4043 (choosing countries to help).
- The help.lantern.io landing page (plan 5).

## Testing

- radiance: the group appears with helper outbounds and disappears without them; selecting it routes only through helper outbounds; `personal-proxies` is redacted on every path `helper-` tags are.
- App: widget tests for the menu entry, banner visibility rules, the Personal proxies section (selectable row, disabled when offline, absent without pairings), the Manage tab (rename persists across restarts, remove, cancel, invite another), the paste row (visibility by country, link normalization), and removal of the Settings entry and GetHelp.
- Stealth: nothing new is reachable in a stealth build.
