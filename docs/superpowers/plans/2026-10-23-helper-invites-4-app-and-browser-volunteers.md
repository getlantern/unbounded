# Helper Invites, Plan 4: App and Browser Volunteers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put helper invites in people's hands. Sara creates an invite in the Lantern app and shares it as a link or QR code. Her friend claims it in the app, both compare a four-digit code, and Sara approves. The friend's app then helps her automatically, direct mode where it can and Unbounded otherwise. Each side lists the other, can end the pairing, and the volunteer chooses whether to help others too. The browser gets the primitives the plan 5 landing page needs to volunteer from a tab.

**Architecture:** The device-side cryptography lives in one Go package, `common/helperinvite` in broflake, checked against lantern-cloud's golden vectors. The app's Go core (`lantern-core/helper`) and the browser's wasm widget both use it. In the app, `lantern-core/helper` is a small engine. It holds the invite secrets, the device key and the display names in one state file, sealed with a key from the OS keystore that Dart passes in. It drives the plan 1 HelperService API through a new radiance IPC relay, so lantern-cloud requests still go through kindling and carry the device ID, but radiance only ever sees hashes, public keys and encrypted name blobs. Dart reaches the engine through one bridge call, `helperCall(op, argsJSON)`, and draws the screens. lantern-cloud adds three things: the `helper_invites` and paired-volunteer feature flags in `/v1/config-new`, an online status per helper in `ListPairings`, and CORS on `/v1/helper/` for `https://help.lantern.io` only. It also lets volunteers reach their endpoints without the flag.

```mermaid
sequenceDiagram
    autonumber
    participant SD as Sara's app<br/>Dart + lantern-core/helper
    participant SR as Sara's radiance<br/>helper/relay.go
    participant API as lantern-cloud<br/>helper_handler.go
    participant VR as Ali's radiance
    participant VD as Ali's app<br/>Dart + lantern-core/helper

    SD->>SD: engine.go create_invite: s, H(s), approval key
    SD->>SR: IPC /helper/relay op=invites
    SR->>API: POST /v1/helper/invites {invite_hash, approval_pubkey}
    Note over SD: share https://lantern.io/help/#s (Telegram, QR) ⚠️ s never leaves the two apps
    VD->>VD: engine.go claim: K_v(s) seals "Ali", code = HMAC(K, pub)
    VD->>VR: IPC /helper/relay op=invites/claim
    VR->>API: POST /v1/helper/invites/claim
    SD->>API: (via relay) invites/get → claimed, volunteer blob
    Note over SD,VD: both show the same 4-digit code; compared on a call
    SD->>API: (via relay) invites/decide {approval_sig, inviter_blob = K_i(s) seals "Sara"}
    API-->>SD: pairing_id
    VD->>API: (via relay) pairings → new pairing + inviter_blob (once)
    Note over VD: opens "Sara" with K_i of a pending claim;<br/>Home starts sharing (direct, else Unbounded)
    VD->>API: config-new → Features[unbounded] for paired volunteers
```

**Tech Stack:** Go (broflake, lantern-cloud with protobuf/gRPC via buf, sqlc v1.30.0, testify and `testdb`; radiance with koanf settings and kindling; lantern-core with cgo FFI and gomobile), `golang.org/x/crypto` (HKDF, ChaCha20-Poly1305), Flutter/Dart (Riverpod 3 with riverpod_generator, auto_route, i18n_extension with gettext `.po` files, share_plus, qr_flutter, mobile_scanner, app_links, and a new dependency, flutter_secure_storage), Kotlin and Swift method-channel handlers.

**Spec:** `docs/superpowers/specs/2026-10-20-helper-invites-design.md` (unbounded repo, branch `docs/helper-invites-spec`): Section 1 (all of it), Section 3 ("Mode", "Helping", "Browser fallback", "Inviter side"), Section 4 ("Threats", "Telemetry", "Testing", "Rollout"), "Components to change" and "Open questions".

**Plan series:** 1 Pairing core (lantern-cloud #3660, merged) · 2 Direct-mode routing (lantern-cloud #3675, radiance #698, merged) · 3 Unbounded rooms and ticket-key rotation (in progress: lantern-cloud #3698 merged, unbounded #465 merged, lantern-box #380, radiance Tasks 12–14) · **4 App and browser volunteers (this plan)** · 5 Landing page and rewards.

## Open questions for the user

Each of these blocks one task at most. The plan implements the recommendation. A different ruling changes only the task named.

1. **Per-person data relayed (blocks the "You're helping" totals, Task 14).** The spec wants one row per person with that person's daily data relayed and time active. Neither radiance nor the volunteer's widget can tell which person a session belongs to today. Direct mode would need samizdat to expose the short ID a session used, lantern-box to carry it on the connection event, and lantern-cloud to say which pairing each short ID belongs to. Unbounded would need freddie to tell the producer which room a consumer matched. That is five repos of plumbing.
   - **Recommendation:** ship v1 rows with only the person's name and "helping since" date. Show today's time active and data relayed once, in the section header, for everything this device relayed today, paired and pool together, as Advanced stats counts it.
   - **No number appears next to any name**, so the section totals can never be read as one person's.
   - Write the per-pairing attribution as a separate plan (4b) after the pilot.
2. **The inviter's name before approval (Task 14 copy; spec amended).** The spec has the link open at "Sara asked for your help", but the link carries only `s`, and plan 1 delivers Sara's name only after she approves, as an encrypted blob.
   - **Recommendation:** before approval, say "A friend asked for your help". Use the name from approval on. Putting the name in the link would leak it wherever the link is forwarded or seized, and the share message that travels with the link already names her.
3. **Account-bound pairings: decided.** The user approved device-bound pairings for the pilot on 2026-10-23, with account binding in a later plan. See "Spec amendments".
4. **Where the crypto runs (Tasks 4 and 9; spec amended).** The spec's testing section names "the Go (lantern-cloud, radiance) and Dart (app) implementations".
   - **Recommendation:** implement it once, in Go, in broflake's `common/helperinvite`, used by both the app's Go core and the browser's wasm. Check it against lantern-cloud's vectors plus new name-blob vectors. This adds no Dart crypto dependency, and the invite secret never enters radiance, which on Windows runs as a system service.
5. **The OS keystore (Task 12).** "Encrypted with the OS keystore" needs a native dependency. The plan uses flutter_secure_storage to hold one 32-byte key, which seals the engine's state file. Linux needs libsecret and a running keyring.
   - **Recommendation:** add flutter_secure_storage. On a device where the keystore fails, such as Linux with no keyring, helper invites show "can't store names securely on this device" and stay off. Names are never stored without the keystore.
   - If the keystore later returns a different key, the old state is kept as `state.bin.bak` and the UI reports `keystore_reset` (Task 9).
6. **Stealth builds (Tasks 12–14).** Stealth artifacts remove identifying UI, app links and the QR scanner.
   - **Recommendation:** helper invites are off in stealth builds (`AppBuildInfo.enableHelperInvites = !suppressIdentifyingFeatures`). The Android `/help` link filters are also left out of stealth manifests, through a manifest placeholder (Task 14).
7. **Browser volunteers' identity (Tasks 3 and 5, and plan 5).** The spec leaves this open. Volunteer tickets come from `IssueTickets`, which authenticates by device ID.
   - **Recommendation, the smallest secure design:**
     - Browser volunteers run only on a dedicated origin, `https://help.lantern.io`. It has a strict CSP (`default-src 'self'`, with `connect-src` limited to the API, freddie and the Unbounded egress), and no third-party JavaScript, analytics or tag managers. **This is a hard rule for plan 5's page.**
     - That page generates a random device ID, `web-` plus 32 hex characters, and a device-key seed, and keeps both in that origin's `localStorage`.
     - It calls `/v1/helper/invites/claim`, `/v1/helper/pairings`, `/v1/helper/pairings/end` and `/v1/helper/tickets` directly. CORS allows only `https://help.lantern.io` (Task 3).
     - The wasm widget computes the claim and the code (Task 5) and joins the pairing's room with the tickets.
     - Leaking the browser's device ID has the same consequence as leaking an app's (spec threat model, "Leaked volunteer ticket"). The dedicated origin and CSP are what keep other scripts from reading it.
     - v1 has no transfer of a browser pairing to the app. Someone who installs the app later asks for a new invite. The page offers "stop helping from this browser", which ends the browser pairing.
8. **What "online" means for a helper (Task 2).** Either the volunteer has a live direct-mode route, or its widget fetched volunteer tickets in the last 15 minutes, which a running widget does every 10. The server keeps one `seen_at` per volunteer device, in Postgres, refreshed at most every 5 minutes and swept after 48 hours. It never logs it. Task 2 records the Redis alternative and why Postgres was chosen.
   - The inviter can watch the volunteer's on and off pattern at about 5-minute resolution. That is new, and is added to the spec's threat model.
   - Known edge: a device that is both an inviter and a volunteer, with the VPN on and sharing off, reads as online, because its tunnel's ticket fetch also returns its volunteer tickets. This is accepted.
9. **Cancelling an invite (Tasks 9 and 13).** HelperService has no "delete invite" call.
   - **Recommendation:** keep a local tombstone. A cancelled invite stays in the sealed state, hidden from the UI, until it expires. If anyone claims it later, the next refresh rejects the claim automatically, so a claimant never waits on an approval that cannot come. A claimed invite is rejected at once on cancel. No RPC is added.
10. **The spec's open question, discreet links.** **Recommendation:** keep branded `lantern.io/help` links for the pilot, and revisit if pilot partners report risk. No task changes.

## Spec amendments (applied to the spec in this branch)

These are the places where this plan departs from the original spec. Each is now written into the spec itself, on `docs/helper-invites-spec`, so the spec stays the binding authority. The reviewer agreed with each, and the user approved executing a plan that lists them. Revert the spec edits if the user objects to any.

| Original spec text | Now in the spec | Why |
|---|---|---|
| Decisions: "What a pairing binds to: The inviter's device; the inviter's account when they have one". Lifecycle: "the pairing binds to the account" | Device-bound; account binding deferred to a later plan. **User-approved 2026-10-23.** | No verified account identity on the helper path, and names live on one device |
| Section 1, Claiming: "the link opens the app at "Sara asked for your help"" | "A friend asked for your help" until approval, then her name | The link carries only `s`; the name arrives sealed, after approval (open question 2) |
| Section 4, Testing: "the Go (lantern-cloud, radiance) and Dart (app) implementations" | One Go device-side implementation (broflake `helperinvite`), used by the app's Go core and the browser's wasm | One codebase against the vectors; no Dart crypto dependency (open question 4) |
| Plan 1 rule: every helper endpoint requires the `helper_invites` flag | `ClaimInvite` passes the gate with a live invite. `ListPairings`, `EndPairing` and `IssueTickets` pass it for a device holding a claim or a pairing | Volunteers abroad need no flag, and their countries never see the inviter UI (Task 1). Recorded in the spec's threat model as "Flag-free volunteer access" |
| Threat model | Rows added: "Helper presence visible to the inviter" (Task 2) and "Flag-free volunteer access" (Task 1) | New exposures this plan creates |

## Global Constraints

### Copied from the spec (verbatim; every task obeys them)

**Privacy and telemetry**

- "No logs or traces may tie a pairing ID to an IP address, a display name or an account. Spans for invite, claim and approval record outcomes and timings only."
- "Display names are stored only on the two devices, encrypted with the OS keystore."
- "Server knowledge | Blind: the server stores opaque pairing IDs and device IDs, never names or the invite secret"
- "Client-side outbound tags | An inviter's config carries `helper-` outbound tags: per-device HMACs, stable until a ticket-key rotation, carrying no IDs. On the device they mark it as a paired inviter, and they let anything that records outbound tags (session history, client telemetry) link that device's sessions. The server records nothing. Clients must not export `helper-` tags in telemetry or reports"
- "No live "Sara is online now" indicator. It would expose her activity patterns to anyone holding the volunteer's phone."
- "The link is `https://lantern.io/help/#<base64url(s)>`. The secret is in the URL fragment, so browsers and link-preview fetchers never send it to any server."
- "It does not use third-party deferred deep-link services, which would receive the secret."

**Copy and UX**

- "Sara taps "Invite someone to help me"."
- "Sara shares it through the OS share sheet (Telegram, WhatsApp, Signal, email, SMS) or shows it as a QR code. Every channel gets the same preview: "Sara wants your help staying online.""
- "A second claim on the same invite is refused with "already claimed", and the claiming app shows that prominently."
- "Both apps compute the matching code locally … rendered as four digits or four emoji." This plan uses four digits.
- "Sara and the volunteer compare codes. The app suggests a call or a different app where possible."
- "Sara taps **Approve** or **Reject**."
- "The friend's app says "this invite was already claimed". Sara rejects the pending claim, the invite is burned, and she sends a new one."
- "One "You're helping" row per paired person, by name (decrypted on-device), with daily totals: data relayed and time active." Open question 1 places the totals on the section for v1.
- ""Help others too" is shown alongside, on by default, with one sentence on what it does and how the allowlist protects the volunteer."
- "Sara sees her helpers with online or offline status, can revoke any of them, and can invite another. The app nudges her toward a second helper, since one friend's computer is a single point of failure."
- "The volunteer's app uses direct mode whenever it can open a port, and falls back to Unbounded otherwise. … There is no mode picker in v1; Advanced stats shows which mode is active."
- "Inviter's phone seized | Helper names encrypted at rest; a "remove all helpers" action"

**Flag gating and limits**

- "Rollout | Production pilot behind a feature flag; no staging phase", and "Implement behind a feature flag in production."
- From plan 1: "Every endpoint except `GET /v1/helper/ticket-key` requires `X-Lantern-Device-Id` and the `helper_invites` feature flag". **Superseded by Task 1:** the device-ID requirement stands, but the flag rule is replaced by "Gate without the flag" in the wire table. Volunteers pass with an invite, a claim or a pairing, and only inviter endpoints need the flag.
- The limits table:

| Who | Limit |
|---|---|
| Inviter | 20 pending invites, 20 active pairings, 20 new invites per device per day |
| Volunteer | 20 pairings; still at most 10 concurrent clients (`MaxClientsPerPeer`) |
| Claims | Rate-limited per volunteer device and per source IP |

- "Unclaimed invites expire after 48 hours. Rejected and expired invites cannot be reused."

### Plan rules

- **Telemetry.** No log line, span attribute, span event, span status, metric or crash report anywhere in this plan may contain any of these:
  - an invite secret, link or link fragment;
  - an invite hash;
  - a device key or seed;
  - a display name;
  - a matching code;
  - a pairing ID;
  - a ticket;
  - a `helper-` tag;
  - the state key;
  - a count of invites, claims, pairings or helpers.

  This holds in Dart, lantern-core, radiance and lantern-cloud. Errors that cross into logs are fixed codes (`already_claimed`, `disabled`, …). The radiance relay is registered without the IPC tracer, because the tracer records error bodies.
- **The UI gate.** Nothing about helper invites shows in a stealth build (`AppBuildInfo.enableHelperInvites`). Beyond that, the two sides are gated differently:
  - **The inviter's UI** (Settings → "Get help from a friend") shows only when `Features["helper_invites"]` is true. The in-app "Help a friend" entry, with its QR scanner, shows when the flag is on or the device already helps someone. A first-time volunteer abroad uses the phone's camera app instead (Task 14). The server decides that flag from `X-Lantern-Feature-Override` or the `helper_invites` flag.
  - **The volunteer's UI** (a `/help` link, the claim screen, "You're helping") needs no flag. A volunteer abroad reaches it through an invite, and the server lets a device holding a live invite, a claim or a pairing through (Task 1). So nobody in a volunteer's country is shown the inviter UI just so that volunteers can help.
- **The keystore is touched only after use.** The app never reads or creates the state key until the user creates or claims an invite, or opens a helper link. A local preference, `helper_invites_used`, records first use. This keeps Keychain prompts and libsecret off every other user's path.
- **Dependency bumps.** After every `go get module@version`, run `go mod tidy`, check `git diff` for unexpected changes, and commit `go.mod` and `go.sum` together in the same commit as the bump. broflake is consumed by pseudo-version and is not tagged. radiance is consumed by pseudo-version in lantern.
- **sqlc (lantern-cloud).** After any query or migration change, run `cd cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`, not `make sqlc`. Commit the generated `cmd/api/dal/` files with the change.
- **proto (lantern-cloud).** After editing `helper.proto`, run `make proto`, then revert every generated file that does not belong to `helper.proto`. Keep only:
  - `cmd/api/apipb/services/helper.pb.go`
  - `helper_grpc.pb.go`
  - `cmd/backoffice/src/gen/services/helper_pb.ts`
  - `helper-HelperService_connectquery.ts`
  - `openapiv2/services/helper.swagger.json`
- **Tests (lantern-cloud).**
  - DB-backed tests run as `bin/testsetup go test -tags=test,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme ./cmd/api/ -run '<pattern>' -v`. This document writes that as `$LC_TEST 'Pattern'`.
  - New `cmd/api` test files that use the peer or helper test helpers carry `//go:build !codeanalysis`.
  - The router package runs as `go test ./router/ -run '<pattern>' -v`.
- **Tests (broflake).** CI runs `go test -race ./...` and `GOOS=js GOARCH=wasm go build -o /dev/null ./cmd`. Every broflake task passes both.
- **Tests (radiance).** `go test -race ./helper/ ./peer/ ./backend/ ./ipc/`, then `make test`.
- **Tests (lantern).**
  - Go: `CGO_ENABLED=1 go test -tags with_gvisor,with_quic,with_wireguard,with_utls,with_grpc ./lantern-core/...`
  - Dart: `make pubget && make gen`, then `flutter analyze` and `flutter test`.
  - FFI bindings are regenerated with `make macos-ffi-headers && make ffigen` and never hand-edited.
- **Model.** Use the strongest available model for every task and every review. This plan touches a security feature in four repos, and its crypto must stay byte-exact.
- **PRs.** Open every PR with the `skills:create-pr` skill. A hook blocks a bare `gh pr create`.
- **[Human]** marks a step that needs a person: merging, migrations, deploys, flag changes, web changes on lantern.io, releases and store submissions.

### Wire formats (normative)

These rows come from plan 1 and are restated so each task is self-contained. Plan 1's table and `cmd/api/pairing/testdata/vectors.json` are normative for every derivation.

| Name | Definition |
|---|---|
| `s` | 32 random bytes; in links, base64url without padding |
| `invite_hash` | `SHA-256(s)` |
| `K` | `HKDF-SHA256(ikm=s, salt=empty, info="lantern-helper-invite-v1/key")`, 32 bytes |
| Approval keypair | Ed25519 from seed `HKDF-SHA256(s, info="lantern-helper-invite-v1/approve")` |
| Name keys | `K_v = HKDF(s, "lantern-helper-invite-v1/volunteer-name")`, `K_i = HKDF(s, "lantern-helper-invite-v1/inviter-name")` |
| Name blob | ChaCha20-Poly1305 under `K_v` (the volunteer's name) or `K_i` (the inviter's), empty associated data, blob = 12-byte random nonce ‖ ciphertext. **New in this plan:** the plaintext is the display name as UTF-8, trimmed of surrounding white space, 1–200 bytes, with no control characters and no format characters (Unicode Cf) other than ZWNJ and ZWJ (`helperinvite.CleanName`). The blob is at most 228 bytes, under lantern-cloud's 512. |
| Matching code | `HMAC-SHA256(K, "code" ‖ volunteer_pubkey)`, first 4 bytes big-endian, mod 10000, 4 digits zero-padded |
| Approval signature | Ed25519 by the approval key over `"approve" ‖ invite_hash ‖ volunteer_pubkey` |
| Device key | Ed25519 from a 32-byte random seed kept in the app's sealed state, or in the browser's `localStorage`. Only its public half leaves the device. |
| Link | `https://lantern.io/help/#` + base64url(`s`). Parsers also accept `https://www.lantern.io/help#…`, `/help/#…`, the app's custom scheme `<APP_AUTH_SCHEME>://help#…` (normalized in Dart), and a bare 43-character base64url secret. |
| HelperService JSON | proto field names, bytes as standard base64 with padding, int64 as a JSON string (protojson). `HelperPairing` gains `online` (field 4, inviter rows only) and `created_unix` (field 5) in Task 2. |
| `Features["helper_invites"]` | In `/v1/config-new`: true when helper invites are enabled for the client (Task 1). It gates only the inviter's UI. |
| Paired-volunteer widget | In `/v1/config-new`: `Features["unbounded"]` is also true, and `Unbounded` widget config is sent, when the client advertises `unbounded`, the device volunteers in at least one pairing, and its country is known and not censored. The `helper_invites` flag is not needed (Task 1). |
| Gate without the flag | `ClaimInvite` passes the `helper_invites` gate without the flag; an unknown, used or expired invite then answers 409 as for any client. `ListPairings`, `EndPairing` and `IssueTickets` pass it without the flag for a device that is the volunteer in a pairing or holds a live claim. `CreateInvite`, `GetInvite` and `DecideInvite` always need the flag (Task 1). |
| radiance relay | IPC `POST /helper/relay` `{"op": <op>, "body": <JSON>}` → 200 `{"status": <int>, "body": <JSON on 2xx>, "error": <text otherwise>}`. The ops are `invites`, `invites/claim`, `invites/get`, `invites/decide`, `pairings`, `pairings/end`, and the local `tickets/refresh`. Anything else gets 400 (Task 6). |
| radiance activity | IPC `GET /helper/activity` → `{"day":"YYYY-MM-DD","active_seconds":<int>,"relayed_bytes":<int>}`, the device's local calendar day (Task 8) |
| `helperCall` | The bridge from Dart to lantern-core. It takes `op`, a string, and `args`, a JSON object string. It returns the operation's JSON result, or `{"error":"<code>"}`. The ops and codes are listed in Task 9. |

### Browser volunteer protocol (for plan 5's landing page)

This is the sequence the landing page runs. Plan 4 builds and tests every server and wasm piece it uses. The `lanternHelperInvite` functions take the bare base64url secret (`ParseLink` also accepts a full link).

1. **Origin.** The page is served only from `https://help.lantern.io`, with a strict CSP (`default-src 'self'; connect-src 'self' https://api.iantem.io https://freddie.iantem.io wss://unbounded.iantem.io; script-src 'self' 'wasm-unsafe-eval'`) and no third-party JavaScript. The hosts are production's (`unbounded_discovery_srv` and `unbounded_egress_addr` in lantern-cloud's `unboundedWidgetConfig`), and the page takes them from its build config. `https://lantern.io/help/` redirects there, and the fragment survives the redirect. CORS allows only this origin (Task 3).
2. **Identity.** `seed = localStorage["helper.seed"] ??= lanternHelperInvite.newSeed()`, and `device = localStorage["helper.device"] ??= "web-" + 32 random hex characters`, both on `help.lantern.io`.
3. **The secret.** On load, `secret = location.hash.slice(1)`. Then at once call `history.replaceState(null, "", location.pathname)`, so the fragment leaves the address bar and history.
   - Hold `secret` in memory.
   - Keep it in `sessionStorage["helper.secret"]` only until approval, so a reload in the same tab can carry on.
   - Never send it anywhere, and never put it in a query string.
4. **Claim.** `const {body, code} = lanternHelperInvite.claim(secret, seed, name)`. Then `POST https://api.iantem.io/v1/helper/invites/claim` with `body`, `Content-Type: application/json` and `X-Lantern-Device-Id: device`. A 409 means "this invite was already claimed". `api.iantem.io` is production's public API host; the page takes the host from its build config.
5. **Approval.** Show `code`. Every 30 s, `POST /v1/helper/pairings` with `{}`.
   - For a new pairing, **first** run `name = lanternHelperInvite.openInviterName(secret, pairing.inviter_blob)`. lantern-cloud delivers the blob exactly once.
   - **Then** delete `sessionStorage["helper.secret"]` and drop `secret`.
6. **Names are memory-only in the browser.** A name is never written to `localStorage`, `sessionStorage`, IndexedDB or a cookie. After a reload the page says "your friend".
7. **Tickets.** Every 10 minutes, `POST /v1/helper/tickets` with `{}`. Pass the volunteer tickets to the widget's `setRoomTickets(tickets)`, and call `setServePool(helpOthers)`.
8. **Stopping.** "Stop helping from this browser" sends `POST /v1/helper/pairings/end {"pairing_id": …}` and clears both `localStorage` keys.

### Repos, branches and worktrees (in execution order)

| Repo | Worktree | Branch | Base |
|---|---|---|---|
| lantern-cloud | `~/go/src/github.com/getlantern/lantern-cloud-helper-app` | `fisk/helper-app` | `origin/main` (451692a04 or later) |
| unbounded (`github.com/getlantern/broflake`) | `~/go/src/github.com/getlantern/unbounded-helper-app` | `fisk/helper-app` | `origin/main` (6679ccf or later) |
| radiance | `~/go/src/github.com/getlantern/radiance-helper-app` | `fisk/helper-app` | `origin/main`, **only after the gates in Task 6 Step 1**: lantern-box #380 merged and tagged, then plan 3's radiance PR (Tasks 12–14) switched to that tag and merged. Tasks 6–8 extend its `helper` package. |
| lantern | `~/go/src/github.com/getlantern/lantern-helper-app` | `fisk/helper-app` | `origin/main` (c56403559 or later) |

Two values come from earlier tasks: `UB_SHA`, the unbounded merge commit (Task 5 Step 7), and `RAD_SHA`, the radiance merge commit (Task 8 Step 7). The lantern Go tasks (9–11) bump to them.

## File Structure

| Repo | File | Responsibility |
|---|---|---|
| lantern-cloud | `cmd/api/helper_features.go`, `helper_features_db_test.go` | `helperFeatureFlags`, the paired-volunteer lookup |
| lantern-cloud | `cmd/api/helper_handler.go` (gate), `helper_handler_db_test.go`, `migrations/infra/000149_helper_claims_by_volunteer.*` | Volunteers without the flag: `helperFlagFreeHandlers`, `volunteerOrNotFound`, flag-off rate-limit buckets |
| lantern-cloud | `cmd/api/config.go` | Emits `Features["helper_invites"]` and the paired-volunteer widget |
| lantern-cloud | `cmd/api/migrations/infra/000150_helper_volunteer_presence.{up,down}.sql` | `helper_volunteer_presence` |
| lantern-cloud | `cmd/api/queries/infra/helper.sql`, `cmd/api/dal/*` | Presence queries |
| lantern-cloud | `cmd/api/apipb/proto/services/helper.proto` and generated files | `HelperPairing.online`, `created_unix` |
| lantern-cloud | `cmd/api/helper_handler.go`, `helper_presence_db_test.go` | Online status in `ListPairings`; presence in `IssueTickets` and the sweep |
| lantern-cloud | `router/middleware.go`, `router/middleware_test.go`, `cmd/api/main.go` | `CORSWith`, `RestrictedCORS`; the `/v1/helper/` rule for `help.lantern.io` |
| unbounded | `common/helperinvite/{helperinvite,names,link,claim,browser}.go`, `helperinvite_test.go`, `testdata/{lantern-cloud-vectors,names}.json` | Device-side crypto, link, claim and approval, a string API for wasm |
| unbounded | `clientcore/broflake.go`, `clientcore/ui_wasm_impl.go`, `cmd/helperinvite_wasm.go`, `cmd/client_wasm_impl.go` | `setRoomTickets`, `setServePool`, `lanternHelperInvite` |
| radiance | `helper/relay.go`, `helper/relay_test.go`, `helper/tickets.go` | The relay with its `onChange` hook, `Source.Invalidate` |
| radiance | `helper/activity.go`, `helper/activity_test.go` | `ActivityTracker` |
| radiance | `ipc/server.go`, `ipc/client.go`, `ipc/types.go`, `ipc/helper_test.go` | `/helper/relay`, `/helper/activity` |
| radiance | `backend/radiance.go`, `backend/peer_share.go`, `backend/radiance_test.go`, `peer/peer.go`, `peer/rotation_test.go` | Wiring; re-register on a serve-pool change |
| lantern | `lantern-core/helper/{engine,state,ops,errors}.go`, `engine_test.go`, `fakecloud_test.go` | The device-side engine |
| lantern | `lantern-core/core.go`, `lantern-core/ffi/ffi.go`, `lantern-core/mobile/mobile.go`, `lantern-core/helper_redaction_test.go` | `HelperCall`, the exports, the log-redaction guard |
| lantern | `android/.../handler/MethodHandler.kt`, `ios/Runner/Handlers/MethodHandler.swift`, `macos/Runner/Handlers/MethodHandler.swift` | `helperCall` on the method channel |
| lantern | `lib/lantern/{lantern_core_service,lantern_ffi_service,lantern_platform_service,lantern_service}.dart`, `lantern_generated_bindings.dart` | `helperCall` in Dart |
| lantern | `lib/core/models/available_servers.dart`, `lib/core/services/logger_service.dart`, `lib/core/utils/helper_tags.dart` | `helper-` tags hidden and redacted |
| lantern | `lib/features/helpers/{models/helpers_view.dart,provider/helpers_notifier.dart,helper_key_store.dart,helper_errors.dart}` | State, the keystore and errors |
| lantern | `lib/features/helpers/{get_help.dart,invite_share.dart,approve_helper_sheet.dart}` | The inviter's screens |
| lantern | `lib/features/helpers/{help_friend.dart,helping_section.dart}`, `lib/lantern_app.dart`, `lib/features/home/home.dart`, `lib/features/action_mode/{action_mode.dart,provider/share_notifier.dart}` | The volunteer's screens, links and auto-share |
| lantern | `lib/core/models/feature_flags.dart`, `lib/core/common/app_build_info.dart`, `lib/core/router/router.dart`, `lib/features/setting/setting.dart`, `lib/core/services/injection_container.dart`, `assets/locales/en.po`, `pubspec.yaml`, `android/app/src/main/AndroidManifest.xml`, `linux/packaging/nfpm.yaml`, `.github/workflows/build-linux.yml` | Gating, routes, strings, deps and packaging |
| lantern | `test/features/helpers/*_test.dart`, `test/core/models/available_servers_helper_test.dart` | Dart tests |

---

### Task 1: lantern-cloud — helper features in `/v1/config-new`, and volunteers without the flag

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-app`

**Files:**
- Create: `cmd/api/helper_features.go`
- Create: `cmd/api/helper_features_db_test.go`
- Modify: `cmd/api/config.go` (the helper block at about line 555, and the features block at about line 780)
- Modify: `cmd/api/helper_handler.go` (`helperRequestContext`, `helperRequest`, `allowSource`, `ListPairings`, `EndPairing`, `IssueTickets`)
- Create: `cmd/api/migrations/infra/000149_helper_claims_by_volunteer.up.sql` and `.down.sql`. If `000149` is taken on `origin/main` when this runs, use the next free number, and renumber Task 2's migration after it.
- Modify: `cmd/api/queries/infra/helper.sql`, and the generated `cmd/api/dal/helper.sql.go`
- Modify: `cmd/api/helper_handler_db_test.go` (`TestHelperGateRunsBeforeBody`)

**Interfaces:**
- Consumes:
  - `helperInvitesEnabled(header, clientIP, country) bool` and `featureFlagHelperInvites` (`helper_handler.go`)
  - `startHelperLookup` and `helperOutboundsTimeout` (`helper_direct.go`)
  - the existing pairing lookup `dal.Queries.CountHelperPairingsForVolunteer`
  - `regions.IsCensoredCountry`
  - the test helpers `newHelperConfigNewFixture`, `newHelperTestService`, `newHelperTestRouter`, `helperPost`, `newTestInvite`, `claimBody` and `approveBody`
- Produces:
  - `func helperFeatureFlags(invitesOn, pairedVolunteer bool, clientCountry string) (helperInvites, forceUnbounded bool)`
  - `func (ls *lanternService) startHelperVolunteerLookup(ctx context.Context, timeout time.Duration, deviceID string) func() bool`
  - `var helperFlagFreeHandlers map[string]bool`, `helperRequestContext.flagOff bool`, and `func (s helperService) volunteerOrNotFound(p helperRequestContext, handler string) error`
  - the query `CountLiveHelperClaimsForVolunteer(ctx, volunteerDeviceID sql.NullString) (int64, error)`
  - On the wire: `Features["helper_invites"]`, and `Features["unbounded"]` with the `Unbounded` widget config for paired volunteers, with or without the flag. The app (Tasks 12–14) and radiance's widget gate read these.
  - The "Gate without the flag" behaviour in Global Constraints.

**Why volunteers need no flag (PL4):** if volunteers needed `helper_invites`, the flag would have to cover every country a volunteer might live in, and everyone there would see the inviter UI. Instead, the invite itself is the volunteer's credential.
- `ClaimInvite` runs for anyone with a live invite.
- Once a device holds a claim or a pairing, `ListPairings`, `EndPairing` and `IssueTickets` run for it.
- Creating, reading and deciding invites still need the flag, so only the pilot's inviters can invite.

**Abuse analysis** (this also adds the spec's "Flag-free volunteer access" threat row):
- Flag-off clients can now try claims with guessed invite hashes. With a 256-bit secret, guessing is hopeless.
- Flag-off requests get their own rate-limit buckets (prefix `flagoff:`), so they cannot drain the egress bucket the pilot's inviters share.
- Any device ID can call the three flag-free volunteer endpoints, and each call costs two indexed counts. `volunteerOrNotFound` charges the request to its source's `flagoff:` bucket before running them, as claims already are. That is 30 a minute per source, and 600 a minute shared through Lantern's egress. Volunteers' own polling (pairings every 30 s while waiting, tickets every 10 minutes) stays well inside it.
- A flag-off device with no claim or pairing gets the gate's 404 from the three volunteer endpoints. These answer 400 rather than 404 to an undecodable body, because their gate now runs after decoding. That reveals only that the endpoints exist, which is now intended.
- The paired-volunteer lookup in `/v1/config-new` now runs for every client that advertises `unbounded`, not only flag-on ones. That is one indexed count, bounded at 300 ms and detached from the trace, like the other helper lookups.

- [ ] **Step 1: Create the worktree**

```bash
cd ~/go/src/github.com/getlantern/lantern-cloud-helper-rooms
git fetch origin
git worktree add -b fisk/helper-app ~/go/src/github.com/getlantern/lantern-cloud-helper-app origin/main
cd ~/go/src/github.com/getlantern/lantern-cloud-helper-app
```

- [ ] **Step 2: Write the failing tests**

`cmd/api/helper_features_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getlantern/common"
	box "github.com/getlantern/lantern-box"
	"github.com/google/uuid"
	sagerNetJson "github.com/sagernet/sing/common/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
)

func TestHelperFeatureFlags(t *testing.T) {
	cases := []struct {
		name                   string
		on, volunteer          bool
		country                string
		wantInvites, wantForce bool
	}{
		{"flag off, not a volunteer", false, false, "DE", false, false},
		{"flag off, paired volunteer abroad", false, true, "DE", false, true},
		{"flag on, not a volunteer", true, false, "DE", true, false},
		{"paired volunteer abroad", true, true, "DE", true, true},
		{"paired volunteer in a censored country", true, true, "IR", true, false},
		{"paired volunteer, unknown country", false, true, "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv, force := helperFeatureFlags(c.on, c.volunteer, c.country)
			assert.Equal(t, c.wantInvites, inv)
			assert.Equal(t, c.wantForce, force)
		})
	}
}

// configNewForDevice is helperConfigNewFixture.configNew as deviceID.
func configNewForDevice(t *testing.T, f helperConfigNewFixture, deviceID string, protocols []string, overrides string) *common.ConfigResponse {
	t.Helper()
	body, err := json.Marshal(&common.ConfigRequest{
		DeviceID:  deviceID,
		UserID:    "10",
		ProToken:  "test-token",
		Platform:  "android",
		Backend:   "sing-box",
		Protocols: protocols,
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/config-new", io.NopCloser(bytes.NewReader(body)))
	r.Header.Set("x-lantern-client-country", "US")
	r.Header.Set("x-forwarded-for", "89.160.20.134")
	r.Header.Set("X-Lantern-App-Version", "7.2.3")
	if overrides != "" {
		r.Header.Set("X-Lantern-Feature-Override", overrides)
	}
	rec := httptest.NewRecorder()
	f.root.ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	cfg, err := sagerNetJson.UnmarshalExtendedContext[common.ConfigResponse](box.BaseContext(), rec.Body.Bytes())
	require.NoError(t, err)
	return &cfg
}

// TestConfigNewHelperFeatures checks that helper_invites reaches the client's
// Features, and that a paired volunteer that can run the widget gets it
// whatever the unbounded and helper_invites flags say.
func TestConfigNewHelperFeatures(t *testing.T) {
	f := newHelperConfigNewFixture(t)
	require.NoError(t, f.q.InsertHelperPairing(context.Background(), dal.InsertHelperPairingParams{
		PairingID: uuid.New(), InviterDeviceID: "inviter", VolunteerDeviceID: "volunteer-A"}))
	protos := []string{"samizdat", common.UNBOUNDED}

	stranger := configNewForDevice(t, f, "stranger", protos, "")
	assert.False(t, stranger.Features[featureFlagHelperInvites], "flag off: no inviter UI")
	baseline := stranger.Features[common.UNBOUNDED]

	flagged := configNewForDevice(t, f, "stranger", protos, featureFlagHelperInvites)
	assert.True(t, flagged.Features[featureFlagHelperInvites])
	assert.Equal(t, baseline, flagged.Features[common.UNBOUNDED], "an unpaired device keeps the unbounded flag's value")

	inviter := configNewForDevice(t, f, "inviter", protos, featureFlagHelperInvites)
	assert.Equal(t, baseline, inviter.Features[common.UNBOUNDED], "being an inviter does not turn the widget on")

	vol := configNewForDevice(t, f, "volunteer-A", protos, "")
	assert.False(t, vol.Features[featureFlagHelperInvites], "a volunteer is not shown the inviter UI")
	assert.True(t, vol.Features[common.UNBOUNDED], "a paired volunteer gets the widget without any flag")
	assert.NotNil(t, vol.Unbounded, "with its discovery and egress settings")

	noWidget := configNewForDevice(t, f, "volunteer-A", []string{"samizdat"}, "")
	assert.Equal(t, baseline, noWidget.Features[common.UNBOUNDED], "a client that can't run the widget is not forced")
}

// TestHelperVolunteerWorksWithoutTheFlag covers the gate without the flag: a
// live invite admits a claim, and a claim or pairing admits the volunteer
// endpoints; inviting still needs the flag.
func TestHelperVolunteerWorksWithoutTheFlag(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/invites", "inviter", body, true).Code)

	for _, path := range []string{"/v1/helper/pairings", "/v1/helper/tickets"} {
		assert.Equal(t, http.StatusNotFound, helperPost(t, mux, path, "stranger", map[string]any{}, false).Code, path)
	}
	assert.Equal(t, http.StatusNotFound, helperPost(t, mux, "/v1/helper/pairings/end", "stranger",
		map[string]any{"pairing_id": uuid.NewString()}, false).Code)
	_, other := newTestInvite(t)
	assert.Equal(t, http.StatusNotFound, helperPost(t, mux, "/v1/helper/invites", "stranger", other, false).Code,
		"inviting needs the flag")

	vol := bytes.Repeat([]byte{6}, 32)
	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/invites/claim", "volunteer-A", claimBody(s, vol), false).Code)
	assert.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/pairings", "volunteer-A", map[string]any{}, false).Code,
		"a device holding a claim polls for approval")
	assert.Equal(t, http.StatusNotFound, helperPost(t, mux, "/v1/helper/invites/get", "volunteer-A",
		map[string]any{"invite_hash": body["invite_hash"]}, false).Code, "reading invites needs the flag")

	w := helperPost(t, mux, "/v1/helper/invites/decide", "inviter", approveBody(s, vol), true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		PairingID string `json:"pairing_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/tickets", "volunteer-A", map[string]any{}, false).Code)
	assert.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/pairings/end", "volunteer-A",
		map[string]any{"pairing_id": resp.PairingID}, false).Code)

	s2, _ := newTestInvite(t)
	assert.Equal(t, http.StatusConflict, helperPost(t, mux, "/v1/helper/invites/claim", "volunteer-B", claimBody(s2, vol), false).Code,
		"an unknown invite reads as already claimed, as for any client")
}
```

In `cmd/api/helper_handler_db_test.go`, replace the body of `TestHelperGateRunsBeforeBody` after `mux := newHelperTestRouter(t, ls)` with:

```go
	// The volunteer endpoints are reachable without the flag, so their gate
	// runs in the handler, after decoding (see helperFlagFreeHandlers).
	flagFree := map[string]bool{"/v1/helper/invites/claim": true, "/v1/helper/pairings": true,
		"/v1/helper/pairings/end": true, "/v1/helper/tickets": true}
	flagOffWant := func(path string) int {
		if flagFree[path] {
			return http.StatusBadRequest
		}
		return http.StatusNotFound
	}
	for _, path := range []string{"/v1/helper/invites", "/v1/helper/invites/claim"} {
		w := helperPost(t, mux, path, "device-A", map[string]any{}, false)
		assert.Equal(t, flagOffWant(path), w.Code, "%s: flag off, invalid body", path)
		w = helperPost(t, mux, path, "device-A", map[string]any{}, true)
		assert.Equal(t, http.StatusBadRequest, w.Code, "%s: flag on, invalid body", path)
	}
	// A JSON string is not a valid body for any method.
	for _, path := range []string{"/v1/helper/invites", "/v1/helper/invites/claim", "/v1/helper/invites/get",
		"/v1/helper/invites/decide", "/v1/helper/pairings", "/v1/helper/pairings/end", "/v1/helper/tickets"} {
		w := helperPost(t, mux, path, "device-A", "not a message", false)
		assert.Equal(t, flagOffWant(path), w.Code, "%s: flag off, undecodable body", path)
		w = helperPost(t, mux, path, "", "not a message", true)
		assert.Equal(t, http.StatusBadRequest, w.Code, "%s: no device id", path)
		assert.Contains(t, w.Body.String(), "X-Lantern-Device-Id", "%s: the gate, not the decoder, refused it", path)
	}
```

Run: `$LC_TEST 'TestHelperFeatureFlags|TestConfigNewHelperFeatures|TestHelperVolunteerWorksWithoutTheFlag|TestHelperGateRunsBeforeBody'`
Expected: the build fails with `undefined: helperFeatureFlags`.

- [ ] **Step 3: Migration and query**

`cmd/api/migrations/infra/000149_helper_claims_by_volunteer.up.sql`:

```sql
-- The gate lets a device that holds a live claim reach the volunteer
-- endpoints without the helper_invites flag; this finds its claims.
CREATE INDEX helper_invites_claimed_volunteer_idx ON helper_invites (volunteer_device_id)
WHERE state = 'claimed';
```

`000149_helper_claims_by_volunteer.down.sql`:

```sql
DROP INDEX IF EXISTS helper_invites_claimed_volunteer_idx;
```

Append to `cmd/api/queries/infra/helper.sql`:

```sql
-- name: CountLiveHelperClaimsForVolunteer :one
SELECT count(*) FROM helper_invites
WHERE volunteer_device_id = @volunteer_device_id AND state = 'claimed' AND expires_at > now();
```

Run: `cd cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate && cd ../..`

- [ ] **Step 4: Write `helper_features.go`**

```go
package main

import (
	"context"
	"errors"
	"time"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/log"
	"github.com/getlantern/lantern-cloud/regions"
)

// helperFeatureFlags returns the client's Features["helper_invites"], which
// gates only the inviter's UI, and whether the Unbounded widget is forced on
// because the device volunteers in a pairing. The second needs no flag: a
// volunteer abroad helps through an invite, and the flag must not have to
// cover its country. It is never forced in a censored country, or where the
// country is unknown, because the widget's UI must not appear there (see the
// app's FeatureFlag.unbounded).
func helperFeatureFlags(invitesOn, pairedVolunteer bool, clientCountry string) (helperInvites, forceUnbounded bool) {
	return invitesOn, pairedVolunteer && clientCountry != "" && !regions.IsCensoredCountry(clientCountry)
}

// startHelperVolunteerLookup starts, in the background, a check of whether
// deviceID volunteers in at least one pairing. It returns a function that
// waits for the answer at most timeout from now, and reports false past it
// (see startHelperLookup). Like the other helper lookups, it runs detached
// from the request's trace and logs nothing that identifies the device.
func (ls *lanternService) startHelperVolunteerLookup(ctx context.Context, timeout time.Duration, deviceID string) func() bool {
	return startHelperLookup(ctx, timeout, "helper volunteer", func(hctx context.Context) bool {
		if deviceID == "" {
			return false
		}
		n, err := dal.New(ls.apiDB.GetPool()).CountHelperPairingsForVolunteer(hctx, deviceID)
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				log.ErrorUnlessCanceled(hctx, "helper volunteer: count pairings", err, nil)
			}
			return false
		}
		return n > 0
	})
}
```

- [ ] **Step 5: The gate without the flag**

In `cmd/api/helper_handler.go`:

Add a field to `helperRequestContext`:

```go
	// flagOff is set when helper_invites is off for the client and the
	// handler is one a volunteer may reach without it. The handler then
	// requires a live invite (claim), or a claim or pairing (the rest).
	flagOff bool
```

After `featureFlagHelperInvites`, add:

```go
// helperFlagFreeHandlers are the volunteer's endpoints. They run without the
// helper_invites flag, so a volunteer abroad needs no flag: a live invite
// admits a claim, and holding a claim or a pairing admits the rest (see
// volunteerOrNotFound). Inviting always needs the flag.
var helperFlagFreeHandlers = map[string]bool{
	"helperClaimInvite": true, "helperListPairings": true, "helperEndPairing": true, "helperIssueTickets": true,
}
```

In `helperRequest`, replace:

```go
	if !helperInvitesEnabled(header, p.clientIP, p.clientCountry) {
		return p, router.NotFound("404 page not found")
	}
```

with:

```go
	if !helperInvitesEnabled(header, p.clientIP, p.clientCountry) {
		if !helperFlagFreeHandlers[handler] {
			return p, router.NotFound("404 page not found")
		}
		p.flagOff = true
	}
```

Add:

```go
var errHelperTooManyRequests = &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many requests"}

// volunteerOrNotFound lets a flag-off request through only for a device that
// is the volunteer in a pairing or holds a live claim. Anything else gets the
// gate's own 404. Any device ID can ask, so each request is charged to its
// source's flagoff: bucket before the two lookups run.
func (s helperService) volunteerOrNotFound(p helperRequestContext, handler string) error {
	if !p.flagOff {
		return nil
	}
	if !s.allowSource(p) {
		return errHelperTooManyRequests
	}
	q := dal.New(s.apiDB.GetPool())
	n, err := q.CountHelperPairingsForVolunteer(p.ctx, p.deviceID)
	if err != nil {
		return helperInternalError(p.ctx, handler, err)
	}
	if n > 0 {
		return nil
	}
	c, err := q.CountLiveHelperClaimsForVolunteer(p.ctx, sql.NullString{String: p.deviceID, Valid: true})
	if err != nil {
		return helperInternalError(p.ctx, handler, err)
	}
	if c > 0 {
		return nil
	}
	return router.NotFound("404 page not found")
}
```

In `ListPairings`, `EndPairing` and `IssueTickets`, right after `p, err := helperRequest(ctx, "<handler>")` and its error check, add the following, with that method's handler name:

```go
	if err := s.volunteerOrNotFound(p, "helperListPairings"); err != nil {
		return nil, err
	}
```

`ClaimInvite` needs no extra check. `ClaimHelperInvite` already admits only a pending, unexpired invite and answers 409 otherwise.

Replace `allowSource` with a version whose flag-off requests use their own buckets:

```go
// allowSource charges the rate-limit bucket for p's network source.
// Flag-off requests, which can only be claims, use buckets of their own, so
// they cannot drain the egress bucket the pilot's inviters share.
func (s helperService) allowSource(p helperRequestContext) bool {
	now := time.Now()
	prefix := ""
	if p.flagOff {
		prefix = "flagoff:"
	}
	if p.source == "" {
		return s.ipLimit.allowN(prefix+helperUnknownSourceKey, helperIPRate, helperIPBurst, now)
	}
	if s.isEgress != nil {
		// An inventory failure falls back to the source's own bucket rather
		// than refusing: helper invites are not worth an outage, and the
		// per-source budget still bounds a single address.
		if egress, err := s.isEgress(p.ctx, p.source); err == nil && egress {
			return s.ipLimit.allowN(prefix+helperEgressKey, helperEgressRate, helperEgressBurst, now)
		}
	}
	return s.ipLimit.allowN(prefix+helperIPKey(p.source), helperIPRate, helperIPBurst, now)
}
```

- [ ] **Step 6: `/v1/config-new`**

In `cmd/api/config.go`, replace the helper block that starts `var helpers []helperOutbound` and ends with the closing brace of `if (wantsDirect || wantsRooms) && helperInvitesEnabled(...) {` with:

```go
	var helpers []helperOutbound
	var helperUnbounded *option.Outbound
	var pairedVolunteer bool
	wantsDirect, wantsRooms := slices.Contains(req.Protocols, "samizdat"), slices.Contains(req.Protocols, common.UNBOUNDED)
	helperOn := helperInvitesEnabled(hdr, clientIPString, clientCountry)
	var waitVolunteer func() bool
	if wantsRooms {
		// Not behind helper_invites: a paired volunteer abroad needs no flag
		// to get its widget (see helperFeatureFlags).
		waitVolunteer = ls.startHelperVolunteerLookup(ctx, helperOutboundsTimeout, req.DeviceID)
	}
	if (wantsDirect || wantsRooms) && helperOn {
		validate := featureEnabled(featureFlagEnforceConfigValidation)
		var waitRooms func() *option.Outbound
		if wantsRooms {
			waitRooms = ls.startHelperUnboundedLookup(ctx, helperOutboundsTimeout, req.DeviceID, validate)
		}
		if wantsDirect {
			helpers = ls.helperOutboundsWithin(ctx, helperOutboundsTimeout, req.DeviceID, clientCountry, helperClientFactors(banditCF, banditCFBuilt), helperNow(), validate)
		}
		if waitRooms != nil {
			helperUnbounded = waitRooms()
		}
	}
	if waitVolunteer != nil {
		pairedVolunteer = waitVolunteer()
	}
```

In the same file, replace:

```go
	fullResponse.Features[common.UNBOUNDED] = featureEnabled(common.UNBOUNDED)
```

with:

```go
	// helper_invites gates the app's inviter screens. A paired volunteer gets
	// the widget whatever the unbounded and helper_invites flags say, so it
	// can help without a port (see helperFeatureFlags). Nothing here is
	// logged or traced.
	helperInvites, forceUnbounded := helperFeatureFlags(helperOn, pairedVolunteer, clientCountry)
	fullResponse.Features[featureFlagHelperInvites] = helperInvites
	fullResponse.Features[common.UNBOUNDED] = featureEnabled(common.UNBOUNDED) || forceUnbounded
```

- [ ] **Step 7: Run the tests and the existing helper tests**

Run: `$LC_TEST 'TestHelperFeatureFlags|TestConfigNewHelperFeatures|TestHelperVolunteerWorksWithoutTheFlag|TestHelper|TestConfigNewHelper|TestConfigNewRecordsNoHelperOnTheRequestSpan'`
Expected: PASS. `TestConfigNewRecordsNoHelperOnTheRequestSpan` still passes because the new lookup runs on a detached trace and the Features map is not recorded anywhere.

- [ ] **Step 8: Telemetry audit**

Run: `git diff origin/main -- cmd/api/*.go ':!*_test.go' | grep -nE '^\+.*(log\.|span\.|attribute\.|metrics\.)'`
Expected: only `log.ErrorUnlessCanceled(hctx, "helper volunteer: count pairings", err, nil)`. `volunteerOrNotFound` logs through `helperInternalError`, which records the handler name only.

- [ ] **Step 9: Commit**

```bash
git add cmd/api/helper_features.go cmd/api/helper_features_db_test.go cmd/api/config.go cmd/api/helper_handler.go \
  cmd/api/helper_handler_db_test.go cmd/api/migrations/infra/000149_helper_claims_by_volunteer.*.sql \
  cmd/api/queries/infra/helper.sql cmd/api/dal
git commit -m "api: helper features in config; volunteers reach their endpoints without the flag"
```

---

### Task 2: lantern-cloud — helper online status and pairing dates in `ListPairings`

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-app`

**Files:**
- Create: `cmd/api/migrations/infra/000150_helper_volunteer_presence.up.sql`, `000150_helper_volunteer_presence.down.sql`. This is the number after Task 1's migration; if Task 1 had to renumber, follow it.
- Modify: `cmd/api/queries/infra/helper.sql`, and the generated `cmd/api/dal/helper.sql.go` and `cmd/api/dal/models.go`
- Modify: `cmd/api/apipb/proto/services/helper.proto`, and the five generated files listed in Global Constraints
- Modify: `cmd/api/helper_handler.go` (`ListPairings`, `IssueTickets`, `runHelperInviteSweep`)
- Create: `cmd/api/helper_presence_db_test.go`

**Interfaces:**
- Consumes:
  - `approvedPairingWith`, `helperPost`, `newHelperTestService` and `newHelperTestRouter` (tests)
  - `newHelperConfigNewFixture` and `pairVolunteer` (tests)
- Produces:
  - Table `helper_volunteer_presence(volunteer_device_id TEXT PRIMARY KEY, seen_at TIMESTAMPTZ NOT NULL)`
  - Queries `TouchHelperVolunteerPresence(ctx, volunteerDeviceID string) error`, `ListHelperVolunteersOnlineForInviter(ctx, inviterDeviceID string) ([]dal.ListHelperVolunteersOnlineForInviterRow, error)` with fields `PairingID uuid.UUID` and `Online bool`, and `DeleteStaleHelperVolunteerPresence(ctx) (int64, error)`
  - `HelperPairing.online` (field 4) and `HelperPairing.created_unix` (field 5). On the wire these are `"online": true` (absent when false) and `"created_unix": "<seconds>"`. The app's engine (Task 9) reads them.

**Where presence lives: Postgres, not Redis TTL keys.**
- *Redis TTL keys* (`SET helper:seen:<sha256(device)> 1 EX 900`) would expire by themselves, need no sweep and no migration, and keep presence writes off Postgres.
- *Postgres* puts presence where `ListPairings` already reads: the online query joins it with `helper_pairings` and `peer_routes` in one statement and is tested with `testdb` like the rest of the helper API. lantern-cloud reaches Redis only through its cache manager, built for caches rather than per-device state.
- At pilot scale a volunteer costs at most one write every 5 minutes.
- **Decision:** Postgres. Move to Redis TTL keys if volunteer counts make the write rate matter. The online status is computed fail-soft (Step 6), so presence storage can never break `ListPairings`.

**Threat-model row** (added to the spec in this plan's commit): "Helper presence visible to the inviter". An inviter, or whoever holds her phone, can chart a volunteer's on and off pattern at about 5-minute resolution. This is accepted, because the spec asks for online status. The server keeps one timestamp per volunteer device, never per pairing, and sweeps it after 48 hours.

- [ ] **Step 1: Write the failing tests**

`cmd/api/helper_presence_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
)

type listedPairing struct {
	PairingID   string `json:"pairing_id"`
	Role        string `json:"role"`
	Online      bool   `json:"online"`
	CreatedUnix string `json:"created_unix"`
}

func listPairings(t *testing.T, mux http.Handler, device string) []listedPairing {
	t.Helper()
	w := helperPost(t, mux, "/v1/helper/pairings", device, map[string]any{}, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		Pairings []listedPairing `json:"pairings"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Pairings
}

// TestHelperListPairingsReportsVolunteerPresence covers the inviter's online
// status: offline until the volunteer's widget fetches tickets, online for 15
// minutes after, and never shown to the volunteer about the inviter.
func TestHelperListPairingsReportsVolunteerPresence(t *testing.T) {
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	pid := approvedPairingWith(t, mux, "volunteer-A")

	got := listPairings(t, mux, "inviter")
	require.Len(t, got, 1)
	assert.Equal(t, pid, got[0].PairingID)
	assert.False(t, got[0].Online, "no route and no ticket fetch: offline")
	created, err := strconv.ParseInt(got[0].CreatedUnix, 10, 64)
	require.NoError(t, err)
	assert.Positive(t, created)

	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/tickets", "inviter", map[string]any{}, true).Code)
	assert.False(t, listPairings(t, mux, "inviter")[0].Online, "the inviter's own fetch says nothing about the volunteer")

	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/tickets", "volunteer-A", map[string]any{}, true).Code)
	assert.True(t, listPairings(t, mux, "inviter")[0].Online, "a volunteer that fetched tickets is online")

	vol := listPairings(t, mux, "volunteer-A")
	require.Len(t, vol, 1)
	assert.False(t, vol[0].Online, "a volunteer is never told whether the inviter is online")
	assert.NotEmpty(t, vol[0].CreatedUnix)

	_, err = tdb.GetPool().Exec(context.Background(),
		`UPDATE helper_volunteer_presence SET seen_at = now() - interval '16 minutes'`)
	require.NoError(t, err)
	assert.False(t, listPairings(t, mux, "inviter")[0].Online, "presence lasts 15 minutes")

	_, err = tdb.GetPool().Exec(context.Background(),
		`UPDATE helper_volunteer_presence SET seen_at = now() - interval '49 hours'`)
	require.NoError(t, err)
	n, err := dal.New(tdb.GetPool()).DeleteStaleHelperVolunteerPresence(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the sweep forgets presence after 48 hours")
}

// TestHelperPresenceWritesAreCoarse checks that a second ticket fetch within
// five minutes does not move seen_at.
func TestHelperPresenceWritesAreCoarse(t *testing.T) {
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	approvedPairingWith(t, mux, "volunteer-A")
	ctx := context.Background()
	require.NoError(t, dal.New(tdb.GetPool()).TouchHelperVolunteerPresence(ctx, "volunteer-A"))
	_, err := tdb.GetPool().Exec(ctx,
		`UPDATE helper_volunteer_presence SET seen_at = now() - interval '2 minutes'`)
	require.NoError(t, err)
	require.NoError(t, dal.New(tdb.GetPool()).TouchHelperVolunteerPresence(ctx, "volunteer-A"))
	var age float64
	require.NoError(t, tdb.GetPool().QueryRow(ctx,
		`SELECT extract(epoch FROM now() - seen_at) FROM helper_volunteer_presence`).Scan(&age))
	assert.Greater(t, age, 60.0, "a touch within 5 minutes is skipped")
}

// TestHelperOnlineCountsALiveRoute checks the direct-mode half: a volunteer
// with a live peer route is online without any ticket fetch.
func TestHelperOnlineCountsALiveRoute(t *testing.T) {
	f := newHelperConfigNewFixture(t)
	pid, _ := f.pairVolunteer(t, "volunteer-R", 45000)
	rows, err := f.q.ListHelperVolunteersOnlineForInviter(context.Background(), "inviter")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, pid, rows[0].PairingID)
	assert.True(t, rows[0].Online)
}
// TestHelperListPairingsSurvivesAMissingPresenceTable checks the fail-soft
// online status: a broken presence lookup shows helpers offline and still
// serves the list.
func TestHelperListPairingsSurvivesAMissingPresenceTable(t *testing.T) {
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	approvedPairingWith(t, mux, "volunteer-A")
	_, err := tdb.GetPool().Exec(context.Background(), `DROP TABLE helper_volunteer_presence`)
	require.NoError(t, err)
	got := listPairings(t, mux, "inviter")
	require.Len(t, got, 1)
	assert.False(t, got[0].Online)
}
```


- [ ] **Step 2: Run the tests to verify they fail**

Run: `$LC_TEST 'TestHelperListPairingsReportsVolunteerPresence|TestHelperPresenceWritesAreCoarse|TestHelperOnlineCountsALiveRoute|TestHelperListPairingsSurvivesAMissingPresenceTable'`
Expected: the build fails with `undefined: ... DeleteStaleHelperVolunteerPresence`.

- [ ] **Step 3: Migration**

`cmd/api/migrations/infra/000150_helper_volunteer_presence.up.sql`:

```sql
-- When each paired volunteer device last fetched pairing tickets, which its
-- widget does every 10 minutes while it runs. An inviter's ListPairings reads
-- it to show a helper as online. One row per volunteer device, not per
-- pairing; it says nothing about whom the volunteer helps. Rows are swept 48
-- hours after their last touch.
CREATE TABLE helper_volunteer_presence (
    volunteer_device_id TEXT PRIMARY KEY,
    seen_at             TIMESTAMPTZ NOT NULL
);
```

`000150_helper_volunteer_presence.down.sql`:

```sql
DROP TABLE IF EXISTS helper_volunteer_presence;
```

- [ ] **Step 4: Queries**

Append to `cmd/api/queries/infra/helper.sql`:

```sql
-- Records that a volunteer device is running, at most once every 5 minutes so
-- a busy volunteer costs one write per 5 minutes.
-- name: TouchHelperVolunteerPresence :exec
INSERT INTO helper_volunteer_presence (volunteer_device_id, seen_at)
VALUES (@volunteer_device_id, now())
ON CONFLICT (volunteer_device_id) DO UPDATE SET seen_at = now()
WHERE helper_volunteer_presence.seen_at < now() - interval '5 minutes';

-- Each of an inviter's pairings with whether its volunteer is online: it has
-- a live peer route, or it fetched tickets in the last 15 minutes. The live
-- route test matches ListHelperRoutesForInviter's, without the track checks,
-- because a route on a disabled track is still a volunteer that is running.
-- name: ListHelperVolunteersOnlineForInviter :many
SELECT hp.pairing_id,
       (EXISTS (SELECT 1 FROM peer_routes pr
                WHERE pr.owner_device_id = hp.volunteer_device_id
                  AND pr.deprecated IS NULL
                  AND pr.callback_verified IS NOT NULL
                  AND pr.expiration > now())
        OR COALESCE(p.seen_at > now() - interval '15 minutes', false))::boolean AS online
FROM helper_pairings hp
LEFT JOIN helper_volunteer_presence p ON p.volunteer_device_id = hp.volunteer_device_id
WHERE hp.inviter_device_id = @inviter_device_id;

-- name: DeleteStaleHelperVolunteerPresence :execrows
DELETE FROM helper_volunteer_presence WHERE seen_at < now() - interval '48 hours';
```

Run: `cd cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate && cd ../..`
Expected: `cmd/api/dal/helper.sql.go` gains the three functions, and `cmd/api/dal/models.go` gains `HelperVolunteerPresence`.

- [ ] **Step 5: Proto**

In `cmd/api/apipb/proto/services/helper.proto`, replace `message HelperPairing { … }` with:

```proto
message HelperPairing {
  string pairing_id = 1;
  // "inviter" or "volunteer": this device's side of the pairing.
  string role = 2;
  // For a volunteer, the inviter's encrypted name the first time it is listed.
  bytes inviter_blob = 3;
  // For an inviter only: whether the volunteer is running now (a live peer
  // route, or a ticket fetch in the last 15 minutes). A volunteer is never
  // told whether its inviter is online.
  bool online = 4;
  // When the pairing was approved, in Unix seconds.
  int64 created_unix = 5;
}
```

Run `make proto`, then `git status --short`, then `git checkout --` on every changed generated file except the five listed in Global Constraints.

- [ ] **Step 6: Handler**

In `cmd/api/helper_handler.go`, in `ListPairings`, replace the body of the transaction function, from `q := dal.New(...)` through `return out, nil`, with:

```go
		q := dal.New(s.apiDB.GetPool()).WithTx(tx)
		rows, err := q.ListHelperPairingsForDevice(ctx, p.deviceID)
		if err != nil {
			return nil, fmt.Errorf("list helper pairings: %w", err)
		}
		out := &services.HelperPairingList{}
		for _, r := range rows {
			hp := &services.HelperPairing{PairingId: r.PairingID.String(), Role: string(pairing.RoleInviter),
				CreatedUnix: r.CreatedAt.Unix()}
			if r.VolunteerDeviceID == p.deviceID {
				hp.Role = string(pairing.RoleVolunteer)
				if len(r.InviterBlob) > 0 {
					blob, err := q.TakeHelperPairingBlob(ctx, dal.TakeHelperPairingBlobParams{
						PairingID:         uuid.NullUUID{UUID: r.PairingID, Valid: true},
						VolunteerDeviceID: sql.NullString{String: p.deviceID, Valid: true},
					})
					if err != nil && !errors.Is(err, pgx.ErrNoRows) {
						return nil, fmt.Errorf("take helper pairing blob: %w", err)
					}
					hp.InviterBlob = blob
				}
			}
			out.Pairings = append(out.Pairings, hp)
		}
		return out, nil
```

After the transaction, and before `return out, nil`, mark the inviter's helpers online. This runs outside the transaction and fails soft: if the presence lookup fails, for example because the table is missing during a rolling deploy, every helper reads offline and the list is still served.

```go
	s.markHelpersOnline(ctx, p.deviceID, out)
```

Add the method:

```go
// markHelpersOnline sets Online on the inviter-side pairings in out. It is
// best-effort: on any error every helper stays offline, so presence can never
// break ListPairings. A volunteer's rows never carry it.
func (s helperService) markHelpersOnline(ctx context.Context, deviceID string, out *services.HelperPairingList) {
	inviter := false
	for _, hp := range out.Pairings {
		if hp.Role == string(pairing.RoleInviter) {
			inviter = true
			break
		}
	}
	if !inviter {
		return
	}
	rows, err := dal.New(s.apiDB.GetPool()).ListHelperVolunteersOnlineForInviter(ctx, deviceID)
	if err != nil {
		log.Error(ctx, "helperListPairings: presence lookup failed; helpers shown offline", err, nil)
		return
	}
	online := make(map[string]bool, len(rows))
	for _, o := range rows {
		online[o.PairingID.String()] = o.Online
	}
	for _, hp := range out.Pairings {
		if hp.Role == string(pairing.RoleInviter) {
			hp.Online = online[hp.PairingId]
		}
	}
}
```

In `IssueTickets`, inside the `for _, r := range rows` loop, after `role = pairing.RoleVolunteer`, add `volunteer = true`. Declare `volunteer := false` before the loop. After the loop, before `return out, nil`, add:

```go
	// A volunteer that fetches tickets is running: its inviters see it
	// online for 15 minutes. A failure costs only that status.
	if volunteer {
		if err := dal.New(s.apiDB.GetPool()).TouchHelperVolunteerPresence(ctx, p.deviceID); err != nil {
			log.Error(ctx, "helperIssueTickets: presence failed", err, nil)
		}
	}
```

In `runHelperInviteSweep`, after the `DeleteExpiredHelperInvites` call, add:

```go
			if _, err := q.DeleteStaleHelperVolunteerPresence(ctx); err != nil {
				log.Error(ctx, "helper presence sweep failed", err, nil)
			}
```

- [ ] **Step 7: Run the tests**

Run: `$LC_TEST 'TestHelperListPairingsReportsVolunteerPresence|TestHelperPresenceWritesAreCoarse|TestHelperOnlineCountsALiveRoute|TestHelperListPairingsSurvivesAMissingPresenceTable|TestHelper'`
Expected: PASS, including every existing `TestHelper*` test.

- [ ] **Step 8: Telemetry audit and commit**

Run: `git diff origin/main -- cmd/api/helper_handler.go | grep -nE '^\+.*(log\.|span|attribute)'`
Expected: only the three fixed messages `"helperIssueTickets: presence failed"`, `"helperListPairings: presence lookup failed; helpers shown offline"` and `"helper presence sweep failed"`.

```bash
git add cmd/api/migrations/infra/000150_helper_volunteer_presence.*.sql cmd/api/queries/infra/helper.sql cmd/api/dal \
  cmd/api/apipb/proto/services/helper.proto cmd/api/apipb/services/helper.pb.go cmd/api/apipb/services/helper_grpc.pb.go \
  cmd/backoffice/src/gen/services/helper_pb.ts cmd/backoffice/src/gen/services/helper-HelperService_connectquery.ts \
  openapiv2/services/helper.swagger.json cmd/api/helper_handler.go cmd/api/helper_presence_db_test.go
git commit -m "api: helper online status and pairing dates in ListPairings"
```

---

### Task 3: lantern-cloud — CORS on `/v1/helper/` for `help.lantern.io` only

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-app`

**Files:**
- Modify: `router/middleware.go` (after `func CORS`)
- Modify: `router/middleware_test.go`
- Modify: `cmd/api/main.go` (the `router.New(...)` call at about line 249)
- Modify: `cmd/api/helper_handler.go` (one variable)

**Interfaces:**
- Consumes: `router.CORS(anonymous ...string) Middleware`
- Produces:
  - `type RestrictedCORS struct { Prefix string; Origins []string; Headers string }`
  - `func CORSWith(restricted []RestrictedCORS, anonymous ...string) Middleware`
  - `var helperCORS = []router.RestrictedCORS{...}`, which allows only `https://help.lantern.io` to send `Content-Type`, `X-Lantern-Device-Id` and `X-Lantern-Feature-Override` to `/v1/helper/`. The override header is there for testing a flag-on browser before the flag is on. Plan 5's page relies on this rule, and must be served from that origin.

**Why restricted:** the generic `CORSMiddleware` reflects any origin with credentials. The helper API takes a bearer device ID, so only Lantern's own volunteer page may call it from a browser. That page lives on a dedicated origin, `help.lantern.io`, with a strict CSP and no third-party script (open question 7), so nothing else on lantern.io can read the browser's device ID or seed. Other origins get no `Access-Control-Allow-Origin`, and the browser refuses their preflight.

- [ ] **Step 1: Write the failing test**

Append to `router/middleware_test.go`:

```go
// TestCORSWithRestrictedPrefix checks that a restricted prefix answers only
// its own origins, sends no credentials header, and leaves every other path
// to CORS.
func TestCORSWithRestrictedPrefix(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := router.CORSWith([]router.RestrictedCORS{{
		Prefix:  "/v1/helper/",
		Origins: []string{"https://help.lantern.io"},
		Headers: "Content-Type, X-Lantern-Device-Id, X-Lantern-Feature-Override",
	}})(ok)
	do := func(method, path, origin string) *http.Response {
		r := httptest.NewRequest(method, path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Result()
	}

	t.Run("allowed origin preflight", func(t *testing.T) {
		resp := do(http.MethodOptions, "/v1/helper/invites/claim", "https://help.lantern.io")
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.Equal(t, "https://help.lantern.io", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "X-Lantern-Device-Id")
		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Credentials"))
		assert.Contains(t, resp.Header.Values("Vary"), "Origin")
	})
	t.Run("other origins' preflights are refused, lantern.io's too", func(t *testing.T) {
		for _, origin := range []string{"https://evil.example", "https://lantern.io", "https://www.lantern.io"} {
			resp := do(http.MethodOptions, "/v1/helper/invites/claim", origin)
			assert.Equal(t, http.StatusNoContent, resp.StatusCode, origin)
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"), origin)
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Headers"), origin)
		}
	})
	t.Run("allowed origin POST reaches the handler", func(t *testing.T) {
		resp := do(http.MethodPost, "/v1/helper/pairings", "https://help.lantern.io")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://help.lantern.io", resp.Header.Get("Access-Control-Allow-Origin"))
	})
	t.Run("no origin, as from the app", func(t *testing.T) {
		resp := do(http.MethodPost, "/v1/helper/pairings", "")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})
	t.Run("other paths keep CORS", func(t *testing.T) {
		resp := do(http.MethodOptions, "/v1/config-new", "https://evil.example")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://evil.example", resp.Header.Get("Access-Control-Allow-Origin"))
	})
}
```

Run: `go test ./router/ -run TestCORSWithRestrictedPrefix -v`
Expected: the build fails with `undefined: router.CORSWith`.

- [ ] **Step 2: Implement**

Add to `router/middleware.go`, after `CORS`:

```go
// RestrictedCORS lets browsers on Origins call the paths under Prefix,
// without credentials, sending only Headers. Any other origin gets no CORS
// headers, so the browser refuses its preflight and never sends the request.
type RestrictedCORS struct {
	Prefix  string
	Origins []string
	Headers string
}

// CORSWith is CORS with restricted prefixes, which are answered by their own
// rule before anything else.
func CORSWith(restricted []RestrictedCORS, anonymous ...string) Middleware {
	base := CORS(anonymous...)
	return func(next http.Handler) http.Handler {
		other := base(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, rc := range restricted {
				if !strings.HasPrefix(r.URL.Path, rc.Prefix) {
					continue
				}
				origin := r.Header.Get("Origin")
				allowed := origin != "" && slices.Contains(rc.Origins, origin)
				if allowed {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", origin)
					h.Add("Vary", "Origin")
					h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				}
				if r.Method == http.MethodOptions {
					if allowed {
						w.Header().Set("Access-Control-Allow-Headers", rc.Headers)
						w.Header().Set("Access-Control-Max-Age", "600")
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			other.ServeHTTP(w, r)
		})
	}
}
```

`router/middleware.go` already imports `slices` and `strings`.

In `cmd/api/helper_handler.go`, after `featureFlagHelperInvites`, add:

```go
// helperCORS lets the browser-volunteer page, and only it, call the helper
// API from a browser (plan 5). That page lives on its own origin with a
// strict CSP and no third-party script, so no other lantern.io page can read
// the device ID it keeps. The device ID header is the only credential, and no
// cookie is ever sent. The override header is for testing.
var helperCORS = []router.RestrictedCORS{{
	Prefix:  "/v1/helper/",
	Origins: []string{"https://help.lantern.io"},
	Headers: "Content-Type, X-Lantern-Device-Id, X-Lantern-Feature-Override",
}}
```

In `cmd/api/main.go`, replace `router.CORS(widgetFeaturesPath)` with `router.CORSWith(helperCORS, widgetFeaturesPath)`.

- [ ] **Step 3: Run the tests**

Run: `go test ./router/ -run 'TestCORS' -v && $LC_TEST 'TestHelperGate|TestWidget'`
Expected: PASS. The existing `TestCORS` is unchanged.

- [ ] **Step 4: Commit and open the lantern-cloud PR**

```bash
git add router/middleware.go router/middleware_test.go cmd/api/main.go cmd/api/helper_handler.go
git commit -m "api: CORS on /v1/helper/ for help.lantern.io only"
```

Run `make test` and `golangci-lint run --new-from-rev=origin/main ./cmd/api/... ./router/...`. Expected: both pass. Then open the PR for Tasks 1–3 with `skills:create-pr`, base `main`.

- [ ] **Step 5: [Human] Merge, migrate, deploy**
  - Merge the PR.
  - Apply migrations 000149 and 000150 in production, the way 000146–000148 were applied.
  - Deploy the API with a rolling restart of the `api` MIG.
  - Check with `curl -si -X OPTIONS -H 'Origin: https://help.lantern.io' -H 'Access-Control-Request-Method: POST' https://api.iantem.io/v1/helper/pairings`. Expect 204 with `Access-Control-Allow-Origin: https://help.lantern.io`.
  - If the API is reached through a CDN, confirm that the CDN passes `Origin`, `X-Lantern-Device-Id` and the CORS response headers.

---

### Task 4: unbounded — `common/helperinvite`: device-side crypto, links and vectors

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-helper-app`

**Files:**
- Create: `common/helperinvite/helperinvite.go`, `names.go`, `link.go`, `claim.go`
- Create: `common/helperinvite/helperinvite_test.go`
- Create: `common/helperinvite/testdata/lantern-cloud-vectors.json`, a verbatim copy of lantern-cloud's `cmd/api/pairing/testdata/vectors.json`
- Create: `common/helperinvite/testdata/names.json`
- Modify: `go.mod`, `go.sum` (`golang.org/x/crypto` becomes direct)

**Interfaces:**
- Produces:
  - `const SecretLen = 32`, `const MaxNameBytes = 200`, `const LinkPrefix = "https://lantern.io/help/#"`
  - `var ErrSecret, ErrName, ErrBlob, ErrLink error`
  - `func NewSecret(r io.Reader) ([]byte, error)`
  - `func InviteHash(s []byte) []byte`
  - `func DeriveKey(s []byte) []byte`
  - `func ApprovalKey(s []byte) ed25519.PrivateKey`
  - `func ApprovalPublic(s []byte) []byte`
  - `func ApprovalMessage(inviteHash, volunteerPub []byte) []byte`
  - `func Approve(s, volunteerPub []byte) []byte`
  - `func MatchingCode(k, volunteerPub []byte) string`
  - `func VolunteerNameKey(s []byte) []byte`
  - `func InviterNameKey(s []byte) []byte`
  - `func DeviceKey(seed []byte) (ed25519.PrivateKey, error)`
  - `func CleanName(name string) (string, error)`
  - `func SealName(key []byte, name string, r io.Reader) ([]byte, error)`
  - `func OpenName(key, blob []byte) (string, error)`
  - `func Link(s []byte) string`
  - `func ParseLink(link string) ([]byte, error)`
  - `type Claim struct { InviteHash, VolunteerPubkey, VolunteerBlob []byte; Code string }`
  - `func NewClaim(s, seed []byte, name string, r io.Reader) (Claim, error)`
  - `type Approval struct { Signature, InviterBlob []byte }`
  - `func NewApproval(s, volunteerPub []byte, inviterName string, r io.Reader) (Approval, error)`
  - Used by lantern-core's engine (Task 9) and the wasm API (Task 5).

- [ ] **Step 1: Create the worktree and copy lantern-cloud's vectors**

```bash
cd ~/go/src/github.com/getlantern/unbounded
git fetch origin
git worktree add -b fisk/helper-app ~/go/src/github.com/getlantern/unbounded-helper-app origin/main
cd ~/go/src/github.com/getlantern/unbounded-helper-app
mkdir -p common/helperinvite/testdata
git -C ~/go/src/github.com/getlantern/lantern-cloud-helper-app show origin/main:cmd/api/pairing/testdata/vectors.json \
  > common/helperinvite/testdata/lantern-cloud-vectors.json
```

- [ ] **Step 2: Write the name-blob vectors**

`common/helperinvite/testdata/names.json`, computed from the plan 1 formulas. The secret is the first lantern-cloud vector's, 32 zero bytes. The nonce is fixed only here. Names are given as UTF-8 hex so the file has no encoding ambiguity.

```json
{
  "secret_b64url": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "nonce_hex": "000102030405060708090a0b",
  "volunteer_name_key_hex": "d2e3f66916d1617a73e3012fef21b4aa879657637131e16863b2f5172686c64b",
  "inviter_name_key_hex": "7f5e6b1bea23883eb71b7b8bf3ba454eb60bac661aa175da990ecc9e45bc80b5",
  "blobs": [
    {"key": "volunteer", "name_hex": "416c69", "blob_hex": "000102030405060708090a0b3d162fa26ae4b01b6f1f42a08d0b9833d29c55"},
    {"key": "inviter", "name_hex": "53617261", "blob_hex": "000102030405060708090a0b763a2be76a376a5b56e96ba66c507b4643b3120d"},
    {"key": "inviter", "name_hex": "d8b3d8a7d8b1d8a7", "blob_hex": "000102030405060708090a0bfde88121ac37ff16f2cf1e0473b436baa96dbb816160a28c"}
  ],
  "device_seed_hex": "0707070707070707070707070707070707070707070707070707070707070707",
  "device_pub_hex": "ea4a6c63e29c520abef5507b132ec5f9954776aebebe7b92421eea691446d22c",
  "device_matching_code": "2562",
  "device_approval_sig_hex": "98b762de9ad5640c119c2d8ed227b839b6def7ce2f59af474e905d407ea2320fd37b3c9e139017de9bed5ee86f68154bbfbbeaab84cf3ad95651bcef75afd904"
}
```

- [ ] **Step 3: Write the failing tests**

`common/helperinvite/helperinvite_test.go`:

```go
package helperinvite

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// TestLanternCloudVectors checks every derivation against lantern-cloud's own
// vectors, so the app, the browser and the server agree byte for byte.
func TestLanternCloudVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/lantern-cloud-vectors.json")
	require.NoError(t, err)
	var v struct {
		Invites []struct {
			SecretB64url    string `json:"secret_b64url"`
			InviteHashHex   string `json:"invite_hash_hex"`
			KeyHex          string `json:"key_hex"`
			ApprovalPubHex  string `json:"approval_pub_hex"`
			VolunteerPubHex string `json:"volunteer_pub_hex"`
			MatchingCode    string `json:"matching_code"`
			ApprovalSigHex  string `json:"approval_sig_hex"`
		} `json:"invites"`
	}
	require.NoError(t, json.Unmarshal(data, &v))
	require.NotEmpty(t, v.Invites)
	for _, iv := range v.Invites {
		s, err := base64.RawURLEncoding.DecodeString(iv.SecretB64url)
		require.NoError(t, err)
		vol := unhex(t, iv.VolunteerPubHex)
		assert.Equal(t, iv.InviteHashHex, hex.EncodeToString(InviteHash(s)))
		assert.Equal(t, iv.KeyHex, hex.EncodeToString(DeriveKey(s)))
		assert.Equal(t, iv.ApprovalPubHex, hex.EncodeToString(ApprovalPublic(s)))
		assert.Equal(t, iv.MatchingCode, MatchingCode(DeriveKey(s), vol))
		assert.Equal(t, iv.ApprovalSigHex, hex.EncodeToString(Approve(s, vol)))
	}
}

type nameVectors struct {
	SecretB64url    string `json:"secret_b64url"`
	NonceHex        string `json:"nonce_hex"`
	VolunteerKeyHex string `json:"volunteer_name_key_hex"`
	InviterKeyHex   string `json:"inviter_name_key_hex"`
	Blobs           []struct {
		Key     string `json:"key"`
		NameHex string `json:"name_hex"`
		BlobHex string `json:"blob_hex"`
	} `json:"blobs"`
	DeviceSeedHex   string `json:"device_seed_hex"`
	DevicePubHex    string `json:"device_pub_hex"`
	DeviceCode      string `json:"device_matching_code"`
	DeviceApprovSig string `json:"device_approval_sig_hex"`
}

func loadNameVectors(t *testing.T) nameVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/names.json")
	require.NoError(t, err)
	var v nameVectors
	require.NoError(t, json.Unmarshal(data, &v))
	return v
}

func TestNameVectors(t *testing.T) {
	v := loadNameVectors(t)
	s, err := base64.RawURLEncoding.DecodeString(v.SecretB64url)
	require.NoError(t, err)
	assert.Equal(t, v.VolunteerKeyHex, hex.EncodeToString(VolunteerNameKey(s)))
	assert.Equal(t, v.InviterKeyHex, hex.EncodeToString(InviterNameKey(s)))
	for _, b := range v.Blobs {
		key := VolunteerNameKey(s)
		if b.Key == "inviter" {
			key = InviterNameKey(s)
		}
		name := string(unhex(t, b.NameHex))
		got, err := sealName(key, name, unhex(t, v.NonceHex))
		require.NoError(t, err)
		assert.Equal(t, b.BlobHex, hex.EncodeToString(got), "seal %q", name)
		opened, err := OpenName(key, unhex(t, b.BlobHex))
		require.NoError(t, err)
		assert.Equal(t, name, opened)
	}
	priv, err := DeviceKey(unhex(t, v.DeviceSeedHex))
	require.NoError(t, err)
	pubBytes := []byte(priv[32:]) // an ed25519.PrivateKey ends with its public key
	assert.Equal(t, v.DevicePubHex, hex.EncodeToString(pubBytes))
	assert.Equal(t, v.DeviceCode, MatchingCode(DeriveKey(s), pubBytes))
	assert.Equal(t, v.DeviceApprovSig, hex.EncodeToString(Approve(s, pubBytes)))
}

func TestNamesSealUnderTheirOwnKeyOnly(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	require.NoError(t, err)
	blob, err := SealName(VolunteerNameKey(s), "  Ali  ", rand.Reader)
	require.NoError(t, err)
	name, err := OpenName(VolunteerNameKey(s), blob)
	require.NoError(t, err)
	assert.Equal(t, "Ali", name, "surrounding spaces are trimmed")
	_, err = OpenName(InviterNameKey(s), blob)
	assert.ErrorIs(t, err, ErrBlob, "the inviter key cannot open the volunteer's name")
	blob[len(blob)-1] ^= 1
	_, err = OpenName(VolunteerNameKey(s), blob)
	assert.ErrorIs(t, err, ErrBlob, "a tampered blob does not open")
	_, err = OpenName(VolunteerNameKey(s), blob[:10])
	assert.ErrorIs(t, err, ErrBlob)
}

func TestCleanName(t *testing.T) {
	for _, bad := range []string{"", "   ", "a\x00b", "line\nbreak", strings.Repeat("a", MaxNameBytes+1), string([]byte{0xff}),
		"\u202eilA", "Al\u200bi", "\ufeffAli", "A\u2066li"} {
		_, err := CleanName(bad)
		assert.ErrorIs(t, err, ErrName, "%q", bad)
	}
	zwnjName := "\u0645\u06cc\u200c\u0631\u0627" // Persian, with a zero-width non-joiner
	got, err := CleanName(zwnjName)
	require.NoError(t, err, "ZWNJ is part of Persian spelling")
	assert.Equal(t, zwnjName, got)
	_, err = CleanName("\U0001f469\u200d\U0001f4bb")
	assert.NoError(t, err, "ZWJ joins emoji")
	got, err = CleanName(" سارا ")
	require.NoError(t, err)
	assert.Equal(t, "سارا", got)
	got, err = CleanName(strings.Repeat("a", MaxNameBytes))
	require.NoError(t, err)
	assert.Len(t, got, MaxNameBytes)
	blob, err := SealName(InviterNameKey(make([]byte, SecretLen)), got, rand.Reader)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(blob), 512, "the largest blob fits lantern-cloud's limit")
}

func TestLinks(t *testing.T) {
	s := bytes.Repeat([]byte{0xfb}, SecretLen)
	link := Link(s)
	require.True(t, strings.HasPrefix(link, LinkPrefix))
	enc := strings.TrimPrefix(link, LinkPrefix)
	for _, in := range []string{
		link,
		"  " + link + "\n",
		"https://www.lantern.io/help/#" + enc,
		"https://lantern.io/help#" + enc,
		enc,
	} {
		got, err := ParseLink(in)
		require.NoError(t, err, in)
		assert.Equal(t, s, got, in)
	}
	for _, bad := range []string{
		"",
		"https://evil.example/help/#" + enc,
		"http://lantern.io/help/#" + enc,
		"https://lantern.io/other/#" + enc,
		"https://lantern.io/help/?s=" + enc,
		"https://lantern.io/help/#" + enc[:20],
		"https://lantern.io/help/#" + enc + "==",
		enc + "x",
	} {
		_, err := ParseLink(bad)
		assert.ErrorIs(t, err, ErrLink, bad)
	}
}

// TestClaimAndApprovalRoundTrip runs both sides with the package alone: the
// volunteer's claim and the inviter's approval agree on the code, the
// signature verifies, and each name opens on the other side.
func TestClaimAndApprovalRoundTrip(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	require.NoError(t, err)
	seed := bytes.Repeat([]byte{7}, 32)
	c, err := NewClaim(s, seed, "Ali", rand.Reader)
	require.NoError(t, err)
	assert.Equal(t, InviteHash(s), c.InviteHash)
	assert.Regexp(t, `^[0-9]{4}$`, c.Code)
	name, err := OpenName(VolunteerNameKey(s), c.VolunteerBlob)
	require.NoError(t, err)
	assert.Equal(t, "Ali", name)
	assert.Equal(t, c.Code, MatchingCode(DeriveKey(s), c.VolunteerPubkey), "the inviter computes the same code")

	a, err := NewApproval(s, c.VolunteerPubkey, "Sara", rand.Reader)
	require.NoError(t, err)
	assert.Equal(t, Approve(s, c.VolunteerPubkey), a.Signature)
	inviter, err := OpenName(InviterNameKey(s), a.InviterBlob)
	require.NoError(t, err)
	assert.Equal(t, "Sara", inviter)

	_, err = NewClaim(s[:5], seed, "Ali", rand.Reader)
	assert.ErrorIs(t, err, ErrSecret)
	_, err = NewClaim(s, seed, "", rand.Reader)
	assert.ErrorIs(t, err, ErrName)
	_, err = NewApproval(s, c.VolunteerPubkey[:5], "Sara", rand.Reader)
	assert.Error(t, err)
}
```

Run: `go test ./common/helperinvite/ -v`
Expected: the build fails with `undefined: InviteHash` and the other missing names.

- [ ] **Step 4: Write `helperinvite.go`**

```go
// Package helperinvite is the device side of Lantern helper invites: the
// invite secret and link, the keys derived from the secret, the matching
// code, the approval signature and the encrypted display names. The Lantern
// app and the browser widget both use it, so there is one implementation.
//
// The formats are lantern-cloud's (cmd/api/pairing/invite.go and the plan 1
// wire-format table). testdata/lantern-cloud-vectors.json is a verbatim copy
// of lantern-cloud's cmd/api/pairing/testdata/vectors.json, and the two must
// not drift. testdata/names.json holds the name-blob vectors, which
// lantern-cloud cannot produce because it never sees a name.
//
// Secrets, names and codes are private: callers must never log them.
package helperinvite

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// SecretLen is the length of an invite secret in bytes.
const SecretLen = 32

const (
	infoKey           = "lantern-helper-invite-v1/key"
	infoApprove       = "lantern-helper-invite-v1/approve"
	infoVolunteerName = "lantern-helper-invite-v1/volunteer-name"
	infoInviterName   = "lantern-helper-invite-v1/inviter-name"
)

// ErrSecret is returned for a secret that is not SecretLen bytes.
var ErrSecret = errors.New("helperinvite: an invite secret is 32 bytes")

// NewSecret reads a fresh invite secret from r, which is crypto/rand.Reader
// outside tests.
func NewSecret(r io.Reader) ([]byte, error) {
	s := make([]byte, SecretLen)
	if _, err := io.ReadFull(r, s); err != nil {
		return nil, fmt.Errorf("helperinvite: read secret: %w", err)
	}
	return s, nil
}

func expand(s []byte, info string) []byte {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, s, nil, []byte(info)), out); err != nil {
		panic(err) // HKDF-SHA256 cannot fail for 32 bytes of output
	}
	return out
}

// InviteHash is what lantern-cloud stores in place of the secret.
func InviteHash(s []byte) []byte {
	h := sha256.Sum256(s)
	return h[:]
}

// DeriveKey is K, the key of the matching code.
func DeriveKey(s []byte) []byte { return expand(s, infoKey) }

// ApprovalKey is the key an inviter signs an approval with.
func ApprovalKey(s []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(expand(s, infoApprove))
}

// ApprovalPublic is ApprovalKey's public half, registered with the invite.
func ApprovalPublic(s []byte) []byte {
	return []byte(ApprovalKey(s).Public().(ed25519.PublicKey))
}

// ApprovalMessage is what an approval signature covers.
func ApprovalMessage(inviteHash, volunteerPub []byte) []byte {
	msg := make([]byte, 0, len("approve")+len(inviteHash)+len(volunteerPub))
	msg = append(msg, "approve"...)
	msg = append(msg, inviteHash...)
	return append(msg, volunteerPub...)
}

// Approve signs the approval of volunteerPub for the invite with secret s.
func Approve(s, volunteerPub []byte) []byte {
	return ed25519.Sign(ApprovalKey(s), ApprovalMessage(InviteHash(s), volunteerPub))
}

// MatchingCode is the four-digit code both people compare.
func MatchingCode(k, volunteerPub []byte) string {
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte("code"))
	mac.Write(volunteerPub)
	return fmt.Sprintf("%04d", binary.BigEndian.Uint32(mac.Sum(nil)[:4])%10000)
}

// VolunteerNameKey is K_v, which seals the volunteer's name for the inviter.
func VolunteerNameKey(s []byte) []byte { return expand(s, infoVolunteerName) }

// InviterNameKey is K_i, which seals the inviter's name for the volunteer.
func InviterNameKey(s []byte) []byte { return expand(s, infoInviterName) }

// DeviceKey is the volunteer device's keypair for a 32-byte seed.
func DeviceKey(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("helperinvite: a device seed is 32 bytes")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
```

- [ ] **Step 5: Write `names.go`, `link.go` and `claim.go`**

`common/helperinvite/names.go`:

```go
package helperinvite

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

// MaxNameBytes bounds a display name's UTF-8 length. A blob is a 12-byte
// nonce, the name and a 16-byte tag, so the largest is 228 bytes, under
// lantern-cloud's 512.
const MaxNameBytes = 200

var (
	// ErrName is returned for a name that is empty, too long, not UTF-8, or
	// has control or format characters (other than ZWNJ and ZWJ).
	ErrName = errors.New("helperinvite: a name is 1 to 200 bytes of UTF-8 with no control or format characters")
	// ErrBlob is returned for a blob that does not open under the key.
	ErrBlob = errors.New("helperinvite: the name blob does not open")
)

// CleanName trims surrounding white space and checks what is left.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameBytes || !utf8.ValidString(name) {
		return "", ErrName
	}
	for _, r := range name {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != zwnj && r != zwj) {
			return "", ErrName
		}
	}
	return name, nil
}

// Format characters (Unicode Cf) are refused: bidi overrides and invisible
// marks could make one name display as another. ZWNJ and ZWJ are the
// exceptions, because Persian spelling and emoji need them.
const (
	zwnj = '\u200c'
	zwj  = '\u200d'
)

// SealName encrypts name under key with a random nonce from r.
func SealName(key []byte, name string, r io.Reader) ([]byte, error) {
	name, err := CleanName(name)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, fmt.Errorf("helperinvite: read nonce: %w", err)
	}
	return sealName(key, name, nonce)
}

func sealName(key []byte, name string, nonce []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("helperinvite: name key: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(name)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, []byte(name), nil), nil
}

// OpenName decrypts a blob SealName made under key. A blob that does not
// open, or opens to something that is not a valid name, is ErrBlob.
func OpenName(key, blob []byte) (string, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return "", ErrBlob
	}
	if len(blob) < chacha20poly1305.NonceSize+aead.Overhead() {
		return "", ErrBlob
	}
	pt, err := aead.Open(nil, blob[:chacha20poly1305.NonceSize], blob[chacha20poly1305.NonceSize:], nil)
	if err != nil {
		return "", ErrBlob
	}
	name, err := CleanName(string(pt))
	if err != nil {
		return "", ErrBlob
	}
	return name, nil
}
```

`common/helperinvite/link.go`:

```go
package helperinvite

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// LinkPrefix starts every invite link. The secret is in the fragment, so
// browsers and link-preview fetchers never send it to a server.
const LinkPrefix = "https://lantern.io/help/#"

// ErrLink is returned for anything that is not a helper invite link.
var ErrLink = errors.New("helperinvite: not a helper invite link")

// Link is the invite link for secret s.
func Link(s []byte) string { return LinkPrefix + base64.RawURLEncoding.EncodeToString(s) }

// ParseLink returns the secret in an invite link. It accepts
// https://lantern.io/help/#s and https://www.lantern.io/help/#s, with or
// without the slash after help, or the bare base64url secret someone pasted.
// Surrounding white space is ignored.
func ParseLink(link string) ([]byte, error) {
	link = strings.TrimSpace(link)
	frag := link
	if strings.Contains(link, "://") {
		u, err := url.Parse(link)
		if err != nil || u.Scheme != "https" || (u.Host != "lantern.io" && u.Host != "www.lantern.io") ||
			(u.Path != "/help" && u.Path != "/help/") || u.RawQuery != "" {
			return nil, ErrLink
		}
		frag = u.Fragment
	}
	s, err := base64.RawURLEncoding.Strict().DecodeString(frag)
	if err != nil || len(s) != SecretLen {
		return nil, ErrLink
	}
	return s, nil
}
```

`common/helperinvite/claim.go`:

```go
package helperinvite

import (
	"crypto/ed25519"
	"errors"
	"io"
)

// Claim is a volunteer's claim on an invite: the fields of lantern-cloud's
// HelperClaimInviteRequest, and the matching code to show.
type Claim struct {
	InviteHash      []byte
	VolunteerPubkey []byte
	VolunteerBlob   []byte
	Code            string
}

// NewClaim builds the claim that the volunteer whose device key has seed
// sends for secret s, with its name sealed for the inviter.
func NewClaim(s, seed []byte, name string, r io.Reader) (Claim, error) {
	if len(s) != SecretLen {
		return Claim{}, ErrSecret
	}
	priv, err := DeviceKey(seed)
	if err != nil {
		return Claim{}, err
	}
	pub := []byte(priv.Public().(ed25519.PublicKey))
	blob, err := SealName(VolunteerNameKey(s), name, r)
	if err != nil {
		return Claim{}, err
	}
	return Claim{InviteHash: InviteHash(s), VolunteerPubkey: pub, VolunteerBlob: blob,
		Code: MatchingCode(DeriveKey(s), pub)}, nil
}

// Approval is what an inviter sends to approve a claim: the approval
// signature, and its own name sealed for the volunteer.
type Approval struct {
	Signature   []byte
	InviterBlob []byte
}

// NewApproval approves volunteerPub for the invite with secret s.
func NewApproval(s, volunteerPub []byte, inviterName string, r io.Reader) (Approval, error) {
	if len(s) != SecretLen {
		return Approval{}, ErrSecret
	}
	if len(volunteerPub) != ed25519.PublicKeySize {
		return Approval{}, errors.New("helperinvite: a volunteer key is 32 bytes")
	}
	blob, err := SealName(InviterNameKey(s), inviterName, r)
	if err != nil {
		return Approval{}, err
	}
	return Approval{Signature: Approve(s, volunteerPub), InviterBlob: blob}, nil
}
```

- [ ] **Step 6: Run the tests and the wasm build**

```bash
go mod tidy && git diff go.mod
go test -race ./common/helperinvite/ -v
GOOS=js GOARCH=wasm go build -o /dev/null ./cmd
```

Expected:
- `go.mod` moves `golang.org/x/crypto` from indirect to direct, with no other change.
- All tests pass.
- The wasm build succeeds.

- [ ] **Step 7: Commit**

```bash
git add common/helperinvite go.mod go.sum
git commit -m "helperinvite: device-side crypto, links and name blobs for helper invites"
```

---

### Task 5: unbounded — the wasm API for browser volunteers

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-helper-app`

**Files:**
- Create: `common/helperinvite/browser.go`, `common/helperinvite/browser_test.go`
- Modify: `clientcore/broflake.go` (beside `setTargetCountries`)
- Create: `clientcore/engine_rooms_test.go`
- Modify: `clientcore/ui_wasm_impl.go` (beside the `setTargetCountries` export)
- Create: `cmd/helperinvite_wasm.go`
- Modify: `cmd/client_wasm_impl.go` (`main`)

**Interfaces:**
- Consumes:
  - Task 4's `ParseLink`, `NewClaim`, `MatchingCode`, `DeriveKey`, `DeviceKey`, `OpenName` and `InviterNameKey`
  - `WebRTCOptions.SetRoomTickets([]string)` and `SetServePool(bool)` (plan 3)
- Produces, on the widget's JS API:
  - `setRoomTickets(tickets)`, which takes an array of strings or a comma-separated string
  - `setServePool(bool)`
- Produces, on the global `lanternHelperInvite`:
  - `newSeed()` → base64 string
  - `claim(secret, seedB64, name)` → `{body, code}` or `{error}`
  - `matchingCode(secret, seedB64)` → string or `{error}`
  - `openInviterName(secret, blobB64)` → string or `{error}`
  - `secret` is the bare base64url secret the page holds in memory, not `location.href`
- Produces, in Go: `func BrowserNewSeed(r io.Reader) (string, error)`, `func BrowserClaim(secret, seedB64, name string, r io.Reader) (body, code string, err error)`, `func BrowserMatchingCode(secret, seedB64 string) (string, error)` and `func BrowserOpenInviterName(secret, blobB64 string) (string, error)`. The landing page follows the "Browser volunteer protocol" in Global Constraints.

- [ ] **Step 1: Write the failing tests**

`common/helperinvite/browser_test.go`:

```go
package helperinvite

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBrowserClaim checks the string API the landing page uses: the body is
// exactly what lantern-cloud's claim endpoint takes, in standard base64 as
// protojson reads it.
func TestBrowserClaim(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	require.NoError(t, err)
	seedB64, err := BrowserNewSeed(rand.Reader)
	require.NoError(t, err)
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	require.NoError(t, err)
	require.Len(t, seed, 32)

	secret := base64.RawURLEncoding.EncodeToString(s)
	body, code, err := BrowserClaim(secret, seedB64, "Ali", rand.Reader)
	require.NoError(t, err)
	var req struct {
		InviteHash      []byte `json:"invite_hash"`
		VolunteerPubkey []byte `json:"volunteer_pubkey"`
		VolunteerBlob   []byte `json:"volunteer_blob"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	assert.Equal(t, InviteHash(s), req.InviteHash)
	priv, _ := DeviceKey(seed)
	assert.Equal(t, []byte(priv[32:]), req.VolunteerPubkey)
	name, err := OpenName(VolunteerNameKey(s), req.VolunteerBlob)
	require.NoError(t, err)
	assert.Equal(t, "Ali", name)

	again, err := BrowserMatchingCode(secret, seedB64)
	require.NoError(t, err)
	assert.Equal(t, code, again, "the page can recompute the code after a reload")

	a, err := NewApproval(s, req.VolunteerPubkey, "Sara", rand.Reader)
	require.NoError(t, err)
	inviter, err := BrowserOpenInviterName(secret, base64.StdEncoding.EncodeToString(a.InviterBlob))
	require.NoError(t, err)
	assert.Equal(t, "Sara", inviter)
}

func TestBrowserRejectsBadInput(t *testing.T) {
	good, _ := BrowserNewSeed(rand.Reader)
	link := Link(bytes.Repeat([]byte{1}, SecretLen))
	_, _, err := BrowserClaim("https://evil.example/#x", good, "Ali", rand.Reader)
	assert.ErrorIs(t, err, ErrLink)
	_, _, err = BrowserClaim(link, "not base64!", "Ali", rand.Reader)
	assert.Error(t, err)
	_, _, err = BrowserClaim(link, good, "", rand.Reader)
	assert.ErrorIs(t, err, ErrName)
	_, err = BrowserOpenInviterName(link, base64.StdEncoding.EncodeToString([]byte("short")))
	assert.ErrorIs(t, err, ErrBlob)
}
```

`clientcore/engine_rooms_test.go`:

```go
package clientcore

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The wasm exports only forward to these; they run natively here.
func TestEngineRoomSetters(t *testing.T) {
	opt := NewDefaultWebRTCOptions()
	b := &BroflakeEngine{rtcOpt: opt}
	b.setRoomTickets([]string{"aa.bb"})
	assert.Equal(t, []string{"aa.bb"}, opt.CurrentRoomTickets())
	b.setServePool(false)
	assert.False(t, opt.ServesPool())
	b.setServePool(true)
	assert.True(t, opt.ServesPool())
	b.setRoomTickets(nil)
	assert.Empty(t, opt.CurrentRoomTickets())

	idle := &BroflakeEngine{}
	idle.setRoomTickets([]string{"aa.bb"}) // no options yet: a no-op, not a panic
	idle.setServePool(false)
}
```

Run: `go test ./common/helperinvite/ ./clientcore/ -run 'Browser|EngineRoomSetters' -v`
Expected: the build fails with `undefined: BrowserNewSeed` and `b.setRoomTickets undefined`.

- [ ] **Step 2: Write `browser.go`**

```go
package helperinvite

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// BrowserNewSeed returns a fresh device-key seed in standard base64, for the
// landing page to keep in localStorage.
func BrowserNewSeed(r io.Reader) (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, seed); err != nil {
		return "", fmt.Errorf("helperinvite: read seed: %w", err)
	}
	return base64.StdEncoding.EncodeToString(seed), nil
}

func browserSeed(seedB64 string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("helperinvite: a device seed is 32 bytes of standard base64")
	}
	return seed, nil
}

// BrowserClaim is NewClaim for the landing page. secret is the bare
// base64url secret the page read from its fragment and holds in memory
// (ParseLink also accepts a full link). It returns the JSON body of
// POST /v1/helper/invites/claim and the code to show.
func BrowserClaim(secret, seedB64, name string, r io.Reader) (body, code string, err error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", "", err
	}
	seed, err := browserSeed(seedB64)
	if err != nil {
		return "", "", err
	}
	c, err := NewClaim(s, seed, name, r)
	if err != nil {
		return "", "", err
	}
	b, err := json.Marshal(struct {
		InviteHash      []byte `json:"invite_hash"`
		VolunteerPubkey []byte `json:"volunteer_pubkey"`
		VolunteerBlob   []byte `json:"volunteer_blob"`
	}{c.InviteHash, c.VolunteerPubkey, c.VolunteerBlob})
	if err != nil {
		return "", "", err
	}
	return string(b), c.Code, nil
}

// BrowserMatchingCode recomputes the code for a claim the page already sent.
func BrowserMatchingCode(secret, seedB64 string) (string, error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", err
	}
	seed, err := browserSeed(seedB64)
	if err != nil {
		return "", err
	}
	priv, err := DeviceKey(seed)
	if err != nil {
		return "", err
	}
	return MatchingCode(DeriveKey(s), []byte(priv.Public().(ed25519.PublicKey))), nil
}

// BrowserOpenInviterName opens the inviter_blob a new pairing carries in
// ListPairings, given in standard base64 as lantern-cloud sends it. The page
// must call it before it forgets the secret: the blob comes only once.
func BrowserOpenInviterName(secret, blobB64 string) (string, error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", err
	}
	blob, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil {
		return "", ErrBlob
	}
	return OpenName(InviterNameKey(s), blob)
}
```

- [ ] **Step 3: Export rooms and serve-pool on the widget**

In `clientcore/broflake.go`, after `setTargetCountries`, add:

```go
// setRoomTickets gives the widget the volunteer tickets of its helper
// pairings. Each worker reads them at its next genesis message.
func (b *BroflakeEngine) setRoomTickets(tickets []string) {
	if b.rtcOpt == nil {
		return
	}
	b.rtcOpt.SetRoomTickets(tickets)
}

// setServePool sets whether the widget also serves the general pool ("help
// others too") or only its pairings' rooms.
func (b *BroflakeEngine) setServePool(serve bool) {
	if b.rtcOpt == nil {
		return
	}
	b.rtcOpt.SetServePool(serve)
}
```

Neither logs anything: tickets are credentials.

In `clientcore/ui_wasm_impl.go`, after the `setTargetCountries` export, add:

```go
	// setRoomTickets(["…", "…"]) or setRoomTickets("…,…") gives the widget its
	// helper pairings' volunteer tickets (from POST /v1/helper/tickets).
	// setRoomTickets([]) leaves every room. Safe to call at any time.
	js.Global().Get(ui.ID).Set(
		"setRoomTickets",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			var tickets []string
			if len(args) > 0 {
				switch v := args[0]; v.Type() {
				case js.TypeString:
					if s := v.String(); s != "" {
						tickets = strings.Split(s, ",")
					}
				case js.TypeObject:
					for i := 0; i < v.Length(); i++ {
						if e := v.Index(i); e.Type() == js.TypeString {
							tickets = append(tickets, e.String())
						}
					}
				}
			}
			ui.BroflakeEngine.setRoomTickets(tickets)
			return nil
		}),
	)

	// setServePool(false) limits the widget to its pairings' rooms ("help
	// others too" off); setServePool(true) also serves everyone.
	js.Global().Get(ui.ID).Set(
		"setServePool",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			serve := true
			if len(args) > 0 && args[0].Type() == js.TypeBoolean {
				serve = args[0].Bool()
			}
			ui.BroflakeEngine.setServePool(serve)
			return nil
		}),
	)
```

- [ ] **Step 4: Export `lanternHelperInvite`**

`cmd/helperinvite_wasm.go`:

```go
//go:build wasm

package main

import (
	"crypto/rand"
	"syscall/js"

	"github.com/getlantern/broflake/common/helperinvite"
)

// installHelperInvite exposes the helper-invite primitives the landing page
// needs to volunteer from a browser. Each function returns a string or an
// object; on failure, an object with "error" set to a fixed message. Nothing
// here logs: the arguments are secrets and names.
func installHelperInvite() {
	str := func(args []js.Value, i int) string {
		if i < len(args) && args[i].Type() == js.TypeString {
			return args[i].String()
		}
		return ""
	}
	fail := func(err error) interface{} {
		return js.ValueOf(map[string]interface{}{"error": err.Error()})
	}
	api := map[string]interface{}{
		"newSeed": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			s, err := helperinvite.BrowserNewSeed(rand.Reader)
			if err != nil {
				return fail(err)
			}
			return s
		}),
		"claim": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			body, code, err := helperinvite.BrowserClaim(str(args, 0), str(args, 1), str(args, 2), rand.Reader)
			if err != nil {
				return fail(err)
			}
			return js.ValueOf(map[string]interface{}{"body": body, "code": code})
		}),
		"matchingCode": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			code, err := helperinvite.BrowserMatchingCode(str(args, 0), str(args, 1))
			if err != nil {
				return fail(err)
			}
			return code
		}),
		"openInviterName": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			name, err := helperinvite.BrowserOpenInviterName(str(args, 0), str(args, 1))
			if err != nil {
				return fail(err)
			}
			return name
		}),
	}
	js.Global().Set("lanternHelperInvite", js.ValueOf(api))
}
```

In `cmd/client_wasm_impl.go`, in `main`, before `select {}`, add `installHelperInvite()`.

- [ ] **Step 5: Run the tests and the wasm build**

```bash
go test -race ./common/helperinvite/ ./clientcore/ -v -run 'Browser|Claim|Name|Link|Vectors|EngineRoomSetters'
go test -race ./...
GOOS=js GOARCH=wasm go build -o /dev/null ./cmd
```

Expected: all pass, and the wasm build succeeds.

- [ ] **Step 6: Telemetry audit and commit**

Run: `git diff origin/main -- . ':!*_test.go' ':!*.json' | grep -nE '^\+.*(slog\.|log\.|fmt\.Print)'`
Expected: no lines.

```bash
git add common/helperinvite/browser.go common/helperinvite/browser_test.go clientcore/broflake.go clientcore/engine_rooms_test.go clientcore/ui_wasm_impl.go \
  cmd/helperinvite_wasm.go cmd/client_wasm_impl.go
git commit -m "wasm: room tickets, serve-pool and helper-invite primitives for browser volunteers"
```

- [ ] **Step 7: Open the PR; [Human] merge**

Open the PR for Tasks 4–5 with `skills:create-pr`, base `main`. After the merge, record the merge commit:

```bash
git fetch origin && UB_SHA=$(git rev-parse origin/main) && echo "$UB_SHA"
```

Publishing a new `widget.wasm` for the landing page belongs to plan 5.

---

### Task 6: radiance — the helper relay and ticket-source invalidation

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-app`

**Files:**
- Create: `helper/relay.go`, `helper/relay_test.go`
- Modify: `helper/tickets.go` (`Source.Invalidate`, with a generation counter that `refresh` checks)
- Modify: `ipc/server.go` (an endpoint constant and its route), `ipc/client.go`, `ipc/types.go`
- Create: `ipc/helper_test.go`
- Modify: `backend/radiance.go` (a `helperRelay` field, construction, `HelperRelay`)

**Interfaces:**
- Consumes:
  - plan 3 Task 12's `helper.Source`, `helper.NewSource(ctx, fetch)`, and its `due` (next fetch) and `busy` (backoff) fields, as merged in radiance's `helper/tickets.go`
  - `common.NewRequestWithHeaders`, `kindling.HTTPClient()`, `common.GetBaseURL()`
- Produces:
  - `const helper.OpRefreshTickets = "tickets/refresh"`
  - `var helper.ErrUnknownOp error`
  - `type helper.RelayResult struct { Status int; Body json.RawMessage; Error string }`
  - `func helper.NewRelayer(httpClient *http.Client, baseURL string, onChange func()) *Relayer`. `onChange` runs after a claim, decision or ending succeeds, and for `tickets/refresh`. The backend passes its one shared ticket `Source`'s `Invalidate`. No global registry is involved.
  - `func (*Relayer) Relay(ctx context.Context, op string, body []byte) (RelayResult, error)`
  - `func (*helper.Source) Invalidate()`, `func helper.IsRelayOp(op string) bool`
  - `func (*backend.LocalBackend) HelperRelay(ctx context.Context, op string, body []byte) (helper.RelayResult, error)`
  - IPC `POST /helper/relay`, `type ipc.HelperRelayRequest struct { Op string; Body json.RawMessage }`
  - `func (*ipc.Client) HelperRelay(ctx context.Context, op string, body []byte) (helper.RelayResult, error)`
  - lantern-core's engine relays through this (Task 10).

- [ ] **Step 1: Create the worktree**

**Gates.** Do not start until all three hold, in this order:
1. **[Human]** lantern-box #380 (plan 3 Tasks 10–11) is merged, off Iran's peak hours, and tagged. Call the tag `LB_TAG`.
2. Plan 3's radiance PR (Tasks 12–14) has switched its lantern-box pin from the #380 pseudo-version to `LB_TAG`, with `go mod tidy`, as ruling R1 requires.
3. **[Human]** that radiance PR is merged.

Check with:

```bash
git -C ~/go/src/github.com/getlantern/radiance fetch origin
git -C ~/go/src/github.com/getlantern/radiance show origin/main:go.mod | grep lantern-box     # must name LB_TAG, not a pseudo-version
git -C ~/go/src/github.com/getlantern/radiance show origin/main:helper/tickets.go | head -5   # must print the package comment
```

Then:

```bash
cd ~/go/src/github.com/getlantern/radiance
git worktree add -b fisk/helper-app ~/go/src/github.com/getlantern/radiance-helper-app origin/main
cd ~/go/src/github.com/getlantern/radiance-helper-app
```

- [ ] **Step 2: Write the failing tests**

`helper/relay_test.go`:

```go
package helper

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/radiance/common"
	"github.com/getlantern/radiance/common/settings"
)

func TestRelayForwardsAllowedOps(t *testing.T) {
	require.NoError(t, settings.Set(settings.DeviceIDKey, "device-R"))
	var gotPath, gotDevice, gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotDevice, gotType = r.URL.Path, r.Header.Get(common.DeviceIDHeader), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"pairing_id":"p"}`))
	}))
	defer srv.Close()
	rl := NewRelayer(srv.Client(), srv.URL+"/v1", nil)
	for op, path := range relayPaths {
		res, err := rl.Relay(context.Background(), op, []byte(`{"x":1}`))
		require.NoError(t, err, op)
		assert.Equal(t, path, gotPath[len("/v1"):], op)
		assert.Equal(t, "device-R", gotDevice)
		assert.Equal(t, "application/json", gotType)
		assert.JSONEq(t, `{"x":1}`, gotBody)
		assert.Equal(t, http.StatusOK, res.Status)
		assert.JSONEq(t, `{"pairing_id":"p"}`, string(res.Body))
		assert.Empty(t, res.Error)
	}
	res, err := rl.Relay(context.Background(), "pairings", nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", gotBody, "an empty body is sent as {}")
	assert.Equal(t, http.StatusOK, res.Status)
}

func TestRelayRefusesOtherOps(t *testing.T) {
	rl := NewRelayer(http.DefaultClient, "http://127.0.0.1:1/v1", nil)
	for _, op := range []string{"", "tickets", "ticket-key", "../peer/register", "invites/../../peer"} {
		_, err := rl.Relay(context.Background(), op, nil)
		assert.ErrorIs(t, err, ErrUnknownOp, op)
	}
}

func TestRelayReportsErrorsAsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invite already claimed or expired", http.StatusConflict)
	}))
	defer srv.Close()
	res, err := NewRelayer(srv.Client(), srv.URL+"/v1", nil).Relay(context.Background(), "invites/claim", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, res.Status)
	assert.Equal(t, "invite already claimed or expired", res.Error)
	assert.Nil(t, res.Body)
}

func TestRelayRejectsANonJSONSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	}))
	defer srv.Close()
	_, err := NewRelayer(srv.Client(), srv.URL+"/v1", nil).Relay(context.Background(), "pairings", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "captive", "an error never quotes the body")
}

// A pairing change runs onChange, so the backend's ticket Source fetches at
// its next read and a new pairing's rooms work within one read, not one
// refresh interval.
func TestRelayCallsOnChangeWhenPairingsChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"pairing_id": "p"})
	}))
	defer srv.Close()
	var changes atomic.Int32
	rl := NewRelayer(srv.Client(), srv.URL+"/v1", func() { changes.Add(1) })
	for _, op := range []string{"invites", "invites/get", "pairings"} {
		_, err := rl.Relay(context.Background(), op, []byte(`{}`))
		require.NoError(t, err)
	}
	assert.Zero(t, changes.Load(), "creating, reading and listing change no pairing")
	for _, op := range []string{"invites/claim", "invites/decide", "pairings/end", OpRefreshTickets} {
		_, err := rl.Relay(context.Background(), op, []byte(`{}`))
		require.NoError(t, err)
	}
	assert.Equal(t, int32(4), changes.Load())
}

func TestSourceInvalidate(t *testing.T) {
	var fetches atomic.Int32
	src := NewSource(context.Background(), func(context.Context) (Tickets, error) {
		fetches.Add(1)
		return Tickets{Lifetime: 15 * time.Minute}, nil
	})
	src.Volunteer()
	require.Eventually(t, func() bool { return !src.isFetching() && fetches.Load() == 1 }, time.Second, 5*time.Millisecond)
	src.Volunteer()
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), fetches.Load(), "a fresh source does not refetch")
	src.Invalidate()
	src.Volunteer()
	require.Eventually(t, func() bool { return !src.isFetching() && fetches.Load() == 2 }, time.Second, 5*time.Millisecond)
}

// TestSourceInvalidateDuringAFetchIsNotLost covers the race: a pairing that
// changes while a fetch is in flight must not be hidden by that fetch's
// success, whose tickets may predate the change.
func TestSourceInvalidateDuringAFetchIsNotLost(t *testing.T) {
	release := make(chan struct{})
	var fetches atomic.Int32
	src := NewSource(context.Background(), func(context.Context) (Tickets, error) {
		if fetches.Add(1) == 1 {
			<-release
		}
		return Tickets{Lifetime: 15 * time.Minute}, nil
	})
	src.Volunteer()
	require.Eventually(t, func() bool { return fetches.Load() == 1 }, time.Second, 5*time.Millisecond)
	src.Invalidate()
	close(release)
	require.Eventually(t, func() bool { return !src.isFetching() }, time.Second, 5*time.Millisecond)
	src.Volunteer()
	require.Eventually(t, func() bool { return fetches.Load() == 2 }, time.Second, 5*time.Millisecond,
		"the fetch that began before the change does not count as fresh")
}

// TestSourceInvalidateDuringADisabledFetchIsNotLost: a flag-off volunteer's
// fetch answers 404 (ErrDisabled) until its pairing exists. An approval that
// lands during that fetch must not wait out DisabledInterval.
func TestSourceInvalidateDuringADisabledFetchIsNotLost(t *testing.T) {
	release := make(chan struct{})
	var fetches atomic.Int32
	src := NewSource(context.Background(), func(context.Context) (Tickets, error) {
		if fetches.Add(1) == 1 {
			<-release
			return Tickets{}, ErrDisabled
		}
		return Tickets{Lifetime: 15 * time.Minute}, nil
	})
	src.Volunteer()
	require.Eventually(t, func() bool { return fetches.Load() == 1 }, time.Second, 5*time.Millisecond)
	src.Invalidate()
	close(release)
	require.Eventually(t, func() bool { return !src.isFetching() }, time.Second, 5*time.Millisecond)
	src.Volunteer()
	require.Eventually(t, func() bool { return fetches.Load() == 2 }, time.Second, 5*time.Millisecond,
		"the 404 that raced the approval does not hold the next fetch for an hour")
}
```

`ipc/helper_test.go`:

```go
package ipc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/radiance/backend"
)

func serveHelper(t *testing.T, api *localapi, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	r := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestHelperRelayEndpoint(t *testing.T) {
	api := newLocalAPI(&backend.LocalBackend{}, false)
	w := serveHelper(t, api, http.MethodPost, helperRelayEndpoint, HelperRelayRequest{Op: "peer/register"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "unknown helper operation\n", w.Body.String())

	w = serveHelper(t, api, http.MethodPost, helperRelayEndpoint, HelperRelayRequest{Op: "pairings"})
	assert.Equal(t, http.StatusBadGateway, w.Code, "a backend without a relayer cannot reach lantern-cloud")
	assert.Equal(t, "helper relay failed\n", w.Body.String(), "the error is fixed text")

	w = serveHelper(t, api, http.MethodGet, helperRelayEndpoint, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
```

Run: `go test ./helper/ ./ipc/ -run 'Relay|HelperRelay' -v`
Expected: the build fails with `undefined: NewRelayer` and `undefined: helperRelayEndpoint`.

- [ ] **Step 3: Write `helper/relay.go`**

```go
package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/getlantern/radiance/common"
	"github.com/getlantern/radiance/common/settings"
)

// relayPaths are the HelperService operations the app may relay, by the name
// it uses, mapped to their paths under the API base URL. Only these reach
// lantern-cloud: the relay is not a general proxy.
var relayPaths = map[string]string{
	"invites":        "/helper/invites",
	"invites/claim":  "/helper/invites/claim",
	"invites/get":    "/helper/invites/get",
	"invites/decide": "/helper/invites/decide",
	"pairings":       "/helper/pairings",
	"pairings/end":   "/helper/pairings/end",
}

// OpRefreshTickets is a local operation. It runs onChange, so the ticket
// Source fetches at its next read, for a pairing the app has just seen appear.
const OpRefreshTickets = "tickets/refresh"

// invalidatingOps change this device's pairings when they succeed.
var invalidatingOps = map[string]bool{"invites/claim": true, "invites/decide": true, "pairings/end": true}

const (
	relayTimeout   = 30 * time.Second
	maxRelayBody   = 64 << 10
	maxRelayErrLen = 200
)

// ErrUnknownOp is Relay's error for an operation it does not relay.
var ErrUnknownOp = errors.New("helper: unknown relay operation")

// RelayResult is lantern-cloud's answer to a relayed request: its status, its
// JSON body on a 2xx, and its error text otherwise.
type RelayResult struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Relayer sends the app's helper-invite requests to lantern-cloud as this
// device, through the same circumvention-capable client as the rest of the
// API. The app's Go core builds every body. They carry invite hashes,
// public keys and encrypted names, never an invite secret, and nothing here
// logs a body or an answer.
type Relayer struct {
	httpClient *http.Client
	baseURL    string
	onChange   func()
}

// NewRelayer returns a Relayer. baseURL includes the API version segment, as
// common.GetBaseURL() returns it.
// onChange, which may be nil, runs after an operation that changes this
// device's pairings succeeds.
func NewRelayer(httpClient *http.Client, baseURL string, onChange func()) *Relayer {
	return &Relayer{httpClient: httpClient, baseURL: baseURL, onChange: onChange}
}

func (r *Relayer) changed() {
	if r.onChange != nil {
		r.onChange()
	}
}

// Relay sends body to op's HelperService path. A transport failure is an
// error, and any HTTP status is a result. After a claim, decision or ending
// succeeds, onChange runs.
func (r *Relayer) Relay(ctx context.Context, op string, body []byte) (RelayResult, error) {
	if op == OpRefreshTickets {
		r.changed()
		return RelayResult{Status: http.StatusOK, Body: json.RawMessage("{}")}, nil
	}
	path, ok := relayPaths[op]
	if !ok {
		return RelayResult{}, ErrUnknownOp
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	req, err := common.NewRequestWithHeaders(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return RelayResult{}, fmt.Errorf("build helper request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if val := settings.GetString(settings.FeatureOverridesKey); val != "" {
		req.Header.Set("X-Lantern-Feature-Override", val)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return RelayResult{}, errors.New("helper request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRelayBody+1))
	if err != nil || len(data) > maxRelayBody {
		return RelayResult{}, errors.New("helper response unreadable")
	}
	out := RelayResult{Status: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if !json.Valid(data) {
			return RelayResult{}, errors.New("helper response is not JSON")
		}
		out.Body = data
		if invalidatingOps[op] {
			r.changed()
		}
		return out, nil
	}
	text := strings.ToValidUTF8(strings.TrimSpace(string(data)), "")
	if len(text) > maxRelayErrLen {
		text = text[:maxRelayErrLen]
	}
	out.Error = text
	return out, nil
}
```

Add to the same file:

```go
// IsRelayOp reports whether op is a HelperService operation Relay forwards.
func IsRelayOp(op string) bool {
	_, ok := relayPaths[op]
	return ok
}
```

The transport error is a fixed string, because `net/http` errors quote the URL.

- [ ] **Step 4: `Source.Invalidate`, without losing a change to a fetch in flight**

In `helper/tickets.go`, as plan 3 merged it (`NewSource(ctx, fetch)`, the `busy` backoff for 429 and 503 answers):

- Add a field to `Source`:

```go
	gen uint64 // bumped by Invalidate; a fetch started under an older gen is stale
```

- In `read`, replace `go s.refresh()` with `go s.refresh(s.gen)`.
- Change `func (s *Source) refresh()` to `func (s *Source) refresh(gen uint64)`. After the `switch` that sets `s.due`, still under the lock (before `s.mu.Unlock()`), add:

```go
	if s.gen != gen && s.busy == 0 {
		// The pairings changed while this fetch was in flight, so whatever it
		// got (tickets, a 404 for a device not yet paired, an error) may
		// predate the change: fetch again at the next read. A busy backoff
		// stands.
		s.due = time.Time{}
	}
```

This covers every outcome, not only success. Without it, an invalidation during a fetch that returned `ErrDisabled` would wait out `DisabledInterval` (an hour). That 404 is the normal answer for a flag-off volunteer just before its pairing exists (Task 1).

- Add:

```go
// Invalidate makes the next read fetch, because this device's pairings have
// changed. A fetch already in flight does not satisfy it (see gen). A Source
// backing off after lantern-cloud said it was busy keeps its backoff.
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	if s.busy == 0 {
		s.due = time.Time{}
	}
}
```

- [ ] **Step 5: IPC and backend**

In `ipc/server.go`, add to the endpoint constants:

```go
	// Helper invites
	helperRelayEndpoint    = "/helper/relay"
	helperActivityEndpoint = "/helper/activity"
```

Register the route in `newLocalAPI` after the Unbounded routes. It skips the tracer middleware, which records error bodies:

```go
	// Helper invites. Not traced: the tracer records error bodies, and these
	// carry invite hashes and encrypted names.
	mux.HandleFunc("POST "+helperRelayEndpoint, s.helperRelayHandler)
```

Add the handler:

```go
const maxHelperRelayRequest = 64 << 10

// helperRelayHandler relays one helper-invite request for the app. Its
// errors are fixed text: they reach logs and must not quote a body.
func (s *localapi) helperRelayHandler(w http.ResponseWriter, r *http.Request) {
	var req HelperRelayRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHelperRelayRequest)).Decode(&req); err != nil {
		http.Error(w, "bad helper relay request", http.StatusBadRequest)
		return
	}
	res, err := s.backend(r.Context()).HelperRelay(r.Context(), req.Op, req.Body)
	switch {
	case errors.Is(err, helper.ErrUnknownOp):
		http.Error(w, "unknown helper operation", http.StatusBadRequest)
	case err != nil:
		http.Error(w, "helper relay failed", http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}
```

Add `"github.com/getlantern/radiance/helper"` to the imports of `ipc/server.go`.

In `ipc/types.go`, add `"encoding/json"` to the imports and:

```go
// HelperRelayRequest is one helper-invite request the app relays to
// lantern-cloud: a HelperService operation and its JSON body.
type HelperRelayRequest struct {
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body,omitempty"`
}
```

In `ipc/client.go`, add `"github.com/getlantern/radiance/helper"` to the imports and:

```go
////////////////////
// Helper invites //
////////////////////

// HelperRelay relays one helper-invite request to lantern-cloud (see
// helper.Relayer). lantern-cloud's status is in the result; an error means
// the relay itself failed.
func (c *Client) HelperRelay(ctx context.Context, op string, body []byte) (helper.RelayResult, error) {
	var res helper.RelayResult
	err := c.doJSON(ctx, http.MethodPost, helperRelayEndpoint, HelperRelayRequest{Op: op, Body: body}, &res)
	return res, err
}
```

In `backend/radiance.go`:
- Add the field `helperRelay *helper.Relayer` to `LocalBackend`.
- In `NewLocalBackend`, right after plan 3's `r.helperTickets = helper.NewSource(ctx, …)`, add:

```go
	// The widget and the tunnel share r.helperTickets, so invalidating it
	// reaches both when the app's pairings change.
	r.helperRelay = helper.NewRelayer(kindling.HTTPClient(), common.GetBaseURL(), func() {
		if r.helperTickets != nil {
			r.helperTickets.Invalidate()
		}
	})
```
- `backend/radiance.go` already imports `github.com/getlantern/radiance/helper`: plan 3 Task 13 added it.
- Add:

```go
var errHelperRelayUnavailable = errors.New("helper relay unavailable")

// HelperRelay sends one helper-invite request for the app (see
// helper.Relayer). The op is checked first, so an unknown op is refused even
// on a backend whose relayer failed to build.
func (r *LocalBackend) HelperRelay(ctx context.Context, op string, body []byte) (helper.RelayResult, error) {
	if op != helper.OpRefreshTickets && !helper.IsRelayOp(op) {
		return helper.RelayResult{}, helper.ErrUnknownOp
	}
	if r.helperRelay == nil {
		return helper.RelayResult{}, errHelperRelayUnavailable
	}
	return r.helperRelay.Relay(ctx, op, body)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./helper/ ./ipc/ ./backend/ -v -run 'Relay|HelperRelay|Source|Fetcher|Invalidate' && make test`
Expected: PASS, including plan 3's `helper` tests and the full module.

- [ ] **Step 7: Commit**

```bash
git add helper/relay.go helper/relay_test.go helper/tickets.go ipc/server.go ipc/client.go ipc/types.go ipc/helper_test.go backend/radiance.go
git commit -m "helper: relay the app's helper-invite requests; refresh tickets when pairings change, even mid-fetch"
```

---

### Task 7: radiance — "help others too" applies at once

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-app`

**Files:**
- Modify: `peer/peer.go` (after `runRotation`)
- Modify: `peer/rotation_test.go`
- Modify: `backend/peer_share.go` (`peerController`, a new `reregisterPeer`)
- Modify: `backend/radiance.go` (`PatchSettings`)
- Modify: `backend/radiance_test.go` (`fakePeerController`, new tests)

**Interfaces:**
- Consumes:
  - `(*peer.Client).rotateCreds` and `servePool()` (plan 2)
  - `settings.PeerServePoolKey`
  - plan 3 Task 13's widget, which re-reads the setting within `roomSyncInterval` (30 s)
- Produces:
  - `var peer.ErrNotActive = errors.New("peer: no session is running")`
  - `func (c *peer.Client) Reregister(ctx context.Context) error`
  - `peerController.Reregister(ctx context.Context) error`
  - `(*LocalBackend).reregisterPeer()`
  - The app's toggle (Task 14) patches `peer_serve_pool`, and direct mode applies it within one re-register instead of at the hourly rotation. This closes plan 2's deferral.

**The cost of a toggle:** re-registering rotates the credentials and rebuilds the samizdat inbound, so every open session drops and reconnects within seconds, paired ones included. The widget does the same when "help others too" turns off. Task 14 adds a caption under the switch saying so (`help_others_too_note`).

- [ ] **Step 1: Write the failing tests**

Append to `peer/rotation_test.go`:

```go
// TestClient_ReregisterSendsTheCurrentServePool checks that turning "help
// others too" off reaches lantern-cloud at once, not at the next hourly
// rotation.
func TestClient_ReregisterSendsTheCurrentServePool(t *testing.T) {
	srv := newStubServer(t)
	port := &portBoxes{}
	c := newTestClient(t, &fakeForwarder{externalIP: "203.0.113.42"}, &fakeBoxService{}, srv, func(cfg *Config) {
		cfg.CredRotationInterval = time.Hour
		cfg.HeartbeatInterval = time.Hour
		cfg.BuildBoxService = port.build
	})
	require.NoError(t, settings.InitSettings(t.TempDir()))
	t.Cleanup(settings.Reset)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	require.NoError(t, settings.Set(settings.PeerServePoolKey, false))
	assert.Nil(t, srv.lastRegisterReq.Load().(RegisterRequest).ServePool, "Start sent the default")
	require.NoError(t, c.Reregister(context.Background()))
	assert.Equal(t, int64(2), srv.registerCount.Load())
	req := srv.lastRegisterReq.Load().(RegisterRequest)
	require.NotNil(t, req.ServePool)
	assert.False(t, *req.ServePool)
	assert.True(t, c.IsActive(), "re-registering keeps the peer serving")
}

func TestClient_ReregisterWithoutASession(t *testing.T) {
	c := newTestClient(t, &fakeForwarder{externalIP: "203.0.113.42"}, &fakeBoxService{}, newStubServer(t))
	assert.ErrorIs(t, c.Reregister(context.Background()), ErrNotActive)
}
```

Add `"github.com/getlantern/radiance/common/settings"` to the imports of `peer/rotation_test.go`.

In `backend/radiance_test.go`, add a `reregisterCalls atomic.Int64` field to `fakePeerController`, and the method:

```go
func (f *fakePeerController) Reregister(_ context.Context) error {
	f.reregisterCalls.Add(1)
	return nil
}
```

Append the tests:

```go
func TestPatchSettings_ServePoolChangeReregistersARunningPeer(t *testing.T) {
	fake := &fakePeerController{}
	b := newPeerTestBackend(t, fake)
	fake.active.Store(true)
	require.NoError(t, b.PatchSettings(settings.Settings{settings.PeerServePoolKey: false}))
	b.peerWG.Wait()
	assert.Equal(t, int64(1), fake.reregisterCalls.Load())

	require.NoError(t, b.PatchSettings(settings.Settings{settings.PeerServePoolKey: false}))
	b.peerWG.Wait()
	assert.Equal(t, int64(1), fake.reregisterCalls.Load(), "an unchanged value re-registers nothing")
}

func TestPatchSettings_ServePoolChangeLeavesAStoppedPeerAlone(t *testing.T) {
	fake := &fakePeerController{}
	b := newPeerTestBackend(t, fake)
	require.NoError(t, b.PatchSettings(settings.Settings{settings.PeerServePoolKey: false}))
	b.peerWG.Wait()
	assert.Zero(t, fake.reregisterCalls.Load())
}

// A patch that also turns sharing on registers once, through Start, with the
// new value already persisted.
func TestPatchSettings_ServePoolWithToggleOnDoesNotReregister(t *testing.T) {
	fake := &fakePeerController{}
	b := newPeerTestBackend(t, fake)
	require.NoError(t, b.PatchSettings(settings.Settings{
		settings.PeerShareEnabledKey: true, settings.PeerServePoolKey: false}))
	b.peerWG.Wait()
	assert.Equal(t, int64(1), fake.startCalls.Load())
	assert.Zero(t, fake.reregisterCalls.Load())
}
```

Run: `go test ./peer/ ./backend/ -run 'Reregister|ServePool' -v`
Expected: the build fails with `c.Reregister undefined` and `undefined: ErrNotActive`.

- [ ] **Step 2: Implement**

In `peer/peer.go`, after `runRotation`, add:

```go
// ErrNotActive is Reregister's error when no session runs.
var ErrNotActive = errors.New("peer: no session is running")

// Reregister re-registers the running session now rather than at its next
// rotation, so a change to what registration sends, such as peer_serve_pool,
// takes effect at once. It rotates the credentials exactly as the hourly
// rotation does, and like it turns a libbox panic into an error.
func (c *Client) Reregister(ctx context.Context) (err error) {
	if !c.IsActive() {
		return ErrNotActive
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("peer re-register panicked: %v", r)
		}
	}()
	return c.rotateCreds(ctx)
}
```

In `backend/peer_share.go`, add `Reregister(ctx context.Context) error` to `peerController`, and add:

```go
// reregisterPeer re-registers a running peer in the background, under the
// toggle lock so it cannot overlap a Start or Stop, after "help others too"
// changes. Re-registering rotates the credentials and rebuilds the inbound,
// so every open session, paired ones included, drops and reconnects within
// seconds; the switch's caption warns of it (help_others_too_note). The
// widget picks the setting up by itself within roomSyncInterval, and plan 3
// restarts it when helping others turns off, with the same brief drop.
func (r *LocalBackend) reregisterPeer() {
	if r.peerClient == nil {
		return
	}
	r.peerWG.Add(1)
	go func() {
		defer r.peerWG.Done()
		r.peerToggleMu.Lock()
		defer r.peerToggleMu.Unlock()
		if !r.peerClient.IsActive() {
			return
		}
		ctx, cancel := context.WithTimeout(r.ctx, peerToggleTimeout)
		defer cancel()
		if err := r.peerClient.Reregister(ctx); err != nil {
			slog.Warn("peer re-register after a serve-pool change failed; the next rotation applies it", "error", err)
		}
	}()
}
```

In `backend/radiance.go` `PatchSettings`, after the `if _, ok := diff[settings.UnboundedKey]; ok { … }` block, add:

```go
	// "Help others too" is sent when the peer registers, so a running peer
	// re-registers now. A patch that also toggled sharing on has just
	// registered with the new value through Start.
	if _, ok := diff[settings.PeerServePoolKey]; ok && !peerRequested {
		r.reregisterPeer()
	}
```

- [ ] **Step 3: Run the tests**

Run: `go test -race ./peer/ ./backend/ -v -run 'Reregister|ServePool|PatchSettings|Rotation' && make test`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add peer/peer.go peer/rotation_test.go backend/peer_share.go backend/radiance.go backend/radiance_test.go
git commit -m "peer: re-register at once when help-others-too changes"
```

---

### Task 8: radiance — today's helping activity

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-app`

**Files:**
- Create: `helper/activity.go`, `helper/activity_test.go`
- Modify: `ipc/server.go`, `ipc/client.go`, `ipc/helper_test.go`
- Modify: `backend/radiance.go` (a field, construction, `Start`, `HelperActivity`)

**Interfaces:**
- Consumes:
  - `peer.Snapshot{Running, Epoch, BytesDown, BytesUp}` through `(*LocalBackend).PeerRelaySnapshot()`
  - `unbounded.CurrentSnapshot()`
  - `atomicfile.WriteFile`
- Produces:
  - `type helper.Activity struct { Day string; ActiveSeconds int64; RelayedBytes uint64 }`, with JSON `day`, `active_seconds` and `relayed_bytes`
  - `type helper.Sharing func() (running bool, epoch string, bytes uint64)`
  - `func helper.NewActivityTracker(path string, read Sharing) *ActivityTracker`
  - `func (*ActivityTracker) Sample(elapsed time.Duration)`, `func (*ActivityTracker) Today() Activity` and `func (*ActivityTracker) Run(ctx context.Context, every time.Duration)`
  - `func (*LocalBackend) HelperActivity() helper.Activity`
  - IPC `GET /helper/activity`, and `func (*ipc.Client) HelperActivity(ctx context.Context) (helper.Activity, error)`
  - The engine (Task 9) shows these as the section totals (open question 1).

- [ ] **Step 1: Write the failing tests**

`helper/activity_test.go`:

```go
package helper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSharing struct {
	running bool
	epoch   string
	bytes   uint64
}

func (f *fakeSharing) read() (bool, string, uint64) { return f.running, f.epoch, f.bytes }

func TestActivityCountsTimeAndBytesWhileSharing(t *testing.T) {
	sh := &fakeSharing{running: true, epoch: "e1"}
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	tr := NewActivityTracker(filepath.Join(t.TempDir(), "a.json"), sh.read)
	tr.now = func() time.Time { return now }

	tr.Sample(time.Minute) // primes the byte baseline
	sh.bytes = 1000
	tr.Sample(time.Minute)
	sh.running = false
	sh.bytes = 1500
	tr.Sample(time.Minute)
	got := tr.Today()
	assert.Equal(t, "2026-10-23", got.Day)
	assert.Equal(t, int64(120), got.ActiveSeconds, "only samples taken while sharing count")
	assert.Equal(t, uint64(1500), got.RelayedBytes)

	sh.epoch, sh.bytes = "e2", 10 // a restarted counter
	tr.Sample(time.Minute)
	assert.Equal(t, uint64(1500), tr.Today().RelayedBytes, "a new epoch re-baselines rather than adding")

	now = now.Add(24 * time.Hour)
	assert.Equal(t, Activity{Day: "2026-10-24"}, tr.Today(), "a new day starts at zero")
}

func TestActivitySavesOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	sh := &fakeSharing{epoch: "e"}
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	tr := NewActivityTracker(path, sh.read)
	tr.now = func() time.Time { return now }
	tr.Sample(time.Minute)
	tr.save()
	info, err := os.Stat(path)
	require.NoError(t, err, "the first sample of a day starts the file")
	first := info.ModTime()
	require.NoError(t, os.Chtimes(path, first.Add(-time.Hour), first.Add(-time.Hour)))
	for range 5 {
		tr.Sample(time.Minute) // not sharing: nothing changes
		tr.save()
	}
	info, err = os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, first.Add(-time.Hour).Unix(), info.ModTime().Unix(), "idle samples write nothing")
	sh.running = true
	tr.Sample(time.Minute)
	tr.save()
	info, err = os.Stat(path)
	require.NoError(t, err)
	assert.NotEqual(t, first.Add(-time.Hour).Unix(), info.ModTime().Unix(), "a change is saved")
}

func TestActivitySurvivesARestartTheSameDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	sh := &fakeSharing{running: true, epoch: "e"}
	tr := NewActivityTracker(path, sh.read)
	tr.Sample(30 * time.Second)
	tr.save()
	again := NewActivityTracker(path, sh.read)
	assert.Equal(t, int64(30), again.Today().ActiveSeconds)
	require.FileExists(t, path)
}
```

Append to `ipc/helper_test.go`:

```go
func TestHelperActivityEndpoint(t *testing.T) {
	api := newLocalAPI(&backend.LocalBackend{}, false)
	w := serveHelper(t, api, http.MethodGet, helperActivityEndpoint, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var a struct {
		Day           string `json:"day"`
		ActiveSeconds int64  `json:"active_seconds"`
		RelayedBytes  uint64 `json:"relayed_bytes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &a))
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}$`, a.Day)
	assert.Zero(t, a.ActiveSeconds)
}
```

Run: `go test ./helper/ ./ipc/ -run 'Activity' -v`
Expected: the build fails with `undefined: NewActivityTracker`.

- [ ] **Step 2: Write `helper/activity.go`**

```go
package helper

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/getlantern/radiance/common/atomicfile"
)

// Activity is today's sharing on this device: how long it ran and how much it
// relayed, on the device's local calendar. It counts paired and pool traffic
// together, as Advanced stats does, and says nothing about whom it helped.
type Activity struct {
	Day           string `json:"day"`
	ActiveSeconds int64  `json:"active_seconds"`
	RelayedBytes  uint64 `json:"relayed_bytes"`
}

// Sharing reads what sharing is doing now: whether either mode runs, an
// identifier for its counters' current epochs, and their cumulative bytes.
type Sharing func() (running bool, epoch string, bytes uint64)

// ActivityTracker builds today's Activity from periodic samples and keeps it
// in a file, so a restart on the same day carries on.
type ActivityTracker struct {
	path string
	read Sharing
	now  func() time.Time

	mu        sync.Mutex
	cur       Activity
	saved     Activity // what the file holds; save writes only when cur differs
	lastEpoch string
	lastBytes uint64
	primed    bool
}

// NewActivityTracker returns a tracker that saves to path and samples read.
func NewActivityTracker(path string, read Sharing) *ActivityTracker {
	t := &ActivityTracker{path: path, read: read, now: time.Now}
	if data, err := os.ReadFile(path); err == nil {
		var a Activity
		if json.Unmarshal(data, &a) == nil {
			t.cur, t.saved = a, a
		}
	}
	return t
}

func dayOf(t time.Time) string { return t.Local().Format("2006-01-02") }

// Today returns today's totals.
func (t *ActivityTracker) Today() Activity {
	t.mu.Lock()
	defer t.mu.Unlock()
	if today := dayOf(t.now()); t.cur.Day != today {
		return Activity{Day: today}
	}
	return t.cur
}

// Sample adds one interval: elapsed counts as active if sharing runs now, and
// the bytes relayed since the last sample are added unless the counters were
// reset in between.
func (t *ActivityTracker) Sample(elapsed time.Duration) {
	running, epoch, bytes := t.read()
	t.mu.Lock()
	defer t.mu.Unlock()
	if today := dayOf(t.now()); t.cur.Day != today {
		t.cur = Activity{Day: today}
	}
	if running {
		t.cur.ActiveSeconds += int64(elapsed / time.Second)
	}
	if t.primed && epoch == t.lastEpoch && bytes >= t.lastBytes {
		t.cur.RelayedBytes += bytes - t.lastBytes
	}
	t.lastEpoch, t.lastBytes, t.primed = epoch, bytes, true
}

// Run samples every interval until ctx ends, saving after a sample that
// changed something. A device that never shares writes nothing but the
// first sample of each day.
func (t *ActivityTracker) Run(ctx context.Context, every time.Duration) {
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			t.save()
			return
		case <-tk.C:
			t.Sample(every)
			t.save()
		}
	}
}

func (t *ActivityTracker) save() {
	t.mu.Lock()
	cur := t.cur
	unchanged := cur == t.saved
	t.mu.Unlock()
	if unchanged {
		return
	}
	data, err := json.Marshal(cur)
	if err != nil {
		return
	}
	if err := atomicfile.WriteFile(t.path, data, 0o600); err != nil {
		slog.Debug("helper: saving activity failed", "error", err)
		return
	}
	t.mu.Lock()
	t.saved = cur
	t.mu.Unlock()
}
```

- [ ] **Step 3: IPC and backend**

In `ipc/server.go` `newLocalAPI`, after the relay route, add:

```go
	mux.HandleFunc("GET "+helperActivityEndpoint, traced(s.helperActivityHandler))
```

and the handler:

```go
func (s *localapi) helperActivityHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend(r.Context()).HelperActivity())
}
```

In `ipc/client.go`:

```go
// HelperActivity reads today's sharing totals (see helper.Activity).
func (c *Client) HelperActivity(ctx context.Context) (helper.Activity, error) {
	var a helper.Activity
	err := c.doJSON(ctx, http.MethodGet, helperActivityEndpoint, nil, &a)
	return a, err
}
```

In `backend/radiance.go`:
- Add the field `helperActivity *helper.ActivityTracker` to `LocalBackend`.
- In `NewLocalBackend`, after `backend = r`, add:

```go
	r.helperActivity = helper.NewActivityTracker(filepath.Join(dataDir, "helper-activity.json"), r.sharingCounters)
```

- In `Start`, after `r.resumePeerShareIfEnabled()`, add `go r.helperActivity.Run(r.ctx, time.Minute)`.
- Add:

```go
// sharingCounters reads both sharing modes' snapshots for the activity
// tracker.
func (r *LocalBackend) sharingCounters() (bool, string, uint64) {
	ps := r.PeerRelaySnapshot()
	us := unbounded.CurrentSnapshot()
	return ps.Running || us.Running, ps.Epoch + "/" + us.Epoch, ps.BytesDown + ps.BytesUp + us.BytesDown + us.BytesUp
}

// HelperActivity is today's sharing totals for the app's "You're helping"
// section.
func (r *LocalBackend) HelperActivity() helper.Activity {
	if r.helperActivity == nil {
		return helper.Activity{Day: time.Now().Local().Format("2006-01-02")}
	}
	return r.helperActivity.Today()
}
```

`backend/radiance.go` already imports `path/filepath`, `time` and `errors`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./helper/ ./ipc/ ./backend/ -v -run 'Activity|Helper' && make test`
Expected: PASS.

- [ ] **Step 5: Telemetry audit**

Run: `git diff origin/main -- . ':!go.sum' ':!*_test.go' | grep -nE '^\+.*(slog\.|log\.)'`
Expected:
- `"helper: saving activity failed"` with `error`, an I/O error that carries no identifier
- `"peer re-register after a serve-pool change failed; the next rotation applies it"` with `error`

No body, op argument, ticket or pairing ID appears.

- [ ] **Step 6: Commit**

```bash
git add helper/activity.go helper/activity_test.go ipc/server.go ipc/client.go ipc/helper_test.go backend/radiance.go
git commit -m "helper: today's sharing time and bytes for the app"
```

- [ ] **Step 7: Open the PR; [Human] merge**

Open the PR for Tasks 6–8 with `skills:create-pr`, base `main`. After the merge:

```bash
git fetch origin && RAD_SHA=$(git rev-parse origin/main) && echo "$RAD_SHA"
```

Releasing radiance to users happens through the lantern app release (Task 15).

---

### Task 9: lantern — `lantern-core/helper`, the device-side engine

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Create: `lantern-core/helper/errors.go`, `state.go`, `engine.go`, `ops.go`
- Create: `lantern-core/helper/engine_test.go`, `lantern-core/helper/fakecloud_test.go`
- Modify: `go.mod`, `go.sum` (broflake bumped to `UB_SHA`; `golang.org/x/crypto` becomes direct)

**Interfaces:**
- Consumes: Task 4's `helperinvite` package, and the HelperService JSON (plan 1 plus Task 2's `online` and `created_unix`).
- Produces:
  - `type Relay func(ctx context.Context, op string, body []byte) (status int, resp []byte, errText string, err error)`
  - `type ActivityFunc func(ctx context.Context) (Activity, error)`
  - `type Activity struct { Day string; ActiveSeconds int64; RelayedBytes uint64 }`, with the same JSON as radiance's
  - `func NewEngine(dir string, relay Relay, activity ActivityFunc) *Engine`
  - `func (*Engine) Call(ctx context.Context, op string, args []byte) ([]byte, error)`
  - `func ErrorJSON(err error) string`
  - `type Error struct{ Code string }`
  - `const InviteTTL = 48 * time.Hour`

**Ops (`Call`), each returning JSON:**

| op | args | result |
|---|---|---|
| `unlock` | `{"key": <std base64 of 32 bytes>}` | View |
| `view` | `{}` | View, with no network |
| `refresh` | `{}` | View, after `invites/get` for each local invite, `pairings` and the activity read |
| `create_invite` | `{"name"}` | `{"invite_id","link"}` |
| `decide` | `{"invite_id","approve": bool}` | View |
| `forget_invite` | `{"invite_id"}` | View. A claimed invite is rejected; an unclaimed one becomes a hidden tombstone, and any later claim on it is rejected at the next refresh |
| `claim` | `{"link","name"}` | `{"claim_id","code"}` |
| `end_pairing` | `{"pairing_id"}` | View |
| `end_all_helpers` | `{}` | View (ends every pairing where this device is helped, and cancels every invite as `forget_invite` does) |

The View is `{"unlocked","my_name","invites":[{"id","state","link"?,"expires_unix","code"?,"volunteer_name"?}],"claims":[{"id","code","expires_unix"}],"helpers":[{"pairing_id","name","online"?,"since_unix"}],"helping":[{"pairing_id","name","since_unix"}],"activity"?:{…},"new_helping","keystore_reset"?}`. A `link` appears only on an unclaimed invite, so it can be re-shared. `keystore_reset` appears only in `unlock`'s answer, when the old state could not be opened and was kept as `state.bin.bak`.

**Error codes** (`{"error": code}`):
- `locked`: no `unlock` yet
- `disabled`: the gate's 404, so helper invites are off for this client
- `not_found`
- `already_claimed`
- `no_pending_claim`
- `too_many_invites`
- `too_many_pairings`
- `bad_link`
- `bad_name`
- `own_invite`
- `unavailable`: transport failure, 5xx, or a malformed answer
- `internal`

- [ ] **Step 1: Create the worktree and bump broflake**

```bash
cd ~/go/src/github.com/getlantern/lantern
git fetch origin
git worktree add -b fisk/helper-app ~/go/src/github.com/getlantern/lantern-helper-app origin/main
cd ~/go/src/github.com/getlantern/lantern-helper-app
go get github.com/getlantern/broflake@$UB_SHA
go mod tidy
git diff go.mod
```

Expected: `go.mod` changes broflake's pseudo-version, and nothing else unexpected. The x/crypto change follows in Step 5's tidy.

- [ ] **Step 2: Write the in-memory lantern-cloud for tests**

`lantern-core/helper/fakecloud_test.go`:

```go
package helper

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/getlantern/broflake/common/helperinvite"
)

type fakeInvite struct {
	inviter, volunteer                       string
	approvalPub, volunteerPub, volunteerBlob []byte
	state, pairingID                         string
}

type fakePairing struct {
	id, inviter, volunteer string
	inviterBlob            []byte
}

// fakeCloud answers HelperService as lantern-cloud does, in memory, for any
// number of devices. Each engine relays through relay(device).
type fakeCloud struct {
	mu       sync.Mutex
	invites  map[string]*fakeInvite
	pairings []*fakePairing
	online   map[string]bool
	disabled bool
	down     bool
	// flagOff lists devices without the helper_invites flag. They get
	// lantern-cloud's gate (Task 1): claims always; pairings, ending and
	// tickets only while they hold a live claim or a pairing; nothing else.
	flagOff map[string]bool
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{invites: map[string]*fakeInvite{}, online: map[string]bool{}, flagOff: map[string]bool{}}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// flagFreeAllowed mirrors lantern-cloud's volunteerOrNotFound for a flag-off
// device. The caller holds f.mu.
func (f *fakeCloud) flagFreeAllowed(device, op string) bool {
	switch op {
	case "invites/claim", "tickets/refresh":
		return true
	case "pairings", "pairings/end":
		for _, p := range f.pairings {
			if p.volunteer == device {
				return true
			}
		}
		for _, inv := range f.invites {
			if inv.state == "claimed" && inv.volunteer == device {
				return true
			}
		}
	}
	return false
}

func (f *fakeCloud) relay(device string) Relay {
	return func(_ context.Context, op string, body []byte) (int, []byte, string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			return 0, nil, "", fmt.Errorf("connection refused")
		}
		if f.disabled || (f.flagOff[device] && !f.flagFreeAllowed(device, op)) {
			return 404, nil, "404 page not found", nil
		}
		var req struct {
			InviteHash      []byte `json:"invite_hash"`
			ApprovalPubkey  []byte `json:"approval_pubkey"`
			VolunteerPubkey []byte `json:"volunteer_pubkey"`
			VolunteerBlob   []byte `json:"volunteer_blob"`
			Approve         bool   `json:"approve"`
			ApprovalSig     []byte `json:"approval_sig"`
			InviterBlob     []byte `json:"inviter_blob"`
			PairingID       string `json:"pairing_id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return 400, nil, "bad body", nil
		}
		key := hex.EncodeToString(req.InviteHash)
		switch op {
		case "invites":
			f.invites[key] = &fakeInvite{inviter: device, approvalPub: req.ApprovalPubkey, state: "pending"}
			return 200, []byte("{}"), "", nil
		case "invites/claim":
			inv := f.invites[key]
			if inv == nil || inv.state != "pending" || inv.inviter == device {
				return 409, nil, "invite already claimed or expired", nil
			}
			inv.state, inv.volunteer, inv.volunteerPub, inv.volunteerBlob = "claimed", device, req.VolunteerPubkey, req.VolunteerBlob
			return 200, []byte("{}"), "", nil
		case "invites/get":
			inv := f.invites[key]
			if inv == nil || inv.inviter != device {
				return 404, nil, "no such invite", nil
			}
			return 200, mustJSON(map[string]any{"state": inv.state, "volunteer_pubkey": inv.volunteerPub,
				"volunteer_blob": inv.volunteerBlob, "pairing_id": inv.pairingID}), "", nil
		case "invites/decide":
			inv := f.invites[key]
			if inv == nil || inv.inviter != device {
				return 404, nil, "no such invite", nil
			}
			if inv.state != "claimed" {
				return 409, nil, "invite has no pending claim", nil
			}
			if !req.Approve {
				inv.state = "rejected"
				return 200, []byte("{}"), "", nil
			}
			if !ed25519.Verify(inv.approvalPub, helperinvite.ApprovalMessage(req.InviteHash, inv.volunteerPub), req.ApprovalSig) {
				return 403, nil, "approval signature does not match", nil
			}
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.pairings)+1)
			inv.state, inv.pairingID = "approved", id
			f.pairings = append(f.pairings, &fakePairing{id: id, inviter: device, volunteer: inv.volunteer, inviterBlob: req.InviterBlob})
			return 200, mustJSON(map[string]any{"pairing_id": id}), "", nil
		case "pairings":
			out := []map[string]any{}
			for _, p := range f.pairings {
				switch device {
				case p.inviter:
					out = append(out, map[string]any{"pairing_id": p.id, "role": "inviter",
						"online": f.online[p.volunteer], "created_unix": strconv.Itoa(1800000000)})
				case p.volunteer:
					m := map[string]any{"pairing_id": p.id, "role": "volunteer", "created_unix": "1800000000"}
					if len(p.inviterBlob) > 0 {
						m["inviter_blob"] = p.inviterBlob
						p.inviterBlob = nil // delivered once, as lantern-cloud does
					}
					out = append(out, m)
				}
			}
			return 200, mustJSON(map[string]any{"pairings": out}), "", nil
		case "pairings/end":
			for i, p := range f.pairings {
				if p.id == req.PairingID && (p.inviter == device || p.volunteer == device) {
					f.pairings = slices.Delete(f.pairings, i, i+1)
					return 200, []byte("{}"), "", nil
				}
			}
			return 404, nil, "no such pairing", nil
		case "tickets/refresh":
			return 200, []byte("{}"), "", nil
		}
		return 400, nil, "unknown op", nil
	}
}
```

- [ ] **Step 3: Write the failing tests**

`lantern-core/helper/engine_test.go`:

```go
package helper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common/helperinvite"
)

var testKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))

func newTestEngine(t *testing.T, cloud *fakeCloud, device, dir string) *Engine {
	t.Helper()
	e := NewEngine(dir, cloud.relay(device), nil)
	call(t, e, "unlock", map[string]any{"key": testKey})
	return e
}

func call(t *testing.T, e *Engine, op string, args map[string]any) map[string]any {
	t.Helper()
	out, err := e.Call(context.Background(), op, mustJSON(args))
	require.NoError(t, err, op)
	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	return m
}

func callErr(t *testing.T, e *Engine, op string, args map[string]any) string {
	t.Helper()
	_, err := e.Call(context.Background(), op, mustJSON(args))
	var he *Error
	require.True(t, errors.As(err, &he), "%s: want *Error, got %v", op, err)
	return he.Code
}

func list(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestInviteClaimApproveEnd(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara-device", t.TempDir())
	aliDir := t.TempDir()
	ali := newTestEngine(t, cloud, "ali-device", aliDir)

	inv := call(t, sara, "create_invite", map[string]any{"name": "Sara"})
	link := inv["link"].(string)
	require.True(t, strings.HasPrefix(link, helperinvite.LinkPrefix))
	pending := list(call(t, sara, "view", nil), "invites")
	require.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0]["state"])
	assert.Equal(t, link, pending[0]["link"], "an unclaimed invite can be re-shared")

	cl := call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	code := cl["code"].(string)
	assert.Regexp(t, `^[0-9]{4}$`, code)
	assert.Len(t, list(call(t, ali, "view", nil), "claims"), 1)

	sv := call(t, sara, "refresh", nil)
	claimed := list(sv, "invites")
	require.Len(t, claimed, 1)
	assert.Equal(t, "claimed", claimed[0]["state"])
	assert.Equal(t, code, claimed[0]["code"], "both sides compute the same code")
	assert.Equal(t, "Ali", claimed[0]["volunteer_name"])
	assert.Nil(t, claimed[0]["link"], "a claimed invite no longer offers its link")

	sv = call(t, sara, "decide", map[string]any{"invite_id": claimed[0]["id"], "approve": true})
	assert.Empty(t, list(sv, "invites"), "a decided invite is forgotten, with its secret")
	helpers := list(sv, "helpers")
	require.Len(t, helpers, 1)
	assert.Equal(t, "Ali", helpers[0]["name"])
	assert.Nil(t, helpers[0]["online"])

	cloud.online["ali-device"] = true
	helpers = list(call(t, sara, "refresh", nil), "helpers")
	assert.Equal(t, true, helpers[0]["online"])

	av := call(t, ali, "refresh", nil)
	helping := list(av, "helping")
	require.Len(t, helping, 1)
	assert.Equal(t, "Sara", helping[0]["name"], "the inviter's name opens with the pending claim's key")
	assert.Equal(t, true, av["new_helping"])
	assert.Empty(t, list(av, "claims"))
	assert.Equal(t, false, call(t, ali, "refresh", nil)["new_helping"], "only the first refresh reports it")

	again := newTestEngine(t, cloud, "ali-device", aliDir)
	assert.Equal(t, "Sara", list(call(t, again, "view", nil), "helping")[0]["name"], "names survive a restart")

	call(t, sara, "end_pairing", map[string]any{"pairing_id": helpers[0]["pairing_id"]})
	assert.Empty(t, list(call(t, sara, "view", nil), "helpers"))
	assert.Empty(t, list(call(t, ali, "refresh", nil), "helping"), "the other side learns at its next refresh")
}

func TestASecondClaimIsAlreadyClaimed(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
	call(t, newTestEngine(t, cloud, "eve", t.TempDir()), "claim", map[string]any{"link": link, "name": "Eve"})
	assert.Equal(t, CodeAlreadyClaimed,
		callErr(t, newTestEngine(t, cloud, "ali", t.TempDir()), "claim", map[string]any{"link": link, "name": "Ali"}))
}

func TestRejectBurnsTheInvite(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
	call(t, newTestEngine(t, cloud, "eve", t.TempDir()), "claim", map[string]any{"link": link, "name": "Eve"})
	id := list(call(t, sara, "refresh", nil), "invites")[0]["id"]
	sv := call(t, sara, "decide", map[string]any{"invite_id": id, "approve": false})
	assert.Empty(t, list(sv, "invites"))
	assert.Empty(t, list(sv, "helpers"))
	assert.Equal(t, CodeAlreadyClaimed,
		callErr(t, newTestEngine(t, cloud, "ali", t.TempDir()), "claim", map[string]any{"link": link, "name": "Ali"}))
}

func TestClaimRefusals(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
	assert.Equal(t, CodeOwnInvite, callErr(t, sara, "claim", map[string]any{"link": link, "name": "Sara"}))
	ali := newTestEngine(t, cloud, "ali", t.TempDir())
	assert.Equal(t, CodeBadLink, callErr(t, ali, "claim", map[string]any{"link": "https://evil.example/#x", "name": "Ali"}))
	assert.Equal(t, CodeBadName, callErr(t, ali, "claim", map[string]any{"link": link, "name": "  "}))
	assert.Equal(t, CodeBadName, callErr(t, sara, "create_invite", map[string]any{"name": ""}))
	first := call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	second := call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	assert.Equal(t, first, second, "claiming the same link again returns the same claim")
}

func TestStatusErrors(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	cloud.disabled = true
	assert.Equal(t, CodeDisabled, callErr(t, sara, "create_invite", map[string]any{"name": "Sara"}))
	cloud.disabled, cloud.down = false, true
	assert.Equal(t, CodeUnavailable, callErr(t, sara, "refresh", nil))
	fresh := NewEngine(t.TempDir(), cloud.relay("x"), nil)
	assert.Equal(t, CodeLocked, callErr(t, fresh, "view", nil))
	assert.JSONEq(t, `{"error":"locked"}`, ErrorJSON(&Error{Code: CodeLocked}))
	assert.JSONEq(t, `{"error":"internal"}`, ErrorJSON(errors.New("boom")))
}

func TestStateIsSealed(t *testing.T) {
	cloud := newFakeCloud()
	dir := t.TempDir()
	sara := newTestEngine(t, cloud, "sara", dir)
	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
	secret := strings.TrimPrefix(link, helperinvite.LinkPrefix)
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "Sara")
	assert.NotContains(t, string(data), secret)
	info, err := os.Stat(filepath.Join(dir, stateFile))
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	other := NewEngine(dir, cloud.relay("sara"), nil)
	out := call(t, other, "unlock", map[string]any{"key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))})
	assert.Empty(t, list(out, "invites"), "another key reads nothing")
	assert.Equal(t, true, out["keystore_reset"], "and says so rather than silently starting over")
	assert.FileExists(t, filepath.Join(dir, stateFile+".bak"), "the unreadable state is kept")
	assert.Nil(t, call(t, other, "view", nil)["keystore_reset"], "reported once, at unlock")
}

// TestVolunteerAbroadWithoutTheFlag covers a volunteer that lantern-cloud
// gates by its claim or pairing (Task 1): after its claim is rejected, and
// after its last pairing ends, the gate answers 404, and the engine must
// read that as "nothing", not as an error that strands the claim or row.
func TestVolunteerAbroadWithoutTheFlag(t *testing.T) {
	cloud := newFakeCloud()
	cloud.flagOff["ali"] = true
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	ali := newTestEngine(t, cloud, "ali", t.TempDir())

	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"]
	call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	assert.Len(t, list(call(t, ali, "refresh", nil), "claims"), 1, "a live claim passes the gate")
	id := list(call(t, sara, "refresh", nil), "invites")[0]["id"]
	call(t, sara, "decide", map[string]any{"invite_id": id, "approve": false})
	assert.Empty(t, list(call(t, ali, "refresh", nil), "claims"), "the rejected claim is dropped, not left waiting")

	link = call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"]
	call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	id = list(call(t, sara, "refresh", nil), "invites")[0]["id"]
	call(t, sara, "decide", map[string]any{"invite_id": id, "approve": true})
	require.Len(t, list(call(t, ali, "refresh", nil), "helping"), 1)
	pid := list(call(t, sara, "view", nil), "helpers")[0]["pairing_id"]
	call(t, sara, "end_pairing", map[string]any{"pairing_id": pid})
	assert.Empty(t, list(call(t, ali, "refresh", nil), "helping"), "the ended pairing's row is removed")
}

// TestCancelledInviteRejectsALaterClaim covers the tombstone: a cancelled,
// unclaimed invite is hidden, and a claim that arrives later is rejected at
// the next refresh instead of waiting for an approval that cannot come.
func TestCancelledInviteRejectsALaterClaim(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	inv := call(t, sara, "create_invite", map[string]any{"name": "Sara"})
	sv := call(t, sara, "forget_invite", map[string]any{"invite_id": inv["invite_id"]})
	assert.Empty(t, list(sv, "invites"), "a cancelled invite is hidden")
	call(t, newTestEngine(t, cloud, "eve", t.TempDir()), "claim", map[string]any{"link": inv["link"], "name": "Eve"})
	assert.Empty(t, list(call(t, sara, "refresh", nil), "invites"))
	assert.Empty(t, list(call(t, sara, "view", nil), "helpers"))
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	for _, fi := range cloud.invites {
		assert.Equal(t, "rejected", fi.state, "the late claim was rejected")
	}
}

// TestEachInviteKeepsItsOwnName checks that approval seals the name given for
// that invite, not whichever name was typed last.
func TestEachInviteKeepsItsOwnName(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	first := call(t, sara, "create_invite", map[string]any{"name": "Sara"})
	call(t, sara, "create_invite", map[string]any{"name": "S. from work"})
	ali := newTestEngine(t, cloud, "ali", t.TempDir())
	call(t, ali, "claim", map[string]any{"link": first["link"], "name": "Ali"})
	for _, iv := range list(call(t, sara, "refresh", nil), "invites") {
		if iv["state"] == "claimed" {
			call(t, sara, "decide", map[string]any{"invite_id": iv["id"], "approve": true})
		}
	}
	assert.Equal(t, "Sara", list(call(t, ali, "refresh", nil), "helping")[0]["name"])
}

func TestExpiredInvitesAndClaimsArePruned(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
	ali := newTestEngine(t, cloud, "ali", t.TempDir())
	call(t, ali, "claim", map[string]any{"link": link, "name": "Ali"})
	later := time.Now().Add(InviteTTL + time.Hour)
	sara.now = func() time.Time { return later }
	ali.now = func() time.Time { return later }
	assert.Empty(t, list(call(t, sara, "refresh", nil), "invites"))
	assert.Empty(t, list(call(t, ali, "refresh", nil), "claims"))
}

func TestEndAllHelpers(t *testing.T) {
	cloud := newFakeCloud()
	sara := newTestEngine(t, cloud, "sara", t.TempDir())
	for _, v := range []string{"ali", "bo"} {
		link := call(t, sara, "create_invite", map[string]any{"name": "Sara"})["link"].(string)
		call(t, newTestEngine(t, cloud, v, t.TempDir()), "claim", map[string]any{"link": link, "name": v})
		inv := list(call(t, sara, "refresh", nil), "invites")
		call(t, sara, "decide", map[string]any{"invite_id": inv[0]["id"], "approve": true})
	}
	call(t, sara, "create_invite", map[string]any{"name": "Sara"})
	require.Len(t, list(call(t, sara, "view", nil), "helpers"), 2)
	sv := call(t, sara, "end_all_helpers", nil)
	assert.Empty(t, list(sv, "helpers"))
	assert.Empty(t, list(sv, "invites"))
	assert.Empty(t, cloud.pairings)
}

func TestRefreshReadsActivity(t *testing.T) {
	cloud := newFakeCloud()
	e := NewEngine(t.TempDir(), cloud.relay("ali"), func(context.Context) (Activity, error) {
		return Activity{Day: "2026-10-23", ActiveSeconds: 3600, RelayedBytes: 42}, nil
	})
	call(t, e, "unlock", map[string]any{"key": testKey})
	act := call(t, e, "refresh", nil)["activity"].(map[string]any)
	assert.Equal(t, "2026-10-23", act["day"])
	assert.Equal(t, float64(3600), act["active_seconds"])
	assert.Equal(t, float64(42), act["relayed_bytes"])
}
```

Run: `go test ./lantern-core/helper/ -v`
Expected: the build fails with `undefined: Relay` and the other missing names.

- [ ] **Step 4: Write the engine**

`lantern-core/helper/errors.go`:

```go
package helper

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Error is an operation's failure, reported to the UI as {"error": Code}.
// Codes are fixed strings and safe to log; nothing else about a failure is.
type Error struct{ Code string }

func (e *Error) Error() string { return "helper: " + e.Code }

const (
	CodeLocked          = "locked"
	CodeDisabled        = "disabled"
	CodeNotFound        = "not_found"
	CodeAlreadyClaimed  = "already_claimed"
	CodeNoPendingClaim  = "no_pending_claim"
	CodeTooManyInvites  = "too_many_invites"
	CodeTooManyPairings = "too_many_pairings"
	CodeBadLink         = "bad_link"
	CodeBadName         = "bad_name"
	CodeOwnInvite       = "own_invite"
	CodeUnavailable     = "unavailable"
	CodeInternal        = "internal"
)

func errCode(code string) *Error { return &Error{Code: code} }

// ErrorJSON is the bridge's answer for err: {"error": code}.
func ErrorJSON(err error) string {
	code := CodeInternal
	var he *Error
	if errors.As(err, &he) {
		code = he.Code
	}
	b, _ := json.Marshal(map[string]string{"error": code})
	return string(b)
}

// statusError maps a HelperService status to a code. The gate's 404 for a
// client the flag is off for reads "404 page not found"; other 404s are a
// missing invite or pairing.
func statusError(op string, status int, errText string) *Error {
	switch {
	case status == http.StatusNotFound && strings.Contains(errText, "page not found"):
		return errCode(CodeDisabled)
	case status == http.StatusNotFound:
		return errCode(CodeNotFound)
	case status == http.StatusConflict && op == "invites/claim":
		return errCode(CodeAlreadyClaimed)
	case status == http.StatusConflict && op == "invites/decide":
		return errCode(CodeNoPendingClaim)
	case status == http.StatusTooManyRequests && strings.Contains(errText, "too many requests"):
		// The flag-free gate's rate limit (lantern-cloud Task 1): retry later.
		return errCode(CodeUnavailable)
	case status == http.StatusTooManyRequests && strings.Contains(errText, "pairings"):
		return errCode(CodeTooManyPairings)
	case status == http.StatusTooManyRequests:
		return errCode(CodeTooManyInvites)
	case status >= 500:
		return errCode(CodeUnavailable)
	}
	return errCode(CodeInternal)
}
```

`lantern-core/helper/state.go`:

```go
package helper

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	stateFile = "state.bin"
	// stateAD binds the sealed state to its purpose.
	stateAD = "lantern-helper-state-v1"
)

// state is everything the engine keeps. It is sealed on disk with the key
// from the OS keystore, and never logged.
type state struct {
	V          int        `json:"v"`
	DeviceSeed []byte     `json:"device_seed,omitempty"`
	// MyName is only the last name used, to prefill the next prompt. Each
	// invite and claim keeps its own.
	MyName     string     `json:"my_name,omitempty"`
	Invites    []*invite  `json:"invites,omitempty"`
	Claims     []*claim   `json:"claims,omitempty"`
	Pairings   []*pairing `json:"pairings,omitempty"`
}

// invite is one this device created and has not seen decided. A cancelled
// invite is a tombstone: hidden from the UI, kept until it expires so that a
// late claim on it is rejected rather than left waiting.
type invite struct {
	ID            string    `json:"id"`
	Secret        []byte    `json:"secret"`
	Created       time.Time `json:"created"`
	State         string    `json:"state"` // "pending", "claimed" or "cancelled"
	// InviterName is the name this user gave for this invite, sealed for the
	// volunteer at approval. Kept per invite: two invites can carry two names.
	InviterName   string    `json:"inviter_name,omitempty"`
	VolunteerPub  []byte    `json:"volunteer_pub,omitempty"`
	VolunteerName string    `json:"volunteer_name,omitempty"`
	Code          string    `json:"code,omitempty"`
}

// claim is an invite this device claimed and has not yet seen approved.
type claim struct {
	ID      string    `json:"id"`
	Secret  []byte    `json:"secret"`
	Created time.Time `json:"created"`
	Code    string    `json:"code"`
	// Name is the name this user gave the inviter with this claim.
	Name string `json:"name,omitempty"`
}

// pairing is one this device is in. Role is this device's side.
type pairing struct {
	ID    string    `json:"id"`
	Role  string    `json:"role"`
	Name  string    `json:"name,omitempty"`
	Since time.Time `json:"since"`
}

const (
	RoleInviter   = "inviter"
	RoleVolunteer = "volunteer"
)

// load reads the sealed state. A missing file is an empty state. A file
// that does not open under the key means the keystore now returns another
// key (an OS reset, a restored backup): the file is kept as state.bin.bak,
// never silently replaced, the state starts empty, and the next view says
// keystore_reset so the UI can tell the user.
func (e *Engine) load() {
	e.st = state{V: 1}
	path := filepath.Join(e.dir, stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if st, ok := e.open(data); ok {
		e.st = st
		return
	}
	if err := os.Rename(path, path+".bak"); err == nil {
		e.reset = true
	}
}

func (e *Engine) open(data []byte) (state, bool) {
	aead, err := chacha20poly1305.New(e.key)
	if err != nil || len(data) < chacha20poly1305.NonceSize+aead.Overhead() {
		return state{}, false
	}
	pt, err := aead.Open(nil, data[:chacha20poly1305.NonceSize], data[chacha20poly1305.NonceSize:], []byte(stateAD))
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(pt, &st) != nil {
		return state{}, false
	}
	return st, true
}

// save seals the state and replaces the file atomically, owner-only.
func (e *Engine) save() error {
	pt, err := json.Marshal(e.st)
	if err != nil {
		return errCode(CodeInternal)
	}
	aead, err := chacha20poly1305.New(e.key)
	if err != nil {
		return errCode(CodeInternal)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(e.rand, nonce); err != nil {
		return errCode(CodeInternal)
	}
	sealed := aead.Seal(nonce, nonce, pt, []byte(stateAD))
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		return errCode(CodeInternal)
	}
	tmp := filepath.Join(e.dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return errCode(CodeInternal)
	}
	if err := os.Rename(tmp, filepath.Join(e.dir, stateFile)); err != nil {
		return errCode(CodeInternal)
	}
	return nil
}

// int64JSON reads an int64 written as a JSON number or, as protojson writes
// it, a string.
type int64JSON int64

func (n *int64JSON) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*n = int64JSON(v)
	return nil
}
```

`lantern-core/helper/engine.go`:

```go
// Package helper is the device side of helper invites in the Lantern app. It
// creates and claims invites, shows the matching code, approves or rejects
// claims, ends pairings, and keeps the names of the people this device helps
// or is helped by.
//
// The invite secrets, the device key and the names live only in this
// package's state file, sealed with a key from the OS keystore that the app
// passes to "unlock". Requests to lantern-cloud go through Relay, to radiance,
// and carry only hashes, public keys and encrypted names. Nothing here logs.
package helper

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// InviteTTL is how long lantern-cloud keeps an invite. Local invites and
// claims older than this are forgotten, since they can no longer be decided.
const InviteTTL = 48 * time.Hour

// Relay sends one HelperService request through radiance. It returns
// lantern-cloud's status, its JSON body on a 2xx and its error text otherwise.
// An error means the request never got an answer.
type Relay func(ctx context.Context, op string, body []byte) (status int, resp []byte, errText string, err error)

// Activity is radiance's helper.Activity: today's sharing time and bytes.
type Activity struct {
	Day           string `json:"day"`
	ActiveSeconds int64  `json:"active_seconds"`
	RelayedBytes  uint64 `json:"relayed_bytes"`
}

// ActivityFunc reads today's Activity from radiance.
type ActivityFunc func(ctx context.Context) (Activity, error)

// Engine runs helper-invite operations for the UI, one at a time.
type Engine struct {
	dir        string
	relay      Relay
	activityFn ActivityFunc
	now        func() time.Time
	rand       io.Reader

	mu       sync.Mutex
	key      []byte
	st       state
	online   map[string]bool
	activity *Activity
	// reset is set when unlock found a state file the key could not open.
	reset bool
}

// NewEngine returns an engine that keeps its state in dir.
func NewEngine(dir string, relay Relay, activity ActivityFunc) *Engine {
	return &Engine{dir: dir, relay: relay, activityFn: activity, now: time.Now, rand: rand.Reader,
		online: map[string]bool{}}
}

// Call runs op with JSON args and returns its JSON result. Its errors are
// *Error.
func (e *Engine) Call(ctx context.Context, op string, args []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	if op == "unlock" {
		return e.unlock(args)
	}
	if e.key == nil {
		return nil, errCode(CodeLocked)
	}
	var out any
	var err error
	switch op {
	case "view":
		out = e.view(false)
	case "refresh":
		out, err = e.refresh(ctx)
	case "create_invite":
		out, err = e.createInvite(ctx, args)
	case "decide":
		out, err = e.decide(ctx, args)
	case "forget_invite":
		out, err = e.forgetInvite(ctx, args)
	case "claim":
		out, err = e.claim(ctx, args)
	case "end_pairing":
		out, err = e.endPairing(ctx, args)
	case "end_all_helpers":
		out, err = e.endAllHelpers(ctx)
	default:
		return nil, errCode(CodeInternal)
	}
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, errCode(CodeInternal)
	}
	return b, nil
}

func (e *Engine) unlock(args []byte) ([]byte, error) {
	var a struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(args, &a) != nil {
		return nil, errCode(CodeInternal)
	}
	key, err := base64.StdEncoding.DecodeString(a.Key)
	if err != nil || len(key) != chacha20poly1305.KeySize {
		return nil, errCode(CodeInternal)
	}
	e.key = key
	e.reset = false
	e.load()
	v := e.view(false)
	v.KeystoreReset = e.reset
	return json.Marshal(v)
}

func (e *Engine) newID() string {
	b := make([]byte, 8)
	_, _ = io.ReadFull(e.rand, b)
	return hex.EncodeToString(b)
}

// post sends req to op and decodes a 2xx answer into resp.
func (e *Engine) post(ctx context.Context, op string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return errCode(CodeInternal)
	}
	status, data, errText, err := e.relay(ctx, op, body)
	if err != nil {
		return errCode(CodeUnavailable)
	}
	if status < 200 || status >= 300 {
		return statusError(op, status, errText)
	}
	if resp != nil && len(data) > 0 && json.Unmarshal(data, resp) != nil {
		return errCode(CodeUnavailable)
	}
	return nil
}

// View is what the UI shows.
type View struct {
	Unlocked   bool          `json:"unlocked"`
	MyName     string        `json:"my_name"`
	Invites    []InviteView  `json:"invites"`
	Claims     []ClaimView   `json:"claims"`
	Helpers    []PairingView `json:"helpers"`
	Helping    []PairingView `json:"helping"`
	Activity   *Activity     `json:"activity,omitempty"`
	NewHelping bool          `json:"new_helping"`
	// KeystoreReset is set only in unlock's answer, when the old state could
	// not be opened and was kept as state.bin.bak.
	KeystoreReset bool `json:"keystore_reset,omitempty"`
}

type InviteView struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Link          string `json:"link,omitempty"`
	ExpiresUnix   int64  `json:"expires_unix"`
	Code          string `json:"code,omitempty"`
	VolunteerName string `json:"volunteer_name,omitempty"`
}

type ClaimView struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	ExpiresUnix int64  `json:"expires_unix"`
}

type PairingView struct {
	PairingID string `json:"pairing_id"`
	Name      string `json:"name"`
	Online    bool   `json:"online,omitempty"`
	SinceUnix int64  `json:"since_unix"`
}
```

`lantern-core/helper/ops.go`:

```go
package helper

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/getlantern/broflake/common/helperinvite"
)

type createInviteReq struct {
	InviteHash     []byte `json:"invite_hash"`
	ApprovalPubkey []byte `json:"approval_pubkey"`
}

type claimReq struct {
	InviteHash      []byte `json:"invite_hash"`
	VolunteerPubkey []byte `json:"volunteer_pubkey"`
	VolunteerBlob   []byte `json:"volunteer_blob"`
}

type inviteRef struct {
	InviteHash []byte `json:"invite_hash"`
}

type inviteStatus struct {
	State           string `json:"state"`
	VolunteerPubkey []byte `json:"volunteer_pubkey"`
	VolunteerBlob   []byte `json:"volunteer_blob"`
	PairingID       string `json:"pairing_id"`
}

type decideReq struct {
	InviteHash  []byte `json:"invite_hash"`
	Approve     bool   `json:"approve"`
	ApprovalSig []byte `json:"approval_sig,omitempty"`
	InviterBlob []byte `json:"inviter_blob,omitempty"`
}

type pairingRef struct {
	PairingID string `json:"pairing_id"`
}

type listedPairing struct {
	PairingID   string    `json:"pairing_id"`
	Role        string    `json:"role"`
	InviterBlob []byte    `json:"inviter_blob"`
	Online      bool      `json:"online"`
	CreatedUnix int64JSON `json:"created_unix"`
}

const opRefreshTickets = "tickets/refresh"

func isCode(err error, code string) bool {
	var he *Error
	return errors.As(err, &he) && he.Code == code
}

func (e *Engine) invite(id string) *invite {
	for _, i := range e.st.Invites {
		if i.ID == id {
			return i
		}
	}
	return nil
}

func (e *Engine) pairing(id string) *pairing {
	for _, p := range e.st.Pairings {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (e *Engine) dropInvite(id string) {
	e.st.Invites = slices.DeleteFunc(e.st.Invites, func(i *invite) bool { return i.ID == id })
}

func (e *Engine) dropClaim(id string) {
	e.st.Claims = slices.DeleteFunc(e.st.Claims, func(c *claim) bool { return c.ID == id })
}

func (e *Engine) dropPairing(id string) {
	e.st.Pairings = slices.DeleteFunc(e.st.Pairings, func(p *pairing) bool { return p.ID == id })
}

func (e *Engine) addPairing(id, role, name string, since time.Time) {
	if id == "" || e.pairing(id) != nil {
		return
	}
	e.st.Pairings = append(e.st.Pairings, &pairing{ID: id, Role: role, Name: name, Since: since})
}

func (e *Engine) prune() bool {
	cutoff := e.now().Add(-InviteTTL)
	ni, nc := len(e.st.Invites), len(e.st.Claims)
	e.st.Invites = slices.DeleteFunc(e.st.Invites, func(i *invite) bool { return i.Created.Before(cutoff) })
	e.st.Claims = slices.DeleteFunc(e.st.Claims, func(c *claim) bool { return c.Created.Before(cutoff) })
	return ni != len(e.st.Invites) || nc != len(e.st.Claims)
}

func (e *Engine) view(newHelping bool) View {
	v := View{Unlocked: true, MyName: e.st.MyName, NewHelping: newHelping, Activity: e.activity,
		Invites: []InviteView{}, Claims: []ClaimView{}, Helpers: []PairingView{}, Helping: []PairingView{}}
	for _, i := range e.st.Invites {
		if i.State == "cancelled" {
			continue
		}
		iv := InviteView{ID: i.ID, State: i.State, ExpiresUnix: i.Created.Add(InviteTTL).Unix(),
			Code: i.Code, VolunteerName: i.VolunteerName}
		if i.State == "pending" {
			iv.Link = helperinvite.Link(i.Secret)
		}
		v.Invites = append(v.Invites, iv)
	}
	for _, c := range e.st.Claims {
		v.Claims = append(v.Claims, ClaimView{ID: c.ID, Code: c.Code, ExpiresUnix: c.Created.Add(InviteTTL).Unix()})
	}
	for _, p := range e.st.Pairings {
		pv := PairingView{PairingID: p.ID, Name: p.Name, SinceUnix: p.Since.Unix()}
		if p.Role == RoleInviter {
			pv.Online = e.online[p.ID]
			v.Helpers = append(v.Helpers, pv)
		} else {
			v.Helping = append(v.Helping, pv)
		}
	}
	return v
}

func decodeArgs(args []byte, v any) error {
	if json.Unmarshal(args, v) != nil {
		return errCode(CodeInternal)
	}
	return nil
}

func (e *Engine) createInvite(ctx context.Context, args []byte) (any, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	name, err := helperinvite.CleanName(a.Name)
	if err != nil {
		return nil, errCode(CodeBadName)
	}
	s, err := helperinvite.NewSecret(e.rand)
	if err != nil {
		return nil, errCode(CodeInternal)
	}
	if err := e.post(ctx, "invites", createInviteReq{InviteHash: helperinvite.InviteHash(s),
		ApprovalPubkey: helperinvite.ApprovalPublic(s)}, nil); err != nil {
		return nil, err
	}
	inv := &invite{ID: e.newID(), Secret: s, Created: e.now(), State: "pending", InviterName: name}
	e.st.Invites = append(e.st.Invites, inv)
	e.st.MyName = name
	if err := e.save(); err != nil {
		return nil, err
	}
	return map[string]string{"invite_id": inv.ID, "link": helperinvite.Link(s)}, nil
}

func (e *Engine) refresh(ctx context.Context) (any, error) {
	changed := e.prune()
	for _, inv := range slices.Clone(e.st.Invites) {
		var st inviteStatus
		err := e.post(ctx, "invites/get", inviteRef{InviteHash: helperinvite.InviteHash(inv.Secret)}, &st)
		if isCode(err, CodeNotFound) {
			e.dropInvite(inv.ID) // expired and swept
			changed = true
			continue
		}
		if err != nil {
			if changed {
				_ = e.save()
			}
			return nil, err
		}
		if inv.State == "cancelled" {
			// A tombstone: reject any claim, so nobody waits on an approval
			// that cannot come, and forget it once it can no longer change.
			switch st.State {
			case "claimed":
				if err := e.post(ctx, "invites/decide", decideReq{InviteHash: helperinvite.InviteHash(inv.Secret)}, nil); err == nil ||
					isCode(err, CodeNoPendingClaim) {
					e.dropInvite(inv.ID)
				}
				changed = true
			case "approved", "rejected":
				e.dropInvite(inv.ID)
				changed = true
			}
			continue
		}
		switch st.State {
		case "claimed":
			if inv.State != "claimed" || !bytes.Equal(inv.VolunteerPub, st.VolunteerPubkey) {
				inv.State = "claimed"
				inv.VolunteerPub = st.VolunteerPubkey
				inv.VolunteerName, _ = helperinvite.OpenName(helperinvite.VolunteerNameKey(inv.Secret), st.VolunteerBlob)
				inv.Code = helperinvite.MatchingCode(helperinvite.DeriveKey(inv.Secret), st.VolunteerPubkey)
				changed = true
			}
		case "approved":
			// Approved here or by an approve whose answer was lost.
			e.addPairing(st.PairingID, RoleInviter, inv.VolunteerName, e.now())
			e.dropInvite(inv.ID)
			changed = true
		case "rejected":
			e.dropInvite(inv.ID)
			changed = true
		}
	}
	var list struct {
		Pairings []listedPairing `json:"pairings"`
	}
	err := e.post(ctx, "pairings", struct{}{}, &list)
	if isCode(err, CodeDisabled) && len(e.st.Invites) == 0 {
		// A volunteer abroad has no helper_invites flag. lantern-cloud lets it
		// list pairings only while it holds a live claim or a pairing (Task 1),
		// so once its claim is rejected or expires, or its last pairing ends,
		// the answer is the gate's 404. A device with no invites of its own is
		// no inviter, so that answer means it holds nothing: an empty list.
		// (An inviter whose flag is turned off also sees an empty list.)
		err = nil
		list.Pairings = nil
		if len(e.st.Claims) > 0 {
			e.st.Claims = nil
			changed = true
		}
	}
	if err != nil {
		if changed {
			_ = e.save()
		}
		return nil, err
	}
	merged, newHelping := e.mergePairings(list.Pairings)
	changed = changed || merged
	if merged {
		// New or ended pairings: ticket sources fetch at their next read,
		// so a new pairing's rooms work within seconds.
		_ = e.post(ctx, opRefreshTickets, struct{}{}, nil)
	}
	if e.activityFn != nil {
		if a, err := e.activityFn(ctx); err == nil {
			e.activity = &a
		}
	}
	if changed {
		if err := e.save(); err != nil {
			return nil, err
		}
	}
	return e.view(newHelping), nil
}

// mergePairings makes the local pairings match lantern-cloud's list. A new
// pairing this device volunteers for takes its inviter's name from the
// pending claim whose key opens the blob, and that claim is done.
func (e *Engine) mergePairings(listed []listedPairing) (changed, newHelping bool) {
	seen := map[string]bool{}
	e.online = map[string]bool{}
	for _, lp := range listed {
		seen[lp.PairingID] = true
		if lp.Role == RoleInviter {
			e.online[lp.PairingID] = lp.Online
		}
		if e.pairing(lp.PairingID) != nil {
			continue
		}
		name := ""
		if lp.Role == RoleVolunteer {
			newHelping = true
			name = e.claimName(lp.InviterBlob)
		}
		e.addPairing(lp.PairingID, lp.Role, name, time.Unix(int64(lp.CreatedUnix), 0))
		changed = true
	}
	n := len(e.st.Pairings)
	e.st.Pairings = slices.DeleteFunc(e.st.Pairings, func(p *pairing) bool { return !seen[p.ID] })
	return changed || n != len(e.st.Pairings), newHelping
}

func (e *Engine) claimName(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	for _, c := range e.st.Claims {
		if name, err := helperinvite.OpenName(helperinvite.InviterNameKey(c.Secret), blob); err == nil {
			e.dropClaim(c.ID)
			return name
		}
	}
	return ""
}

func (e *Engine) decide(ctx context.Context, args []byte) (any, error) {
	var a struct {
		InviteID string `json:"invite_id"`
		Approve  bool   `json:"approve"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	inv := e.invite(a.InviteID)
	if inv == nil || inv.State != "claimed" {
		return nil, errCode(CodeNoPendingClaim)
	}
	hash := helperinvite.InviteHash(inv.Secret)
	if !a.Approve {
		if err := e.post(ctx, "invites/decide", decideReq{InviteHash: hash}, nil); err != nil {
			return nil, err
		}
		e.dropInvite(inv.ID)
	} else {
		name := inv.InviterName
		if name == "" {
			name = e.st.MyName // an invite saved before names were kept per invite
		}
		appr, err := helperinvite.NewApproval(inv.Secret, inv.VolunteerPub, name, e.rand)
		if err != nil {
			return nil, errCode(CodeBadName)
		}
		var resp struct {
			PairingID string `json:"pairing_id"`
		}
		if err := e.post(ctx, "invites/decide", decideReq{InviteHash: hash, Approve: true,
			ApprovalSig: appr.Signature, InviterBlob: appr.InviterBlob}, &resp); err != nil {
			return nil, err
		}
		e.addPairing(resp.PairingID, RoleInviter, inv.VolunteerName, e.now())
		e.dropInvite(inv.ID)
	}
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.view(false), nil
}

func (e *Engine) forgetInvite(ctx context.Context, args []byte) (any, error) {
	var a struct {
		InviteID string `json:"invite_id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	inv := e.invite(a.InviteID)
	if inv == nil {
		return e.view(false), nil
	}
	if err := e.cancelInvite(ctx, inv); err != nil {
		return nil, err
	}
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.view(false), nil
}

// cancelInvite rejects a claimed invite and forgets it. An unclaimed one
// becomes a tombstone (there is no RPC to delete it), which refresh uses to
// reject any later claim until it expires.
func (e *Engine) cancelInvite(ctx context.Context, inv *invite) error {
	if inv.State == "claimed" {
		err := e.post(ctx, "invites/decide", decideReq{InviteHash: helperinvite.InviteHash(inv.Secret)}, nil)
		if err != nil && !isCode(err, CodeNoPendingClaim) && !isCode(err, CodeNotFound) {
			return err
		}
		e.dropInvite(inv.ID)
		return nil
	}
	inv.State = "cancelled"
	inv.InviterName = ""
	return nil
}

func (e *Engine) claim(ctx context.Context, args []byte) (any, error) {
	var a struct {
		Link string `json:"link"`
		Name string `json:"name"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	s, err := helperinvite.ParseLink(a.Link)
	if err != nil {
		return nil, errCode(CodeBadLink)
	}
	for _, inv := range e.st.Invites {
		if bytes.Equal(inv.Secret, s) {
			return nil, errCode(CodeOwnInvite)
		}
	}
	for _, c := range e.st.Claims {
		if bytes.Equal(c.Secret, s) {
			return map[string]string{"claim_id": c.ID, "code": c.Code}, nil
		}
	}
	if len(e.st.DeviceSeed) == 0 {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := io.ReadFull(e.rand, seed); err != nil {
			return nil, errCode(CodeInternal)
		}
		e.st.DeviceSeed = seed
	}
	c, err := helperinvite.NewClaim(s, e.st.DeviceSeed, a.Name, e.rand)
	if errors.Is(err, helperinvite.ErrName) {
		return nil, errCode(CodeBadName)
	}
	if err != nil {
		return nil, errCode(CodeInternal)
	}
	if err := e.post(ctx, "invites/claim", claimReq{InviteHash: c.InviteHash, VolunteerPubkey: c.VolunteerPubkey,
		VolunteerBlob: c.VolunteerBlob}, nil); err != nil {
		return nil, err
	}
	name, _ := helperinvite.CleanName(a.Name)
	cl := &claim{ID: e.newID(), Secret: s, Created: e.now(), Code: c.Code, Name: name}
	e.st.Claims = append(e.st.Claims, cl)
	e.st.MyName = name
	if err := e.save(); err != nil {
		return nil, err
	}
	return map[string]string{"claim_id": cl.ID, "code": cl.Code}, nil
}

func (e *Engine) endPairing(ctx context.Context, args []byte) (any, error) {
	var a struct {
		PairingID string `json:"pairing_id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	p := e.pairing(a.PairingID)
	if p == nil {
		return nil, errCode(CodeNotFound)
	}
	if err := e.post(ctx, "pairings/end", pairingRef{PairingID: p.ID}, nil); err != nil && !isCode(err, CodeNotFound) {
		return nil, err
	}
	e.dropPairing(p.ID)
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.view(false), nil
}

// endAllHelpers is "remove all helpers": it ends every pairing in which this
// device is helped, rejects claimed invites, and turns unclaimed ones into
// tombstones without names, so a seized phone holds no helper names. It saves after each step, so a failure
// part way keeps what was done.
func (e *Engine) endAllHelpers(ctx context.Context) (any, error) {
	for _, p := range slices.Clone(e.st.Pairings) {
		if p.Role != RoleInviter {
			continue
		}
		if err := e.post(ctx, "pairings/end", pairingRef{PairingID: p.ID}, nil); err != nil && !isCode(err, CodeNotFound) {
			_ = e.save()
			return nil, err
		}
		e.dropPairing(p.ID)
	}
	for _, inv := range slices.Clone(e.st.Invites) {
		if err := e.cancelInvite(ctx, inv); err != nil {
			_ = e.save()
			return nil, err
		}
	}
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.view(false), nil
}
```

- [ ] **Step 5: Run the tests**

```bash
go mod tidy && git diff go.mod
go test -race ./lantern-core/helper/ -v
```

Expected:
- `golang.org/x/crypto` becomes a direct requirement.
- All engine tests pass.

- [ ] **Step 6: Telemetry audit and commit**

Run: `grep -rnE 'slog\.|log\.|fmt\.Print' lantern-core/helper/ --include='*.go' | grep -v _test.go`
Expected: no output.

```bash
git add lantern-core/helper go.mod go.sum
git commit -m "lantern-core: helper-invite engine with sealed state"
```

---

### Task 10: lantern — the `helperCall` bridge, from Dart to the engine

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Modify: `go.mod`, `go.sum` (radiance bumped to `RAD_SHA`)
- Modify: `lantern-core/core.go` (a `Helper` interface in `Core`, a `helpers` field, `initialize`, `HelperCall`)
- Create: `lantern-core/helper_call_test.go`
- Modify: `lantern-core/ffi/ffi.go` (`//export helperCall`)
- Modify: `lantern-core/mobile/mobile.go` (`HelperCall`)
- Modify: `android/app/src/main/kotlin/org/getlantern/lantern/handler/MethodHandler.kt`
- Modify: `ios/Runner/Handlers/MethodHandler.swift`, `macos/Runner/Handlers/MethodHandler.swift`
- Modify: `lib/lantern/lantern_core_service.dart`, `lantern_ffi_service.dart`, `lantern_platform_service.dart`, `lantern_service.dart`
- Regenerate: `lib/lantern/lantern_generated_bindings.dart` (`make macos-ffi-headers && make ffigen`)

**Interfaces:**
- Consumes:
  - Task 9's `helper.NewEngine`, `(*Engine).Call` and `helper.ErrorJSON`
  - Task 6's `(*ipc.Client).HelperRelay`
  - Task 8's `(*ipc.Client).HelperActivity`
- Produces:
  - Go: `type Helper interface { HelperCall(op, argsJSON string) string }`, part of `Core`
  - `func (*LanternCore) HelperCall(op, argsJSON string) string`, which never returns a raw error: a failure is `{"error": code}`
  - FFI: `char* helperCall(char* op, char* args)`
  - gomobile: `func HelperCall(op, argsJSON string) (string, error)`, which Swift sees as `MobileHelperCall(op, args, &error)` and Kotlin as `Mobile.helperCall(op, args)`
  - The method channel `helperCall` with arguments `{"op": String, "args": String}`
  - Dart: `Future<Either<Failure, String>> helperCall(String op, Map<String, dynamic> args)` on `LanternCoreService` and `LanternService`. It returns the raw JSON, including the `{"error":…}` envelope, which Task 12 parses.

- [ ] **Step 1: Bump radiance**

```bash
go get github.com/getlantern/radiance@$RAD_SHA
go mod tidy
git diff go.mod
```

Expected: radiance's pseudo-version changes. broflake may move to a newer pseudo-version that radiance requires, which is fine. Nothing else changes unexpectedly.

- [ ] **Step 2: Write the failing Go test**

`lantern-core/helper_call_test.go`:

```go
package lanterncore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern/lantern-core/helper"
)

func TestHelperCallAnswersJSONAndNeverARawError(t *testing.T) {
	relay := func(context.Context, string, []byte) (int, []byte, string, error) {
		return 200, []byte(`{"pairings":[]}`), "", nil
	}
	lc := &LanternCore{ctx: context.Background(), helpers: helper.NewEngine(t.TempDir(), relay, nil)}

	assert.JSONEq(t, `{"error":"locked"}`, lc.HelperCall("view", "{}"))
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(lc.HelperCall("unlock", `{"key":"`+key+`"}`)), &v))
	assert.Equal(t, true, v["unlocked"])
	require.NoError(t, json.Unmarshal([]byte(lc.HelperCall("refresh", "")), &v))
	assert.Equal(t, true, v["unlocked"])
	assert.JSONEq(t, `{"error":"internal"}`, lc.HelperCall("no-such-op", "{}"))
}
```

Run: `go test ./lantern-core/ -run TestHelperCall -v`
Expected: the build fails with `unknown field helpers`.

- [ ] **Step 3: Wire the core**

In `lantern-core/core.go`:

Add the import `"github.com/getlantern/lantern/lantern-core/helper"`.

Add to `LanternCore`:

```go
	helpers      *helper.Engine
```

Add the interface and include it in `Core`:

```go
type Helper interface {
	// HelperCall runs one helper-invite operation (see lantern-core/helper)
	// and returns its JSON result, or {"error": code}. It never returns a raw
	// error, so nothing about a failure but its code crosses into Dart.
	HelperCall(op, argsJSON string) string
}
```

In `type Core interface`, add `Helper` after `PeerShare`.

In `initialize`, after `lc.eventEmitter = eventEmitter`, add:

```go
	lc.helpers = helper.NewEngine(filepath.Join(opts.DataDir, "helper"), lc.relayHelper, lc.helperActivity)
```

Add the methods:

```go
// helperCallTimeout bounds one helper operation. A refresh makes one request
// per pending invite, up to 20, plus one, each relayed through kindling.
const helperCallTimeout = 90 * time.Second

func (lc *LanternCore) HelperCall(op, argsJSON string) string {
	if lc.helpers == nil {
		return helper.ErrorJSON(&helper.Error{Code: helper.CodeUnavailable})
	}
	ctx, cancel := context.WithTimeout(lc.ctx, helperCallTimeout)
	defer cancel()
	out, err := lc.helpers.Call(ctx, op, []byte(argsJSON))
	if err != nil {
		return helper.ErrorJSON(err)
	}
	return string(out)
}

// relayHelper sends one HelperService request through radiance.
func (lc *LanternCore) relayHelper(ctx context.Context, op string, body []byte) (int, []byte, string, error) {
	res, err := lc.client.HelperRelay(ctx, op, body)
	if err != nil {
		return 0, nil, "", err
	}
	return res.Status, res.Body, res.Error, nil
}

// helperActivity reads today's sharing totals from radiance.
func (lc *LanternCore) helperActivity(ctx context.Context) (helper.Activity, error) {
	a, err := lc.client.HelperActivity(ctx)
	if err != nil {
		return helper.Activity{}, err
	}
	return helper.Activity{Day: a.Day, ActiveSeconds: a.ActiveSeconds, RelayedBytes: a.RelayedBytes}, nil
}
```

- [ ] **Step 4: Exports**

In `lantern-core/ffi/ffi.go`, after `getPeerStatusJSON`, add:

```go
// helperCall runs one helper-invite operation and returns its JSON result or
// {"error": code}. Its arguments carry invite links and names: never log them.
//
//export helperCall
func helperCall(_op, _args *C.char) *C.char {
	op, args := C.GoString(_op), C.GoString(_args)
	return runOnGoStack(func() *C.char {
		c, errStr := requireCore()
		if errStr != nil {
			return errStr
		}
		return C.CString(c.HelperCall(op, args))
	})
}
```

In `lantern-core/mobile/mobile.go`, after `GetPeerStatus`, add:

```go
// HelperCall runs one helper-invite operation and returns its JSON result or
// {"error": code}. The error return is only "radiance not initialized".
func HelperCall(op, argsJSON string) (string, error) {
	return withCoreR(func(c lanterncore.Core) (string, error) {
		return c.HelperCall(op, argsJSON), nil
	})
}
```

- [ ] **Step 5: Platform handlers**

`MethodHandler.kt`: in the `Methods` enum, after `ProbeUPnP("probeUPnP"),`, add `HelperCall("helperCall"),`. In the `when`, after the `Methods.ProbeUPnP.method` branch, add:

```kotlin
            // One helper-invite operation (lantern-core/helper). The result is
            // JSON, including {"error": code} on failure. Never log the
            // arguments: they carry invite links and names.
            Methods.HelperCall.method -> {
                scope.handleValue(result, "helper_call") {
                    val op = call.argument<String>("op") ?: error("Missing op")
                    val args = call.argument<String>("args") ?: "{}"
                    Mobile.helperCall(op, args)
                }
            }
```

`ios/Runner/Handlers/MethodHandler.swift` and `macos/Runner/Handlers/MethodHandler.swift`: after the `case "probeUPnP":` block, add the same case to each:

```swift
      // One helper-invite operation (lantern-core/helper). Network-bound,
      // so it runs off the main actor. Never log the arguments.
      case "helperCall":
        let data = call.arguments as? [String: Any] ?? [:]
        let op = data["op"] as? String ?? ""
        let args = data["args"] as? String ?? "{}"
        Task.detached {
          var error: NSError?
          let json = MobileHelperCall(op, args, &error)
          await MainActor.run {
            if let error {
              result(FlutterError(code: "HELPER_CALL_FAILED", message: error.localizedDescription, details: nil))
            } else {
              result(json)
            }
          }
        }
```

- [ ] **Step 6: Dart services**

`lib/lantern/lantern_core_service.dart`, after `getPeerStatusJSON`:

```dart
  /// Runs one helper-invite operation in the Go core (lantern-core/helper)
  /// and returns its JSON result, which is `{"error": code}` on failure.
  /// [args] carries invite links and names: never log it.
  Future<Either<Failure, String>> helperCall(
    String op,
    Map<String, dynamic> args,
  );
```

`lib/lantern/lantern_ffi_service.dart`:

```dart
  @override
  Future<Either<Failure, String>> helperCall(
    String op,
    Map<String, dynamic> args,
  ) async {
    try {
      final payload = jsonEncode(args);
      final result = await runInBackground<String>(() async {
        final opPtr = op.toNativeUtf8();
        final argsPtr = payload.toNativeUtf8();
        try {
          final resultPtr = _ffiService.helperCall(
            opPtr.cast<Char>(),
            argsPtr.cast<Char>(),
          );
          try {
            return resultPtr.cast<Utf8>().toDartString();
          } finally {
            _ffiService.freeCString(resultPtr);
          }
        } finally {
          malloc.free(opPtr);
          malloc.free(argsPtr);
        }
      });
      return right(result);
    } catch (e, st) {
      appLogger.error('helperCall failed', e, st);
      return Left(e.toFailure());
    }
  }
```

`lib/lantern/lantern_platform_service.dart`:

```dart
  @override
  Future<Either<Failure, String>> helperCall(
    String op,
    Map<String, dynamic> args,
  ) async {
    try {
      final result = await _methodChannel.invokeMethod<String>('helperCall', {
        'op': op,
        'args': jsonEncode(args),
      });
      return right(result ?? '');
    } catch (e, st) {
      appLogger.error('helperCall failed', e, st);
      return Left(e.toFailure());
    }
  }
```

`lib/lantern/lantern_service.dart`:

```dart
  @override
  Future<Either<Failure, String>> helperCall(
    String op,
    Map<String, dynamic> args,
  ) {
    if (PlatformUtils.isFFISupported) {
      return _ffiService.helperCall(op, args);
    }
    return _platformService.helperCall(op, args);
  }
```

Neither error log includes `args` or the result.

- [ ] **Step 7: Regenerate the bindings and run everything**

```bash
make macos-ffi-headers && make ffigen
git diff --stat lib/lantern/lantern_generated_bindings.dart
CGO_ENABLED=1 go test -tags with_gvisor,with_quic,with_wireguard,with_utls,with_grpc ./lantern-core/...
make pubget && make gen && flutter analyze
make macos-unit-tests && make ios-compile-check
cd android && ./gradlew :app:compileDebugKotlin && cd ..
```

Expected:
- The bindings gain `helperCall`.
- The Go tests pass, including `TestHelperCallAnswersJSONAndNeverARawError`.
- `flutter analyze` has no new issues.
- The Swift and Kotlin builds compile.

If `android/gradlew` is missing, run `make install-android-deps && make android-debug-ci` first. Only `gradle-wrapper.properties` is tracked.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum lantern-core/core.go lantern-core/helper_call_test.go lantern-core/ffi/ffi.go lantern-core/mobile/mobile.go \
  android/app/src/main/kotlin/org/getlantern/lantern/handler/MethodHandler.kt \
  ios/Runner/Handlers/MethodHandler.swift macos/Runner/Handlers/MethodHandler.swift \
  lib/lantern/lantern_core_service.dart lib/lantern/lantern_ffi_service.dart lib/lantern/lantern_platform_service.dart \
  lib/lantern/lantern_service.dart lib/lantern/lantern_generated_bindings.dart
git commit -m "app: helperCall bridge from Dart to the helper-invite engine"
```

---

### Task 11: lantern — keep `helper-` tags out of the UI and logs

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Create: `lib/core/utils/helper_tags.dart`
- Modify: `lib/core/models/available_servers.dart` (`lanternServers`)
- Modify: `lib/core/services/logger_service.dart` (`FileLogPrinter.onLog`, `DebugPrintLoggyPrinter.onLog`)
- Create: `test/core/models/available_servers_helper_test.dart`
- Create: `lantern-core/helper_redaction_test.go`

**Interfaces:**
- Consumes: radiance's log redaction from plan 3 (radiance `log/redact.go`, which wraps every slog sink), now reached through the `RAD_SHA` bump in Task 10.
- Produces:
  - `bool isHelperTag(String tag)`
  - `String redactHelperTags(String text)`, a port of radiance's `internal/redact.Helper`. It turns `helper-[0-9a-f]{12}` into `helper`, and replaces every IP address, with any port, on a line naming a helper outbound with `<redacted>`
  - The server list, the location picker and the tray never show a helper outbound. Auto-selection may still use one.

**Why:** the spec says "Clients must not export `helper-` tags in telemetry or reports". lantern-cloud gives each helper outbound a location (`outboundLocations[h.outbound.Tag]`), so radiance lists it as a server. The app would show the volunteer's city as a selectable server and write its tag to `flutter.log`, which the Logs screen shares unredacted.

- [ ] **Step 1: Write the failing tests**

`test/core/models/available_servers_helper_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:lantern/core/models/available_servers.dart';
import 'package:lantern/core/utils/helper_tags.dart';

Map<String, dynamic> _server(String tag) => {
  'tag': tag,
  'type': 'samizdat',
  'isLantern': true,
  'location': {'country': 'Germany', 'city': 'Berlin', 'country_code': 'DE'},
};

void main() {
  test('helper outbounds are never listed as Lantern servers', () {
    final servers = AvailableServers.fromJson([
      _server('lantern-1'),
      _server('helper-0123456789ab'),
    ]);
    expect(servers.lanternServers.map((s) => s.tag), ['lantern-1']);
    expect(
      servers.lanternServerLocations.map((s) => s.tag),
      isNot(contains('helper-0123456789ab')),
    );
  });

  test('redactHelperTags replaces every helper tag', () {
    expect(
      redactHelperTags('outbound/samizdat[helper-0123456789ab] and helper-ffffffffffff'),
      'outbound/samizdat[helper] and helper',
    );
    expect(redactHelperTags('helper-short stays'), 'helper-short stays');
    expect(isHelperTag('helper-0123456789ab'), isTrue);
    expect(isHelperTag('lantern-1'), isFalse);
  });

  // The same cases as radiance's internal/redact tests.
  const tag = 'helper-0123456789ab';
  final cases = {
    'outbound/samizdat[$tag]: dial tcp 203.0.113.7:443: i/o timeout':
        'outbound/samizdat[helper]: dial tcp <redacted>: i/o timeout',
    'outbound/samizdat[$tag]: no route to 203.0.113.7.':
        'outbound/samizdat[helper]: no route to <redacted>.',
    'outbound/samizdat[$tag]: dial tcp [2001:db8::7]:8443: refused':
        'outbound/samizdat[helper]: dial tcp <redacted>: refused',
    'outbound/samizdat[$tag]: unreachable 2001:db8::7':
        'outbound/samizdat[helper]: unreachable <redacted>',
    'outbound/samizdat[helper]: dial tcp 203.0.113.7:443':
        'outbound/samizdat[helper]: dial tcp <redacted>',
    'time="2026-10-23 13:30:00.000 UTC" msg="outbound/samizdat[$tag]: dial tcp 203.0.113.7:443"':
        'time="2026-10-23 13:30:00.000 UTC" msg="outbound/samizdat[helper]: dial tcp <redacted>"',
  };
  cases.forEach((input, want) {
    test('redacts the address on a helper line: $want', () {
      expect(redactHelperTags(input), want);
    });
  });

  test('lines that name no helper keep their addresses', () {
    const input = 'outbound/samizdat[lantern-1]: dial tcp 198.51.100.9:443\n'
        'outbound/samizdat[$tag]: dial tcp 203.0.113.7:443\n'
        'dial tcp [2001:db8::9]:443 ok\n';
    const want = 'outbound/samizdat[lantern-1]: dial tcp 198.51.100.9:443\n'
        'outbound/samizdat[helper]: dial tcp <redacted>\n'
        'dial tcp [2001:db8::9]:443 ok\n';
    expect(redactHelperTags(input), want);
    expect(redactHelperTags('nothing to see 198.51.100.9'), 'nothing to see 198.51.100.9');
  });
}
```

The fixture uses the keys `Server.fromJson` reads: `tag`, `type`, `isLantern` and `location`, with `country`, `city` and `country_code` inside it.

`lantern-core/helper_redaction_test.go`:

```go
package lanterncore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rlog "github.com/getlantern/radiance/log"
)

// lantern-core logs through radiance's logger on every platform (common.Init,
// or setupAppLogging on Apple), and that logger redacts helper- tags. This
// guards the dependency: a radiance bump that dropped the redaction would
// put helper tags in issue reports.
func TestAppLogsRedactHelperTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), AppLogFileName)
	logger := rlog.NewLogger(rlog.Config{LogPath: path, Level: "info", Prod: true, DisablePublisher: true})
	logger.Info("Auto location server:", "server", `{"tag":"helper-0123456789ab"}`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "helper-0123456789ab")
	assert.Contains(t, string(data), "helper")
}
```

Run: `flutter test test/core/models/available_servers_helper_test.dart && go test ./lantern-core/ -run TestAppLogsRedactHelperTags -v`
Expected: the Dart test fails to compile (`helper_tags.dart` does not exist). The Go test passes once Task 10 has bumped radiance past plan 3's redaction. If it fails, the radiance pin predates that redaction: bump radiance to a commit that includes it before going on.

- [ ] **Step 2: Implement**

`lib/core/utils/helper_tags.dart`:

```dart
import 'dart:io' show InternetAddress;

/// Helper outbound tags are client-side pseudonyms for the user's helper
/// pairings, and a line naming a helper outbound may carry the helper's
/// address. The spec forbids exporting either, so the app never shows helper
/// outbounds as servers and redacts both from its logs. This is a port of
/// radiance's internal/redact.Helper and must stay in step with it.
const redactedAddress = '<redacted>';

final _helperTag = RegExp(r'\bhelper-[0-9a-f]{12}\b');

// A helper outbound named in a line: its tag, or the "[helper]" an earlier
// redaction left of "outbound/<type>[helper-…]".
final _helperNamed = RegExp(r'\bhelper-[0-9a-f]{12}\b|\[helper\]');

// Text that may be an IP address, with or without a port: a bracketed host,
// or a run of hex digits, dots and colons. Each match is checked before it is
// replaced.
final _ipCandidate = RegExp(
  r'\[[0-9A-Fa-f:.]+(?:%[0-9A-Za-z_.\-]+)?\](?::[0-9]{1,5})?|[0-9A-Fa-f]*[.:][0-9A-Fa-f.:%]*',
);

bool isHelperTag(String tag) => tag.startsWith('helper-');

/// Replaces each helper tag with "helper" and, on every line that names a
/// helper outbound, every IP address (with any port) with "<redacted>".
/// Lines that name no helper outbound keep their addresses.
String redactHelperTags(String text) {
  if (!text.contains('helper')) return text;
  if (_helperNamed.hasMatch(text)) {
    text = text.splitMapJoin(
      '\n',
      onNonMatch: (line) => _helperNamed.hasMatch(line)
          ? line.replaceAllMapped(_ipCandidate, (m) => _redactIp(m[0]!))
          : line,
    );
  }
  return text.replaceAll(_helperTag, 'helper');
}

String _redactIp(String m) {
  final core = m.replaceFirst(RegExp(r'[.:]+$'), '');
  return _isIpAddr(core) ? '$redactedAddress${m.substring(core.length)}' : m;
}

bool _isIpAddr(String s) {
  if (s.startsWith('[')) {
    final end = s.indexOf(']');
    return end >= 0 && _parseIp(s.substring(1, end));
  }
  if (_parseIp(s)) return true;
  // host:port, as Go's net.SplitHostPort reads it: an IPv6 host would be
  // bracketed, so the host here has no colon.
  final i = s.lastIndexOf(':');
  if (i <= 0) return false;
  final host = s.substring(0, i);
  final port = s.substring(i + 1);
  return !host.contains(':') && port.isNotEmpty && int.tryParse(port) != null && _parseIp(host);
}

bool _parseIp(String s) {
  final zone = s.indexOf('%');
  return InternetAddress.tryParse(zone >= 0 ? s.substring(0, zone) : s) != null;
}
```

In `lib/core/models/available_servers.dart`, add `import 'package:lantern/core/utils/helper_tags.dart';` and replace `lanternServers` with:

```dart
  /// Lantern servers the user can see and pick. Helper outbounds are left out:
  /// auto-selection may use them, but they are never listed.
  List<Server> get lanternServers =>
      servers.where((s) => s.isLantern && !isHelperTag(s.tag)).toList();
```

In `lib/core/services/logger_service.dart`, add the import and, in `FileLogPrinter.onLog`, replace `final line = buffer.toString();` with `final line = redactHelperTags(buffer.toString());`. In `DebugPrintLoggyPrinter.onLog`, replace `'[${record.level.name}] ${record.loggerName}: ${record.message}'` with `'[${record.level.name}] ${record.loggerName}: ${redactHelperTags(record.message)}'`.

- [ ] **Step 3: Run the tests**

Run: `flutter test test/core/models/ test/core/services/logger_service_test.dart && go test ./lantern-core/ -run TestAppLogsRedactHelperTags -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/core/utils/helper_tags.dart lib/core/models/available_servers.dart lib/core/services/logger_service.dart \
  test/core/models/available_servers_helper_test.dart lantern-core/helper_redaction_test.go
git commit -m "app: never list or log helper- outbound tags"
```

---

### Task 12: lantern — helper state, the keystore and the gate

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Modify: `pubspec.yaml`, `pubspec.lock` (`flutter_secure_storage`)
- Modify: `linux/packaging/nfpm.yaml`, `.github/workflows/build-linux.yml`, `Dockerfile` (libsecret)
- Modify: `lib/core/models/feature_flags.dart`, `lib/core/common/app_build_info.dart`
- Create: `lib/features/helpers/models/helpers_view.dart`
- Create: `lib/features/helpers/helper_key_store.dart`
- Create: `lib/features/helpers/helper_errors.dart`
- Create: `lib/features/helpers/provider/helpers_notifier.dart` (and the generated `helpers_notifier.g.dart`)
- Modify: `lib/core/services/injection_container.dart`
- Modify: `assets/locales/en.po` (error strings)
- Create: `test/features/helpers/helpers_notifier_test.dart`, `test/features/helpers/helpers_view_test.dart`, `test/features/helpers/helper_key_store_test.dart`

**Interfaces:**
- Consumes:
  - Task 10's `LanternService.helperCall(op, args)`
  - `getSettings()`, `patchSettings(map)`, `sendConfigRequest()` and `featureFlag()` on `LanternService`
  - `featureFlagProvider`, `LocalStorageService.getBool` and `setBool`, and `sl`
- Produces:
  - `FeatureFlag.helperInvites` (key `helper_invites`), `AppBuildInfo.enableHelperInvites`
  - `class HelperKeyStore { Future<String> stateKey(); }`, registered in `sl`
  - Models: `HelpersView`, `HelperInviteView`, `HelperClaimView`, `HelperPairingView` and `HelperActivity`, each with `fromJson`, parsed tolerantly
  - `class HelperResult { Map<String, dynamic> data; String? error; bool get ok; }`
  - `class HelpersState { HelpersView view; String? error; bool helpOthers; int helpingApprovals; }`
  - `helpersProvider` (`HelpersNotifier`), with `ensureUnlocked()`, `refresh()`, `refreshIfUsed()`, `refreshView()`, `createInvite(name)`, `decide(id, approve:)`, `forgetInvite(id)`, `claim(link, name)`, `holdLink(link)`, `takeLink()`, `endPairing(id)`, `endAllHelpers()`, `loadHelpOthers()` and `setHelpOthers(bool)`
  - `helperInvitesAvailableProvider` (bool)
  - `String helperErrorText(String code)`
  - Tasks 13 and 14 build the screens on these.

- [ ] **Step 1: Add the dependency and the Linux packaging**

```bash
flutter pub add 'flutter_secure_storage:^9.2.4'
```

In `linux/packaging/nfpm.yaml`:
- under `overrides.deb.depends`, add `- libsecret-1-0`;
- under `overrides.rpm.depends`, add `- libsecret`;
- under `overrides.archlinux.depends`, add `- libsecret`.

In `.github/workflows/build-linux.yml`, in `required_packages=(`, add `libsecret-1-dev` after `libwebkit2gtk-4.1-dev`. In `Dockerfile`, add `libsecret-1-dev \` after the first `libgtk-3-dev \`.

Check the minimum OS. flutter_secure_storage 9.x needs Android `minSdk` 23; the app has 24. It also needs macOS 10.14; the app targets 10.15. Run `grep -n MACOSX_DEPLOYMENT_TARGET macos/Runner.xcodeproj/project.pbxproj | head -3` to confirm.

- [ ] **Step 2: Write the failing tests**

`test/features/helpers/helpers_view_test.dart`:

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';

void main() {
  test('parses a full view', () {
    final v = HelpersView.fromJson({
      'unlocked': true,
      'my_name': 'Sara',
      'invites': [
        {'id': 'i1', 'state': 'claimed', 'expires_unix': 1800000000, 'code': '1234', 'volunteer_name': 'Ali'},
        {'id': 'i2', 'state': 'pending', 'link': 'https://lantern.io/help/#x', 'expires_unix': 1800000000},
      ],
      'claims': [{'id': 'c1', 'code': '9876', 'expires_unix': 1800000000}],
      'helpers': [{'pairing_id': 'p1', 'name': 'Ali', 'online': true, 'since_unix': 1700000000}],
      'helping': [{'pairing_id': 'p2', 'name': 'Sara', 'since_unix': 1700000000}],
      'activity': {'day': '2026-10-23', 'active_seconds': 3600, 'relayed_bytes': 42},
      'new_helping': true,
    });
    expect(v.unlocked, isTrue);
    expect(v.invites.first.claimed, isTrue);
    expect(v.invites.last.link, 'https://lantern.io/help/#x');
    expect(v.claims.single.code, '9876');
    expect(v.helpers.single.online, isTrue);
    expect(v.helping.single.name, 'Sara');
    expect(v.activity!.activeSeconds, 3600);
    expect(v.newHelping, isTrue);
    expect(v.needsPolling, isTrue);
  });

  test('a malformed entry is skipped, not fatal', () {
    final v = HelpersView.fromJson({
      'unlocked': true,
      'helpers': [
        'junk',
        {'pairing_id': 'p1', 'name': 7, 'online': 'yes', 'since_unix': '1700000000'},
      ],
      'invites': 'not a list',
    });
    expect(v.helpers.single.pairingId, 'p1');
    expect(v.helpers.single.name, '');
    expect(v.helpers.single.online, isFalse);
    expect(v.helpers.single.sinceUnix, 1700000000);
    expect(v.invites, isEmpty);
    expect(v.needsPolling, isFalse);
  });
}
```

`test/features/helpers/helper_key_store_test.dart`:

```dart
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:lantern/features/helpers/helper_key_store.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('creates one 32-byte key and reuses it', () async {
    FlutterSecureStorage.setMockInitialValues({});
    final store = HelperKeyStore();
    final a = await store.stateKey();
    final b = await store.stateKey();
    expect(a, hasLength(44)); // base64 of 32 bytes
    expect(b, a);
  });
}
```

`test/features/helpers/helpers_notifier_test.dart`:

```dart
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:fpdart/fpdart.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/services/injection_container.dart';
import 'package:lantern/core/services/local_storage_service.dart';
import 'package:lantern/core/utils/failure.dart';
import 'package:lantern/features/helpers/helper_key_store.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';
import 'package:lantern/lantern/lantern_service.dart';
import 'package:lantern/lantern/lantern_service_notifier.dart';

class _Service implements LanternService {
  final calls = <String>[];
  final patches = <Map<String, dynamic>>[];
  int configRequests = 0;
  Map<String, String> answers = {};

  @override
  Future<Either<Failure, String>> helperCall(String op, Map<String, dynamic> args) async {
    calls.add(op);
    return right(answers[op] ?? '{"error":"internal"}');
  }

  @override
  Future<Either<Failure, Map<String, dynamic>>> getSettings() async => right({'peer_serve_pool': false});

  @override
  Future<Either<Failure, Unit>> patchSettings(Map<String, dynamic> updates) async {
    patches.add(updates);
    return right(unit);
  }

  @override
  Future<Either<Failure, Unit>> sendConfigRequest() async {
    configRequests++;
    return right(unit);
  }

  @override
  Future<Either<Failure, String>> featureFlag() async => right('{}');

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

class _Keys implements HelperKeyStore {
  _Keys({this.fail = false});
  final bool fail;
  @override
  Future<String> stateKey() async {
    if (fail) throw StateError('no keyring');
    return base64.encode(List.filled(32, 1));
  }
}

class _Storage implements LocalStorageService {
  final values = <String, bool>{};
  @override
  bool? getBool(String key) => values[key];
  @override
  Future<void> setBool(String key, bool value) async => values[key] = value;
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

String _view({bool newHelping = false, String helpers = '[]', String invites = '[]'}) =>
    '{"unlocked":true,"my_name":"","invites":$invites,"claims":[],"helpers":$helpers,'
    '"helping":[],"new_helping":$newHelping}';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  late _Service svc;
  late _Storage storage;

  ProviderContainer container() {
    final c = ProviderContainer(overrides: [lanternServiceProvider.overrideWithValue(svc)]);
    addTearDown(c.dispose);
    return c;
  }

  setUp(() {
    svc = _Service();
    storage = _Storage();
    sl.registerSingleton<LocalStorageService>(storage);
    sl.registerSingleton<HelperKeyStore>(_Keys());
  });
  tearDown(() => sl.reset());

  test('refresh unlocks once and applies the view', () async {
    svc.answers = {
      'unlock': _view(),
      'refresh': _view(helpers: '[{"pairing_id":"p1","name":"Ali","online":true,"since_unix":1}]'),
    };
    final c = container();
    final r = await c.read(helpersProvider.notifier).refresh();
    expect(r.ok, isTrue);
    expect(svc.calls, ['unlock', 'refresh']);
    expect(c.read(helpersProvider).view.helpers.single.name, 'Ali');
    expect(storage.values[HelpersNotifier.usedKey], isTrue);
    await c.read(helpersProvider.notifier).refresh();
    expect(svc.calls, ['unlock', 'refresh', 'refresh'], reason: 'unlock runs once');
  });

  test('refreshIfUsed touches nothing for a device that never used helpers', () async {
    final c = container();
    await c.read(helpersProvider.notifier).refreshIfUsed();
    expect(svc.calls, isEmpty);
  });

  test('an error envelope becomes the state error', () async {
    svc.answers = {'unlock': _view(), 'create_invite': '{"error":"too_many_invites"}'};
    final c = container();
    final r = await c.read(helpersProvider.notifier).createInvite('Sara');
    expect(r.error, 'too_many_invites');
    expect(c.read(helpersProvider).error, 'too_many_invites');
  });

  test('a new pairing to help counts an approval and refetches config', () async {
    svc.answers = {'unlock': _view(), 'refresh': _view(newHelping: true)};
    final c = container();
    await c.read(helpersProvider.notifier).refresh();
    expect(c.read(helpersProvider).helpingApprovals, 1);
    await Future<void>.delayed(Duration.zero);
    expect(svc.configRequests, 1);
  });

  test('a keystore failure never calls the engine', () async {
    sl.unregister<HelperKeyStore>();
    sl.registerSingleton<HelperKeyStore>(_Keys(fail: true));
    final c = container();
    final r = await c.read(helpersProvider.notifier).refresh();
    expect(r.error, 'keystore');
    expect(svc.calls, isEmpty);
  });

  test('help others too reads and writes peer_serve_pool', () async {
    final c = container();
    await c.read(helpersProvider.notifier).loadHelpOthers();
    expect(c.read(helpersProvider).helpOthers, isFalse);
    await c.read(helpersProvider.notifier).setHelpOthers(true);
    expect(svc.patches.single, {'peer_serve_pool': true});
    expect(c.read(helpersProvider).helpOthers, isTrue);
  });
}
```

Run: `flutter test test/features/helpers/`
Expected: compilation fails, because `helpers_view.dart`, `helper_key_store.dart` and `helpers_notifier.dart` do not exist.

- [ ] **Step 3: The gate**

In `lib/core/models/feature_flags.dart`, add before `unbounded('unbounded');`:

```dart
  // Server-side gate for the inviter's side of helper invites ("Get help
  // from a friend"). lantern-cloud sets it for the pilot's inviters only.
  // Volunteers are never flagged: their screens show through an invite link
  // or a pairing (lantern-cloud lets them through without the flag).
  helperInvites('helper_invites'),
```

In `lib/core/common/app_build_info.dart`, after `enableQrScanner`, add:

```dart
  /// Helper invites are a recognisable Lantern artifact in a chat, so
  /// stealth artifacts leave them out.
  static const bool enableHelperInvites = !suppressIdentifyingFeatures;
```

- [ ] **Step 4: Models, keystore and errors**

`lib/features/helpers/models/helpers_view.dart`:

```dart
/// What lantern-core's helper engine reports (lantern-core/helper View).
/// Parsed tolerantly: an entry of the wrong shape is skipped, and a field of
/// the wrong type falls back to its default.
class HelpersView {
  const HelpersView({
    this.unlocked = false,
    this.myName = '',
    this.invites = const [],
    this.claims = const [],
    this.helpers = const [],
    this.helping = const [],
    this.activity,
    this.newHelping = false,
  });

  final bool unlocked;
  final String myName;
  final List<HelperInviteView> invites;
  final List<HelperClaimView> claims;
  final List<HelperPairingView> helpers;
  final List<HelperPairingView> helping;
  final HelperActivity? activity;
  final bool newHelping;

  /// Something is waiting on the other person: poll for it.
  bool get needsPolling => invites.isNotEmpty || claims.isNotEmpty;

  factory HelpersView.fromJson(Map<String, dynamic> j) => HelpersView(
    unlocked: j['unlocked'] == true,
    myName: _str(j['my_name']),
    invites: _list(j['invites'], HelperInviteView.fromJson),
    claims: _list(j['claims'], HelperClaimView.fromJson),
    helpers: _list(j['helpers'], HelperPairingView.fromJson),
    helping: _list(j['helping'], HelperPairingView.fromJson),
    activity: j['activity'] is Map<String, dynamic>
        ? HelperActivity.fromJson(j['activity'] as Map<String, dynamic>)
        : null,
    newHelping: j['new_helping'] == true,
  );
}

class HelperInviteView {
  const HelperInviteView({
    required this.id,
    required this.state,
    this.link = '',
    this.expiresUnix = 0,
    this.code = '',
    this.volunteerName = '',
  });

  final String id;
  final String state;
  final String link;
  final int expiresUnix;
  final String code;
  final String volunteerName;

  bool get claimed => state == 'claimed';

  factory HelperInviteView.fromJson(Map<String, dynamic> j) => HelperInviteView(
    id: _str(j['id']),
    state: _str(j['state']),
    link: _str(j['link']),
    expiresUnix: _int(j['expires_unix']),
    code: _str(j['code']),
    volunteerName: _str(j['volunteer_name']),
  );
}

class HelperClaimView {
  const HelperClaimView({required this.id, required this.code, this.expiresUnix = 0});

  final String id;
  final String code;
  final int expiresUnix;

  factory HelperClaimView.fromJson(Map<String, dynamic> j) => HelperClaimView(
    id: _str(j['id']),
    code: _str(j['code']),
    expiresUnix: _int(j['expires_unix']),
  );
}

class HelperPairingView {
  const HelperPairingView({
    required this.pairingId,
    this.name = '',
    this.online = false,
    this.sinceUnix = 0,
  });

  final String pairingId;
  final String name;
  final bool online;
  final int sinceUnix;

  factory HelperPairingView.fromJson(Map<String, dynamic> j) => HelperPairingView(
    pairingId: _str(j['pairing_id']),
    name: _str(j['name']),
    online: j['online'] == true,
    sinceUnix: _int(j['since_unix']),
  );
}

class HelperActivity {
  const HelperActivity({this.day = '', this.activeSeconds = 0, this.relayedBytes = 0});

  final String day;
  final int activeSeconds;
  final int relayedBytes;

  factory HelperActivity.fromJson(Map<String, dynamic> j) => HelperActivity(
    day: _str(j['day']),
    activeSeconds: _int(j['active_seconds']),
    relayedBytes: _int(j['relayed_bytes']),
  );
}

String _str(Object? v) => v is String ? v : '';

int _int(Object? v) => switch (v) {
  int i => i,
  num n => n.toInt(),
  String s => int.tryParse(s) ?? 0,
  _ => 0,
};

List<T> _list<T>(Object? v, T Function(Map<String, dynamic>) parse) {
  if (v is! List) return const [];
  final out = <T>[];
  for (final e in v) {
    if (e is! Map<String, dynamic>) continue;
    try {
      out.add(parse(e));
    } catch (_) {
      // One malformed entry must not hide the others.
    }
  }
  return out;
}
```

`lib/features/helpers/helper_key_store.dart`:

```dart
import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Holds the key that seals lantern-core's helper state (invite secrets, the
/// device key and helper names) in the OS keystore: Keychain, the Android
/// Keystore, Windows Credential Manager or libsecret. The key never leaves
/// the device and is never logged.
class HelperKeyStore {
  HelperKeyStore([FlutterSecureStorage? storage])
    : _storage =
          storage ??
          const FlutterSecureStorage(
            aOptions: AndroidOptions(encryptedSharedPreferences: true),
            // The macOS app is not sandboxed; the file-based keychain needs no
            // access-group entitlement.
            mOptions: MacOsOptions(useDataProtectionKeyChain: false),
          );

  final FlutterSecureStorage _storage;
  static const _name = 'lantern_helper_state_key_v1';

  /// Returns the key, creating it on first use. Throws when the keystore is
  /// unavailable, as on Linux without a running keyring.
  Future<String> stateKey() async {
    final existing = await _storage.read(key: _name);
    if (existing != null && existing.length == 44) return existing;
    final rnd = Random.secure();
    final key = base64.encode(
      Uint8List.fromList(List<int>.generate(32, (_) => rnd.nextInt(256))),
    );
    await _storage.write(key: _name, value: key);
    return key;
  }
}
```

`lib/features/helpers/helper_errors.dart`:

```dart
import 'package:lantern/core/localization/i18n.dart';

/// The text for one of lantern-core/helper's error codes.
String helperErrorText(String code) => switch (code) {
  'disabled' => 'helper_error_disabled'.i18n,
  'already_claimed' => 'help_friend_already_claimed_body'.i18n,
  'no_pending_claim' => 'helper_error_no_pending_claim'.i18n,
  'too_many_invites' => 'helper_error_too_many_invites'.i18n,
  'too_many_pairings' => 'helper_error_too_many_pairings'.i18n,
  'bad_link' => 'help_friend_bad_link'.i18n,
  'bad_name' => 'helper_error_bad_name'.i18n,
  'own_invite' => 'help_friend_own_invite'.i18n,
  'keystore' => 'helper_error_keystore'.i18n,
  'keystore_reset' => 'helper_error_keystore_reset'.i18n,
  _ => 'helper_error_unavailable'.i18n,
};
```

- [ ] **Step 5: The notifier**

`lib/features/helpers/provider/helpers_notifier.dart`:

```dart
import 'dart:async';
import 'dart:convert';

import 'package:flutter/widgets.dart';
import 'package:lantern/core/common/app_build_info.dart';
import 'package:lantern/core/models/feature_flags.dart';
import 'package:lantern/core/services/injection_container.dart';
import 'package:lantern/core/services/local_storage_service.dart';
import 'package:lantern/core/services/logger_service.dart';
import 'package:lantern/features/helpers/helper_key_store.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/home/provider/feature_flag_notifier.dart';
import 'package:lantern/lantern/lantern_service.dart';
import 'package:lantern/lantern/lantern_service_notifier.dart';
import 'package:riverpod_annotation/riverpod_annotation.dart';

part 'helpers_notifier.g.dart';

/// Whether this build and the server let this device use helper invites.
@riverpod
bool helperInvitesAvailable(Ref ref) =>
    AppBuildInfo.enableHelperInvites &&
    ref.watch(featureFlagProvider).getBool(FeatureFlag.helperInvites);

/// One engine call's result: its JSON, or an error code.
class HelperResult {
  const HelperResult._(this.data, this.error);

  final Map<String, dynamic> data;
  final String? error;

  bool get ok => error == null;

  factory HelperResult.failure(String code) => HelperResult._(const {}, code);

  factory HelperResult.parse(String raw) {
    try {
      final decoded = jsonDecode(raw);
      if (decoded is Map<String, dynamic>) {
        final err = decoded['error'];
        if (err != null) {
          final code = err.toString();
          return HelperResult._(const {}, code == 'not_initialized' ? 'unavailable' : code);
        }
        return HelperResult._(decoded, null);
      }
    } catch (_) {
      // Not JSON: the core is not answering.
    }
    return const HelperResult._({}, 'unavailable');
  }
}

class HelpersState {
  const HelpersState({
    this.view = const HelpersView(),
    this.error,
    this.helpOthers = true,
    this.helpingApprovals = 0,
  });

  final HelpersView view;

  /// The last operation's error code, cleared by the next success.
  final String? error;

  /// "Help others too" (radiance peer_serve_pool; unset means on).
  final bool helpOthers;

  /// Counts refreshes that found a new pairing this device helps. Home starts
  /// sharing when it grows.
  final int helpingApprovals;

  HelpersState copyWith({
    HelpersView? view,
    String? error,
    bool clearError = false,
    bool? helpOthers,
    int? helpingApprovals,
  }) => HelpersState(
    view: view ?? this.view,
    error: clearError ? null : (error ?? this.error),
    helpOthers: helpOthers ?? this.helpOthers,
    helpingApprovals: helpingApprovals ?? this.helpingApprovals,
  );
}

/// The app's side of helper invites. Every operation goes to lantern-core's
/// engine through [LanternService.helperCall]. Links, names and codes are
/// never logged.
@Riverpod(keepAlive: true)
class HelpersNotifier extends _$HelpersNotifier {
  /// Set once the user has used helper invites, so the keystore is never
  /// touched for anyone else.
  static const usedKey = 'helper_invites_used';

  @visibleForTesting
  static Duration pollInterval = const Duration(seconds: 30);

  Timer? _poll;
  Future<void>? _unlocking;
  String? _heldLink;

  /// Holds an invite link that opened the app, for HelpFriend to take, so
  /// the secret never travels in route arguments. Memory only.
  void holdLink(String link) => _heldLink = link;

  /// Returns the held link once, and forgets it.
  String? takeLink() {
    final link = _heldLink;
    _heldLink = null;
    return link;
  }

  @override
  HelpersState build() {
    ref.onDispose(() => _poll?.cancel());
    return const HelpersState();
  }

  LanternService get _svc => ref.read(lanternServiceProvider);

  LocalStorageService get _storage => sl<LocalStorageService>();

  bool get used => _storage.getBool(usedKey) ?? false;

  Future<HelperResult> _call(String op, [Map<String, dynamic> args = const {}]) async {
    final res = await _svc.helperCall(op, args);
    return res.fold((_) => HelperResult.failure('unavailable'), HelperResult.parse);
  }

  /// Applies a result. A view result replaces the view; an error is kept.
  HelperResult _apply(HelperResult r, {bool isView = true}) {
    if (!ref.mounted) return r;
    if (!r.ok) {
      state = state.copyWith(error: r.error);
      return r;
    }
    if (isView) {
      final view = HelpersView.fromJson(r.data);
      state = state.copyWith(
        view: view,
        clearError: true,
        helpingApprovals: view.newHelping ? state.helpingApprovals + 1 : null,
      );
      _schedulePoll(view);
    } else {
      state = state.copyWith(clearError: true);
    }
    return r;
  }

  /// Unlocks the engine with the keystore key, once.
  Future<bool> ensureUnlocked() async {
    if (state.view.unlocked) return true;
    await (_unlocking ??= _unlock().whenComplete(() => _unlocking = null));
    return state.view.unlocked;
  }

  Future<void> _unlock() async {
    await _storage.setBool(usedKey, true);
    final String key;
    try {
      key = await sl<HelperKeyStore>().stateKey();
    } catch (_) {
      appLogger.error('helper: the OS keystore is unavailable');
      if (ref.mounted) state = state.copyWith(error: 'keystore');
      return;
    }
    final r = _apply(await _call('unlock', {'key': key}));
    if (r.ok && r.data['keystore_reset'] == true && ref.mounted) {
      // The keystore returned a new key: the old names could not be read and
      // were set aside. Tell the user once.
      state = state.copyWith(error: 'keystore_reset');
    }
  }

  Future<HelperResult?> _guard() async {
    if (await ensureUnlocked()) return null;
    return HelperResult.failure(state.error ?? 'keystore');
  }

  Future<HelperResult> refresh() async {
    final locked = await _guard();
    if (locked != null) return locked;
    final r = _apply(await _call('refresh'));
    if (r.ok && r.data['new_helping'] == true) {
      // A new person to help: fetch the config that turns the widget on for
      // paired volunteers, then the features that show its tab.
      unawaited(
        _svc.sendConfigRequest().then((_) {
          if (ref.mounted) ref.read(featureFlagProvider.notifier).fetchFeatureFlags();
        }),
      );
    }
    return r;
  }

  /// Refreshes only on a device that has used helper invites, in a build
  /// that has them.
  Future<void> refreshIfUsed() async {
    if (AppBuildInfo.enableHelperInvites && used) await refresh();
  }

  Future<HelperResult> refreshView() async => _apply(await _call('view'));

  Future<HelperResult> createInvite(String name) async {
    final locked = await _guard();
    if (locked != null) return locked;
    final r = _apply(await _call('create_invite', {'name': name}), isView: false);
    if (r.ok) await refreshView();
    return r;
  }

  Future<HelperResult> decide(String inviteId, {required bool approve}) async =>
      _apply(await _call('decide', {'invite_id': inviteId, 'approve': approve}));

  Future<HelperResult> forgetInvite(String inviteId) async =>
      _apply(await _call('forget_invite', {'invite_id': inviteId}));

  Future<HelperResult> claim(String link, String name) async {
    final locked = await _guard();
    if (locked != null) return locked;
    final r = _apply(await _call('claim', {'link': link, 'name': name}), isView: false);
    if (r.ok) await refreshView();
    return r;
  }

  Future<HelperResult> endPairing(String pairingId) async =>
      _apply(await _call('end_pairing', {'pairing_id': pairingId}));

  Future<HelperResult> endAllHelpers() async => _apply(await _call('end_all_helpers'));

  Future<void> loadHelpOthers() async {
    final res = await _svc.getSettings();
    if (!ref.mounted) return;
    res.fold((_) {}, (s) {
      final v = s['peer_serve_pool'];
      state = state.copyWith(helpOthers: v is bool ? v : true);
    });
  }

  Future<void> setHelpOthers(bool value) async {
    final res = await _svc.patchSettings({'peer_serve_pool': value});
    if (!ref.mounted) return;
    res.fold(
      (f) => appLogger.error('helper: setting help-others-too failed: ${f.error}'),
      (_) => state = state.copyWith(helpOthers: value),
    );
  }

  void _schedulePoll(HelpersView view) {
    if (!view.needsPolling) {
      _poll?.cancel();
      _poll = null;
      return;
    }
    _poll ??= Timer.periodic(pollInterval, (_) {
      if (WidgetsBinding.instance.lifecycleState != AppLifecycleState.resumed) return;
      unawaited(refresh());
    });
  }
}
```

In `lib/core/services/injection_container.dart`, after the `DeepLinkCallbackManager` registration, add:

```dart
  sl.registerLazySingleton<HelperKeyStore>(() => HelperKeyStore());
```

with `import 'package:lantern/features/helpers/helper_key_store.dart';`.

- [ ] **Step 6: Error strings**

Append to `assets/locales/en.po`:

```po
# Helper invites: errors. "Lantern" is the app.
msgid "helper_error_disabled"
msgstr "Helping friends isn't available on this device yet."

msgid "helper_error_unavailable"
msgstr "Couldn't reach Lantern. Check your connection and try again."

msgid "helper_error_too_many_invites"
msgstr "You've sent as many invites as you can for now. Try again tomorrow."

msgid "helper_error_too_many_pairings"
msgstr "You've reached the limit of 20 people."

msgid "helper_error_no_pending_claim"
msgstr "This invite has already been decided."

msgid "helper_error_bad_name"
msgstr "Enter a name your friend will recognise, up to 60 characters."

msgid "helper_error_keystore"
msgstr "This device can't store names securely, so helping friends is off. On Linux, start a keyring such as GNOME Keyring."

msgid "helper_error_keystore_reset"
msgstr "This device's secure storage was reset, so the names of your helpers and invites could not be read. Your helpers still work; invite them again to see their names."
```

The strings `help_friend_already_claimed_body`, `help_friend_bad_link` and `help_friend_own_invite` are added in Task 14.

- [ ] **Step 7: Generate, run the tests, commit**

```bash
make gen
flutter analyze
flutter test test/features/helpers/
make find-duplicate-translations
```

Expected:
- `make gen` writes `helpers_notifier.g.dart`, with `helpersProvider` and `helperInvitesAvailableProvider`.
- Analyze is clean.
- The three test files pass.
- No duplicate msgids.

```bash
git add pubspec.yaml pubspec.lock linux/packaging/nfpm.yaml .github/workflows/build-linux.yml Dockerfile \
  lib/core/models/feature_flags.dart lib/core/common/app_build_info.dart lib/features/helpers \
  lib/core/services/injection_container.dart assets/locales/en.po test/features/helpers
git commit -m "app: helper-invite state, keystore and feature gate"
```

---

### Task 13: lantern — the inviter's screens

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Create: `lib/features/helpers/get_help.dart`
- Create: `lib/features/helpers/invite_share.dart`
- Create: `lib/features/helpers/approve_helper_sheet.dart`
- Modify: `lib/core/router/router.dart` (two routes), and regenerate `router.gr.dart`
- Modify: `lib/features/setting/setting.dart` (a tile and its `_SettingType`)
- Modify: `lib/lantern/lantern_platform_service.dart` (`copySensitiveText`), `android/app/src/main/kotlin/org/getlantern/lantern/handler/MethodHandler.kt` (`copySensitiveText`)
- Modify: `assets/locales/en.po`
- Create: `test/features/helpers/inviter_screens_test.dart`

**Interfaces:**
- Consumes: Task 12's `helpersProvider`, `HelpersState`, `HelperInviteView`, `HelperPairingView`, `HelperResult`, `helperErrorText` and `helperInvitesAvailableProvider`; `AppDialog.show`, `AppTile`, `AppCard`, `PrimaryButton`, `SecondaryButton`, `AppTextButton`, `AppTextField`, `BaseScreen`, `QrImageView` and `SharePlus`.
- Produces:
  - Routes `GetHelp()` (`/get-help`) and `InviteShare({required String inviteId})` (`/invite-share`)
  - `Future<void> showApproveHelperSheet(BuildContext context, HelperInviteView invite)`
  - `Future<String?> askHelperName(BuildContext context, {required String initial, required String title})`, used by `GetHelp`

**The flow the spec asks for:**
- Settings → "Get help from a friend".
- "Invite someone to help me" asks for the name the friend knows Sara by, then shows the invite screen. That screen has "Share link", which opens the OS share sheet with "Sara wants your help staying online. <link>", plus a QR code and "Copy link".
- When the friend claims the invite, the list shows "Someone opened your invite". Its sheet shows the four-digit code and suggests comparing it on a call or in another app. It offers Approve, and Reject with the leaked-link explanation.
- Each helper row has an online or offline status and a remove action.
- With exactly one helper, the screen nudges Sara toward a second.
- "Remove all helpers" covers a seized phone.

- [ ] **Step 1: Write the failing widget tests**

`test/features/helpers/inviter_screens_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
// common.dart re-exports the generated route classes, which share the
// screens' names.
import 'package:lantern/core/common/common.dart' hide GetHelp;
import 'package:lantern/features/helpers/approve_helper_sheet.dart';
import 'package:lantern/features/helpers/get_help.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

class _Helpers extends HelpersNotifier {
  _Helpers(this.initial);
  final HelpersState initial;
  final decisions = <(String, bool)>[];
  int refreshes = 0;

  @override
  HelpersState build() => initial;

  @override
  Future<void> refreshIfUsed() async => refreshes++;

  @override
  Future<HelperResult> decide(String inviteId, {required bool approve}) async {
    decisions.add((inviteId, approve));
    return HelperResult.parse('{"unlocked":true}');
  }
}

Future<void> _pump(WidgetTester tester, _Helpers fake, Widget child) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [helpersProvider.overrideWith(() => fake)],
      child: ScreenUtilInit(
        designSize: const Size(393, 852),
        child: MaterialApp(theme: AppTheme.appTheme(), home: Scaffold(body: child)),
      ),
    ),
  );
  await tester.pump();
}

const _claimed = HelperInviteView(id: 'i1', state: 'claimed', code: '2562', volunteerName: 'Ali');

void main() {
  testWidgets('the approve sheet shows the code and approves', (tester) async {
    final fake = _Helpers(const HelpersState());
    await _pump(tester, fake, const ApproveHelperSheet(invite: _claimed));
    expect(find.byKey(const Key('approve.code')), findsOneWidget);
    expect(find.text('2562'), findsOneWidget);
    await tester.tap(find.byKey(const Key('approve.yes')));
    await tester.pumpAndSettle();
    expect(fake.decisions, [('i1', true)]);
  });

  testWidgets('the approve sheet rejects', (tester) async {
    final fake = _Helpers(const HelpersState());
    await _pump(tester, fake, const ApproveHelperSheet(invite: _claimed));
    await tester.tap(find.byKey(const Key('approve.no')));
    await tester.pumpAndSettle();
    expect(fake.decisions, [('i1', false)]);
  });

  testWidgets('one helper: status shown and a second helper suggested', (tester) async {
    final fake = _Helpers(const HelpersState(
      view: HelpersView(unlocked: true, helpers: [
        HelperPairingView(pairingId: 'p1', name: 'Ali', online: true, sinceUnix: 1700000000),
      ]),
    ));
    await _pump(tester, fake, const GetHelp());
    expect(find.text('Ali'), findsOneWidget);
    expect(find.byKey(const Key('helpers.online.0')), findsOneWidget);
    expect(find.byKey(const Key('helpers.nudge')), findsOneWidget);
    expect(fake.refreshes, 1, reason: 'opening the screen refreshes, if helper invites were used');
  });

  testWidgets('two helpers: no nudge; offline shown', (tester) async {
    final fake = _Helpers(const HelpersState(
      view: HelpersView(unlocked: true, helpers: [
        HelperPairingView(pairingId: 'p1', name: 'Ali', online: true),
        HelperPairingView(pairingId: 'p2', name: 'Bo'),
      ]),
    ));
    await _pump(tester, fake, const GetHelp());
    expect(find.byKey(const Key('helpers.nudge')), findsNothing);
    expect(find.byKey(const Key('helpers.offline.1')), findsOneWidget);
  });

  testWidgets('a claimed invite is surfaced above the helpers', (tester) async {
    final fake = _Helpers(const HelpersState(view: HelpersView(unlocked: true, invites: [_claimed])));
    await _pump(tester, fake, const GetHelp());
    expect(find.byKey(const Key('helpers.claimed.i1')), findsOneWidget);
  });
}
```

Run: `flutter test test/features/helpers/inviter_screens_test.dart`
Expected: compilation fails, because the screens do not exist.

- [ ] **Step 2: Write the approve sheet and the name prompt**

`lib/features/helpers/approve_helper_sheet.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_hooks/flutter_hooks.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/helper_errors.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// Shows the matching code of a claimed invite and lets the inviter approve
/// or reject the person who claimed it.
Future<void> showApproveHelperSheet(BuildContext context, HelperInviteView invite) {
  return showModalBottomSheet<void>(
    context: context,
    isScrollControlled: true,
    builder: (_) => ApproveHelperSheet(invite: invite),
  );
}

class ApproveHelperSheet extends HookConsumerWidget {
  const ApproveHelperSheet({super.key, required this.invite});

  final HelperInviteView invite;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final busy = useState(false);
    final textTheme = Theme.of(context).textTheme;
    final name = invite.volunteerName.isEmpty ? 'helper_unnamed'.i18n : invite.volunteerName;

    Future<void> decide(bool approve) async {
      busy.value = true;
      final r = await ref.read(helpersProvider.notifier).decide(invite.id, approve: approve);
      if (!context.mounted) return;
      busy.value = false;
      if (r.ok) {
        await Navigator.of(context).maybePop();
        return;
      }
      context.showSnackBarError(helperErrorText(r.error!));
      if (r.error == 'no_pending_claim') {
        // Decided elsewhere, or an approval whose answer was lost: re-read.
        await ref.read(helpersProvider.notifier).refresh();
      }
    }

    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.all(defaultSize),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('invite_claimed_title'.i18n, style: textTheme.titleLarge),
            const SizedBox(height: 12),
            Text('approve_code_intro'.i18n.fill([name]), style: textTheme.bodyMedium),
            const SizedBox(height: 16),
            Text('approve_code_label'.i18n, style: textTheme.labelMedium, textAlign: TextAlign.center),
            Text(
              invite.code,
              key: const Key('approve.code'),
              textAlign: TextAlign.center,
              style: textTheme.displayMedium?.copyWith(letterSpacing: 8),
            ),
            const SizedBox(height: 16),
            KeyedSubtree(
              key: const Key('approve.yes'),
              child: PrimaryButton(
                label: 'approve_button'.i18n,
                enabled: !busy.value,
                onPressed: () => decide(true),
              ),
            ),
            const SizedBox(height: 8),
            KeyedSubtree(
              key: const Key('approve.no'),
              child: SecondaryButton(
                label: 'reject_button'.i18n,
                enabled: !busy.value,
                onPressed: () => decide(false),
              ),
            ),
            const SizedBox(height: 8),
            Text('reject_explainer'.i18n, style: textTheme.bodySmall),
          ],
        ),
      ),
    );
  }
}

/// Asks for the name the other person knows this user by. Returns null when
/// cancelled or left empty.
Future<String?> askHelperName(
  BuildContext context, {
  required String initial,
  required String title,
}) async {
  var current = initial;
  String? result;
  await AppDialog.show(
    context: context,
    title: title,
    content: _HelperNameField(initial: initial, onChanged: (v) => current = v),
    primaryLabel: 'continue'.i18n,
    onPrimaryPressed: () => result = current.trim(),
    secondaryLabel: 'cancel'.i18n,
  );
  return (result == null || result!.isEmpty) ? null : result;
}

/// The name field inside askHelperName's dialog. It owns its controller,
/// which the hook disposes when the dialog's route is gone, after its exit
/// animation, not when show() returns.
class _HelperNameField extends HookWidget {
  const _HelperNameField({required this.initial, required this.onChanged});

  final String initial;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    final controller = useTextEditingController(text: initial);
    return AppTextField(
      controller: controller,
      label: 'helpers_your_name_label'.i18n,
      hintText: 'helpers_your_name_hint'.i18n,
      maxLength: 60,
      autofocus: true,
      onChanged: onChanged,
    );
  }
}
```

- [ ] **Step 3: Write the list screen and the invite screen**

`lib/features/helpers/get_help.dart`:

```dart
import 'dart:async';

import 'package:auto_route/auto_route.dart';
import 'package:flutter/material.dart';
import 'package:flutter_hooks/flutter_hooks.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/approve_helper_sheet.dart';
import 'package:lantern/features/helpers/helper_errors.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// The inviter's helpers: who helps her, online or not, invites waiting on a
/// friend, and claims waiting on her.
@RoutePage(name: 'GetHelp')
class GetHelp extends HookConsumerWidget {
  const GetHelp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(helpersProvider);
    final view = state.view;
    final notifier = ref.read(helpersProvider.notifier);
    final textTheme = Theme.of(context).textTheme;

    useEffect(() {
      // refreshIfUsed, not refresh: merely opening this screen must not touch
      // the keystore (Plan rules). The first invite unlocks it.
      unawaited(notifier.refreshIfUsed());
      // Online status changes while the screen is open.
      final timer = Timer.periodic(const Duration(seconds: 60), (_) => notifier.refreshIfUsed());
      return timer.cancel;
    }, const []);

    final claimed = view.invites.where((i) => i.claimed).toList();
    final pending = view.invites.where((i) => !i.claimed).toList();

    return BaseScreen(
      title: 'helpers_title'.i18n,
      body: ListView(
        padding: const EdgeInsets.symmetric(vertical: defaultSize),
        children: [
          Text('helpers_intro'.i18n, style: textTheme.bodyMedium!.copyWith(color: context.textSecondary)),
          const SizedBox(height: defaultSize),
          for (final inv in claimed) ...[
            AppCard(
              padding: EdgeInsets.zero,
              child: AppTile(
                tileKey: Key('helpers.claimed.${inv.id}'),
                label: 'invite_claimed_title'.i18n,
                subtitle: Text(inv.volunteerName.isEmpty ? 'helper_unnamed'.i18n : inv.volunteerName),
                trailing: const Icon(Icons.chevron_right),
                onPressed: () => showApproveHelperSheet(context, inv),
              ),
            ),
            const SizedBox(height: defaultSize),
          ],
          if (view.helpers.isNotEmpty) ...[
            Text('helpers_list_title'.i18n, style: textTheme.labelLarge),
            const SizedBox(height: 8),
            AppCard(
              padding: EdgeInsets.zero,
              child: Column(
                children: [
                  for (final (i, h) in view.helpers.indexed) ...[
                    if (i > 0) DividerSpace(),
                    _HelperRow(index: i, helper: h),
                  ],
                ],
              ),
            ),
            if (view.helpers.length == 1) ...[
              const SizedBox(height: defaultSize),
              Text(
                'helpers_nudge_second'.i18n,
                key: const Key('helpers.nudge'),
                style: textTheme.bodyMedium!.copyWith(color: context.textSecondary),
              ),
            ],
            const SizedBox(height: defaultSize),
          ] else if (claimed.isEmpty && pending.isEmpty) ...[
            Text('helpers_empty'.i18n, key: const Key('helpers.empty')),
            const SizedBox(height: defaultSize),
          ],
          for (final inv in pending)
            AppTile(
              label: 'invite_waiting'.i18n,
              subtitle: Text('invite_expires'.i18n.fill([_hoursLeft(inv.expiresUnix)])),
              onPressed: () => appRouter.push(InviteShare(inviteId: inv.id)),
              trailing: IconButton(
                tooltip: 'invite_forget'.i18n,
                icon: const Icon(Icons.close),
                onPressed: () => notifier.forgetInvite(inv.id),
              ),
            ),
          const SizedBox(height: defaultSize),
          KeyedSubtree(
            key: const Key('helpers.invite'),
            child: PrimaryButton(label: 'invite_someone'.i18n, onPressed: () => _invite(context, ref)),
          ),
          if (view.helpers.isNotEmpty || view.invites.isNotEmpty) ...[
            const SizedBox(height: 8),
            AppTextButton(label: 'helpers_remove_all'.i18n, onPressed: () => _removeAll(context, ref)),
          ],
          if (state.error != null) ...[
            const SizedBox(height: defaultSize),
            Text(
              helperErrorText(state.error!),
              key: const Key('helpers.error'),
              style: textTheme.bodyMedium!.copyWith(color: Theme.of(context).colorScheme.error),
            ),
          ],
        ],
      ),
    );
  }

  Future<void> _invite(BuildContext context, WidgetRef ref) async {
    final name = await askHelperName(
      context,
      initial: ref.read(helpersProvider).view.myName,
      title: 'invite_someone'.i18n,
    );
    if (name == null || !context.mounted) return;
    final r = await ref.read(helpersProvider.notifier).createInvite(name);
    if (!context.mounted) return;
    if (!r.ok) {
      context.showSnackBarError(helperErrorText(r.error!));
      return;
    }
    appRouter.push(InviteShare(inviteId: r.data['invite_id'] as String));
  }

  Future<void> _removeAll(BuildContext context, WidgetRef ref) => AppDialog.show(
    context: context,
    title: 'helpers_remove_all'.i18n,
    body: 'helpers_remove_all_body'.i18n,
    primaryLabel: 'remove'.i18n,
    onPrimaryPressed: () => ref.read(helpersProvider.notifier).endAllHelpers(),
    secondaryLabel: 'cancel'.i18n,
  );
}

String _hoursLeft(int expiresUnix) {
  final left = expiresUnix - DateTime.now().millisecondsSinceEpoch ~/ 1000;
  return '${left <= 0 ? 0 : (left + 3599) ~/ 3600}';
}

class _HelperRow extends ConsumerWidget {
  const _HelperRow({required this.index, required this.helper});

  final int index;
  final HelperPairingView helper;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final name = helper.name.isEmpty ? 'helper_unnamed'.i18n : helper.name;
    return AppTile(
      label: name,
      subtitle: Row(
        key: Key(helper.online ? 'helpers.online.$index' : 'helpers.offline.$index'),
        children: [
          Icon(Icons.circle, size: 10, color: helper.online ? context.statusSuccessBg : context.textTertiary),
          const SizedBox(width: 6),
          Text(helper.online ? 'helper_online'.i18n : 'helper_offline'.i18n),
        ],
      ),
      trailing: IconButton(
        tooltip: 'remove'.i18n,
        icon: const Icon(Icons.person_remove_outlined),
        onPressed: () => AppDialog.show(
          context: context,
          title: 'helper_remove_confirm_title'.i18n.fill([name]),
          body: 'helper_remove_confirm_body'.i18n.fill([name]),
          primaryLabel: 'remove'.i18n,
          onPrimaryPressed: () => ref.read(helpersProvider.notifier).endPairing(helper.pairingId),
          secondaryLabel: 'cancel'.i18n,
        ),
      ),
    );
  }
}
```

`lib/features/helpers/invite_share.dart`:

```dart
import 'package:auto_route/auto_route.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/approve_helper_sheet.dart';
import 'package:lantern/core/services/injection_container.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';
import 'package:lantern/lantern/lantern_platform_service.dart';
import 'package:qr_flutter/qr_flutter.dart';
import 'package:share_plus/share_plus.dart';

/// Copies an invite link. On Android it is marked sensitive, so Android 13+
/// hides it from the clipboard preview and keyboard clipboard history; the
/// link is a secret.
Future<void> copySensitiveText(String text) async {
  if (PlatformUtils.isAndroid) {
    await sl<LanternPlatformService>().copySensitiveText(text);
    return;
  }
  await Clipboard.setData(ClipboardData(text: text));
}

/// One invite: share it as a link or a QR code, then compare codes once a
/// friend opens it. It reads the invite from the engine's view by ID, so the
/// link never travels in route arguments.
@RoutePage(name: 'InviteShare')
class InviteShare extends ConsumerWidget {
  const InviteShare({super.key, required this.inviteId});

  final String inviteId;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final view = ref.watch(helpersProvider.select((s) => s.view));
    final invite = view.invites.where((i) => i.id == inviteId).firstOrNull;
    final textTheme = Theme.of(context).textTheme;

    if (invite == null) {
      return BaseScreen(
        title: 'invite_share_title'.i18n,
        body: Center(child: Text('invite_gone'.i18n, key: const Key('invite.gone'))),
      );
    }
    if (invite.claimed) {
      return BaseScreen(
        title: 'invite_share_title'.i18n,
        body: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const SizedBox(height: defaultSize),
            Text('invite_claimed_title'.i18n, style: textTheme.titleLarge),
            const SizedBox(height: defaultSize),
            PrimaryButton(
              label: 'invite_compare_codes'.i18n,
              onPressed: () => showApproveHelperSheet(context, invite),
            ),
          ],
        ),
      );
    }

    final message = 'invite_share_message'.i18n.fill([view.myName, invite.link]);
    return BaseScreen(
      title: 'invite_share_title'.i18n,
      body: ListView(
        padding: const EdgeInsets.symmetric(vertical: defaultSize),
        children: [
          Text('invite_share_body'.i18n, style: textTheme.bodyMedium!.copyWith(color: context.textSecondary)),
          const SizedBox(height: 24),
          KeyedSubtree(
            key: const Key('invite.share'),
            child: PrimaryButton(
              label: 'invite_share_button'.i18n,
              onPressed: () => SharePlus.instance.share(ShareParams(text: message)),
            ),
          ),
          const SizedBox(height: 8),
          SecondaryButton(
            label: 'invite_copy_link'.i18n,
            onPressed: () async {
              try {
                await copySensitiveText(invite.link);
                if (context.mounted) context.showSnackBar('invite_link_copied'.i18n);
              } catch (_) {
                if (context.mounted) context.showSnackBarError('helper_error_copy'.i18n);
              }
            },
          ),
          const SizedBox(height: 24),
          Center(
            child: QrImageView(
              key: const Key('invite.qr'),
              data: invite.link,
              version: QrVersions.auto,
              size: 220,
              backgroundColor: Colors.white,
            ),
          ),
          const SizedBox(height: 8),
          Center(child: Text('invite_qr_hint'.i18n, style: textTheme.labelMedium)),
          const SizedBox(height: 24),
          Row(
            children: [
              const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
              const SizedBox(width: 8),
              Expanded(child: Text('invite_waiting'.i18n)),
            ],
          ),
        ],
      ),
    );
  }
}
```

- [ ] **Step 3b: Sensitive copy on Android**

Add to `LanternPlatformService` in `lib/lantern/lantern_platform_service.dart`. It is Android-only, so it is not part of `LanternCoreService`:

```dart
  /// Android only: copies [text] with ClipDescription's IS_SENSITIVE extra.
  Future<void> copySensitiveText(String text) =>
      _methodChannel.invokeMethod('copySensitiveText', {'text': text});
```

In `MethodHandler.kt`:
- add `CopySensitiveText("copySensitiveText"),` to the `Methods` enum;
- add the imports `android.content.ClipData`, `android.content.ClipboardManager` and `android.os.PersistableBundle`;
- add a branch to the `when`:

```kotlin
            // Copies text marked sensitive, so Android 13+ keeps it out of the
            // clipboard preview and keyboards' clipboard history. Used for
            // helper invite links, which are secrets. setExtras needs API 24,
            // the app's minSdk; older releases ignore the flag.
            Methods.CopySensitiveText.method -> {
                try {
                    val text = call.argument<String>("text") ?: error("Missing text")
                    val clip = ClipData.newPlainText("", text)
                    clip.description.extras = PersistableBundle().apply {
                        putBoolean("android.content.extra.IS_SENSITIVE", true)
                    }
                    val cm = appContext.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                    cm.setPrimaryClip(clip)
                    result.success(null)
                } catch (e: Exception) {
                    result.error("copy_sensitive_text", e.localizedMessage, null)
                }
            }
```

Add the string to `assets/locales/en.po` with Step 5's:

```po
msgid "helper_error_copy"
msgstr "Couldn't copy the link. Use Share link instead."
```

- [ ] **Step 4: Routes and the settings entry**

In `lib/core/router/router.dart`, add after the `/invite-friends` route:

```dart
    AutoRoute(
      path: '/get-help',
      page: GetHelp.page,
    ),
    AutoRoute(
      path: '/invite-share',
      page: InviteShare.page,
    ),
```

In `lib/features/setting/setting.dart`:
- Add `getHelp,` to `_SettingType` after `actionModeSetting,`.
- Add the import `package:lantern/features/helpers/provider/helpers_notifier.dart`.
- In `build`, after `unboundedAvailable`, add `final helperInvites = ref.watch(helperInvitesAvailableProvider);`.
- After the `if (unboundedAvailable) ...[ … ],` block in the second card, add:

```dart
                if (helperInvites) ...[
                  DividerSpace(),
                  AppTile(
                    tileKey: const Key('setting.get_help_tile'),
                    label: 'helpers_title'.i18n,
                    icon: AppImagePaths.handshake,
                    onPressed: () => settingMenuTap(_SettingType.getHelp),
                  ),
                ],
```

- In `settingMenuTap`'s switch, add:

```dart
      case _SettingType.getHelp:
        appRouter.push(GetHelp());
        break;
```

- [ ] **Step 5: Strings**

Append to `assets/locales/en.po`. The spec's copy is kept where it gives any.

```po
# Helper invites: the inviter's side. A "helper" is a friend abroad whose
# internet connection this user borrows through Lantern.
msgid "helpers_title"
msgstr "Get help from a friend"

msgid "helpers_intro"
msgstr "Invite someone you trust who lives somewhere without censorship. Lantern can connect you through their internet."

msgid "invite_someone"
msgstr "Invite someone to help me"

msgid "helpers_your_name_label"
msgstr "Your name, as your friend knows you"

msgid "helpers_your_name_hint"
msgstr "For example, Sara"

msgid "helpers_list_title"
msgstr "Your helpers"

msgid "helpers_empty"
msgstr "No one is helping you yet."

msgid "helpers_nudge_second"
msgstr "If your helper's computer goes offline, so does your connection through it. Invite a second friend to stay connected."

msgid "helper_online"
msgstr "Online"

msgid "helper_offline"
msgstr "Offline"

msgid "helper_unnamed"
msgstr "Your helper"

# %s is the helper's name.
msgid "helper_remove_confirm_title"
msgstr "Remove %s?"

# %s is the helper's name.
msgid "helper_remove_confirm_body"
msgstr "%s will stop helping you. You can invite them again later."

msgid "helpers_remove_all"
msgstr "Remove all helpers"

msgid "helpers_remove_all_body"
msgstr "Every helper stops helping you, and their names are erased from this device."

msgid "invite_share_title"
msgstr "Send your invite"

msgid "invite_share_body"
msgstr "Send this link to one person you trust. It works once and expires in 48 hours."

# The text shared with the link. The first %s is the user's name; the second is the link.
msgid "invite_share_message"
msgstr "%s wants your help staying online. %s"

msgid "invite_share_button"
msgstr "Share link"

msgid "invite_copy_link"
msgstr "Copy link"

msgid "invite_link_copied"
msgstr "Link copied"

msgid "invite_qr_hint"
msgstr "Your friend can scan this with Lantern."

msgid "invite_waiting"
msgstr "Waiting for your friend to open the link"

# %s is a number of hours.
msgid "invite_expires"
msgstr "Expires in %s hours"

msgid "invite_forget"
msgstr "Cancel invite"

msgid "invite_gone"
msgstr "This invite has been used or has expired."

msgid "invite_claimed_title"
msgstr "Someone opened your invite"

msgid "invite_compare_codes"
msgstr "Compare codes"

# %s is the name the person typed.
msgid "approve_code_intro"
msgstr "Ask %s which code their Lantern shows. Compare it on a call or in a different app, not in the chat you sent the link in."

msgid "approve_code_label"
msgstr "Code on this device"

msgid "approve_button"
msgstr "Codes match: approve"

msgid "reject_button"
msgstr "Codes don't match: reject"

msgid "reject_explainer"
msgstr "If the codes are different, someone else opened your link. Reject them and send a new invite."
```

- [ ] **Step 6: Generate, test, commit**

```bash
make gen
flutter analyze
flutter test test/features/helpers/
make find-duplicate-translations
(cd android && ./gradlew :app:compileDebugKotlin)
```

Expected:
- `router.gr.dart` gains `GetHelp` and `InviteShare`.
- The Kotlin compiles with `copySensitiveText`.
- Analyze is clean.
- The tests pass.
- No duplicates.

```bash
git add lib/features/helpers lib/core/router/router.dart lib/core/router/router.gr.dart lib/features/setting/setting.dart \
  lib/lantern/lantern_platform_service.dart android/app/src/main/kotlin/org/getlantern/lantern/handler/MethodHandler.kt \
  assets/locales/en.po test/features/helpers/inviter_screens_test.dart
git commit -m "app: invite a helper, share the link or QR, compare codes, approve, manage helpers"
```

---

### Task 14: lantern — the volunteer's side: links, claim, "You're helping", auto-share

**Repo / worktree:** lantern, `~/go/src/github.com/getlantern/lantern-helper-app`

**Files:**
- Create: `lib/features/helpers/help_friend.dart`
- Create: `lib/features/helpers/helping_section.dart`
- Modify: `lib/features/action_mode/provider/share_notifier.dart` (`_toggle` and `autoStart`, plus new `_startAuto`, `_autoStartWith` and `startForHelper`)
- Modify: `lib/features/action_mode/action_mode.dart` (`ActionModeTab`)
- Modify: `lib/features/home/home.dart` (`Home.build`)
- Modify: `lib/lantern_app.dart` (`_handleDeepLinkUri`, `_onResume`)
- Modify: `lib/core/router/router.dart` (`/help-friend`), and regenerate `router.gr.dart`
- Modify: `android/app/src/main/AndroidManifest.xml`, `android/app/build.gradle` (`helperLinksEnabled`)
- Modify: `assets/locales/en.po`
- Create: `test/features/helpers/volunteer_screens_test.dart`
- Modify: `test/features/action_mode/share_notifier_test.dart`

**Interfaces:**
- Consumes:
  - Task 12's `helpersProvider`, with `claim`, `endPairing`, `loadHelpOthers`, `setHelpOthers` and `refreshIfUsed`, and `HelpersState.helpingApprovals`, `helperInvitesAvailableProvider`, `helperErrorText` and `HelpersView.helping`/`.activity`
  - `ShareNotifier.ensureConsent` and `_start`
  - `QrCodeScanner`, which returns the scanned string through `appRouter.pop`
  - `int.siBytes`
- Produces:
  - The route `HelpFriend({bool fromLink = false})` (`/help-friend`). A deep link's secret goes through `HelpersNotifier.holdLink` and `takeLink` (Task 12), never through route arguments, as Task 13 does for invites
  - `class HelpingSection extends HookConsumerWidget`
  - `Future<void> ShareNotifier.startForHelper(WidgetRef widgetRef)`
  - `(int, int) helpingHoursMinutes(int seconds)` and `String helpingDuration(int seconds)`
  - Deep links: `https://lantern.io/help#…`, `https://lantern.io/help/#…`, and `<APP_AUTH_SCHEME>://help#…`

**The flow the spec asks for:**
- The link, a scanned QR or a pasted link opens "Help a friend stay online". Before approval the copy says "A friend asked for your help" (open question 2).
- The volunteer types the name the friend knows them by and accepts the sharing consent. They then see the four-digit code to read to the friend.
- If the invite was already claimed, a prominent "This invite was already claimed" explains what to tell the friend.
- On approval, Home starts sharing automatically, picking the mode as the toggle does (direct where a port opens, Unbounded otherwise; "Mode" in the spec).
- The Unbounded tab shows "You're helping", with each person's name and since-date, today's time active and data relayed, a warning if sharing is off, and a "Help others too" switch with one sentence on the allowlist.
- Each row can stop helping that person.
- A first-time volunteer abroad has no flag and is not yet helping, so the app shows them no in-app scanner entry. In person, they scan Sara's QR code with the phone's camera app, which opens the `lantern.io/help` link like any other. This is accepted for the pilot. Once they help someone, "Help a friend" (with the scanner) appears in the Unbounded tab.

- [ ] **Step 1: Write the failing tests**

`test/features/helpers/volunteer_screens_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
// common.dart re-exports the generated route classes, which share the
// screens' names.
import 'package:lantern/core/common/common.dart' hide HelpFriend;
import 'package:lantern/core/models/share_state.dart';
import 'package:lantern/features/action_mode/provider/share_notifier.dart';
import 'package:lantern/features/helpers/help_friend.dart';
import 'package:lantern/features/helpers/helping_section.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

class _Helpers extends HelpersNotifier {
  _Helpers(this.initial, {this.claimAnswer = '{"claim_id":"c1","code":"2562"}'});
  final HelpersState initial;
  final String claimAnswer;
  final claims = <(String, String)>[];
  final helpOthers = <bool>[];

  @override
  HelpersState build() => initial;
  @override
  Future<void> loadHelpOthers() async {}
  @override
  Future<void> setHelpOthers(bool value) async => helpOthers.add(value);
  @override
  Future<HelperResult> claim(String link, String name) async {
    claims.add((link, name));
    return HelperResult.parse(claimAnswer);
  }
}

class _Share extends ShareNotifier {
  _Share({this.active = false});
  final bool active;
  @override
  ShareState build() => ShareState(active: active);
  @override
  Future<bool> ensureConsent(BuildContext context) async => true;
}

Future<void> _pump(WidgetTester tester, _Helpers helpers, Widget child, {bool sharing = false}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        helpersProvider.overrideWith(() => helpers),
        shareProvider.overrideWith(() => _Share(active: sharing)),
      ],
      child: ScreenUtilInit(
        designSize: const Size(393, 852),
        child: MaterialApp(theme: AppTheme.appTheme(), home: Scaffold(body: child)),
      ),
    ),
  );
  await tester.pump();
}

const _link = 'https://lantern.io/help/#AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

HelpersState _helping() => const HelpersState(
  helpOthers: true,
  view: HelpersView(
    unlocked: true,
    helping: [HelperPairingView(pairingId: 'p1', name: 'Sara', sinceUnix: 1700000000)],
    activity: HelperActivity(day: '2026-10-23', activeSeconds: 3 * 3600 + 20 * 60, relayedBytes: 1200000000),
  ),
);

void main() {
  testWidgets('claiming shows the code', (tester) async {
    final helpers = _Helpers(const HelpersState());
    helpers.holdLink(_link);
    await _pump(tester, helpers, const HelpFriend(fromLink: true));
    await tester.enterText(find.byKey(const Key('help_friend.name')), 'Ali');
    await tester.tap(find.byKey(const Key('help_friend.accept')));
    await tester.pump();
    await tester.pump(); // pumpAndSettle would never settle: the waiting view spins
    expect(helpers.claims, [(_link, 'Ali')]);
    expect(find.text('2562'), findsOneWidget);
  });

  testWidgets('an already claimed invite says so prominently', (tester) async {
    final helpers = _Helpers(const HelpersState(), claimAnswer: '{"error":"already_claimed"}');
    helpers.holdLink(_link);
    await _pump(tester, helpers, const HelpFriend(fromLink: true));
    await tester.enterText(find.byKey(const Key('help_friend.name')), 'Ali');
    await tester.tap(find.byKey(const Key('help_friend.accept')));
    await tester.pump();
    await tester.pump(); // pumpAndSettle would never settle: the waiting view spins
    expect(find.byKey(const Key('help_friend.already_claimed')), findsOneWidget);
  });

  testWidgets("you're helping: rows, totals, and help others too", (tester) async {
    final helpers = _Helpers(_helping());
    await _pump(tester, helpers, const SingleChildScrollView(child: HelpingSection()), sharing: true);
    expect(find.text('Sara'), findsOneWidget);
    expect(find.byKey(const Key('helping.today')), findsOneWidget);
    expect(find.byKey(const Key('helping.sharing_off')), findsNothing);
    await tester.tap(find.byKey(const Key('helping.help_others_switch')));
    await tester.pump();
    expect(helpers.helpOthers, [false]);
  });

  testWidgets('warns when sharing is off', (tester) async {
    await _pump(tester, _Helpers(_helping()), const SingleChildScrollView(child: HelpingSection()));
    expect(find.byKey(const Key('helping.sharing_off')), findsOneWidget);
  });

  testWidgets('hidden for someone helping nobody', (tester) async {
    await _pump(tester, _Helpers(const HelpersState()), const HelpingSection());
    expect(find.byKey(const Key('helping.today')), findsNothing);
  });

  test('helpingHoursMinutes', () {
    expect(helpingHoursMinutes(3 * 3600 + 20 * 60), (3, 20));
    expect(helpingHoursMinutes(3600), (1, 0));
    expect(helpingHoursMinutes(59), (0, 0));
    expect(helpingHoursMinutes(0), (0, 0));
  });
}
```

Append to `test/features/action_mode/share_notifier_test.dart`, inside `main`:

```dart
  test('startForHelper does nothing without the sharing consent', () async {
    final notifier = _notifier();
    await notifier.startForHelper(_NoRef());
    expect(notifier.state.active, isFalse);
    expect(notifier.state.probing, isFalse);
  });
```

and, at file level:

```dart
class _NoRef implements WidgetRef {
  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw StateError('startForHelper must not touch the ref without consent');
}
```

Run: `flutter test test/features/helpers/volunteer_screens_test.dart test/features/action_mode/share_notifier_test.dart`
Expected: compilation fails, because `help_friend.dart`, `helping_section.dart` and `startForHelper` do not exist.

- [ ] **Step 2: `startForHelper` in the share notifier**

In `lib/features/action_mode/provider/share_notifier.dart`, in `_toggle`, replace everything from `// The iOS network extension cannot carry …` to the end of the method with:

```dart
    await _startAuto(widgetRef);
  }

  /// Picks the sharing mode from the network and starts it: direct (SmC)
  /// where a manual port or UPnP gateway allows, Unbounded otherwise. The
  /// spec's "Mode": direct whenever it works, with no picker.
  Future<void> _startAuto(WidgetRef widgetRef) async {
    // The iOS network extension cannot carry the peer proxy's second sing-box
    // instance, so the backend refuses SmC there.
    if (PlatformUtils.isIOS) {
      await _start(widgetRef, ShareMode.unbounded);
      return;
    }

    // A manually forwarded port is an explicit request for SmC.
    final manualPortRes = await widgetRef
        .read(lanternServiceProvider)
        .getPeerManualPort();
    if (!ref.mounted) return;
    if (manualPortRes.fold((_) => 0, (p) => p) > 0) {
      await _start(widgetRef, ShareMode.smc);
      return;
    }

    // Blocks up to ~6s on IGD discovery; any failure counts as unavailable.
    final probeRes = await widgetRef.read(lanternServiceProvider).probeUPnP();
    if (!ref.mounted) return;
    final upnpAvailable = probeRes.fold((_) => false, (v) => v);
    await _start(
      widgetRef,
      upnpAvailable ? ShareMode.smc : ShareMode.unbounded,
    );
  }

  /// Starts sharing for a newly approved helper pairing, as the toggle would.
  /// The consent was collected when the user accepted the invite; without it
  /// this does nothing. A user who later turns sharing off stays off.
  Future<void> startForHelper(WidgetRef widgetRef) =>
      _autoStartWith(() => _startAuto(widgetRef));

  /// The no-UI start shared by autoStart and startForHelper: skip while a
  /// change is in flight or sharing is already up, and never start without
  /// the consent ack.
  Future<void> _autoStartWith(Future<void> Function() start) async {
    if (_changing || state.active || state.probing) return;
    if (!_consentAcked) return;
    _changes++;
    try {
      state = state.copyWith(probing: true);
      await start();
    } finally {
      _changes--;
    }
  }
```

Replace the body of the existing `autoStart` so the two share one guard:

```dart
  Future<void> autoStart(WidgetRef widgetRef) =>
      _autoStartWith(() => _start(widgetRef, ShareMode.unbounded));
```

Keep `autoStart`'s doc comment. The lines removed from `_toggle` are exactly the body of `_startAuto`, so `toggle` behaves as before, and `autoStart` keeps its guards and its Unbounded-only start.

- [ ] **Step 3: The claim screen**

`lib/features/helpers/help_friend.dart`:

```dart
import 'package:auto_route/auto_route.dart';
import 'package:flutter/material.dart';
import 'package:flutter_hooks/flutter_hooks.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/action_mode/provider/share_notifier.dart';
import 'package:lantern/features/helpers/helper_errors.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// A volunteer accepts an invite: from a link that opened the app, a scanned
/// QR code, or a pasted link. It shows the code to read to the friend, then
/// waits for approval. The link is a secret: it is never logged or shown.
@RoutePage(name: 'HelpFriend')
class HelpFriend extends HookConsumerWidget {
  const HelpFriend({super.key, this.fromLink = false});

  /// Whether a deep link opened this screen. Its secret is held by the
  /// notifier (holdLink), never passed in route arguments.
  final bool fromLink;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final textTheme = Theme.of(context).textTheme;
    final helpers = ref.watch(helpersProvider);
    final link = useMemoized(() => fromLink ? ref.read(helpersProvider.notifier).takeLink() : null);
    final linkCtl = useTextEditingController(text: link ?? '');
    final nameCtl = useTextEditingController(text: helpers.view.myName);
    final code = useState<String?>(null);
    final approvalsAtClaim = useState<int?>(null);
    final error = useState<String?>(null);
    final busy = useState(false);

    if (!AppBuildInfo.enableHelperInvites) {
      return BaseScreen(
        title: 'help_friend_title'.i18n,
        body: Center(child: Text('helper_error_disabled'.i18n)),
      );
    }

    Future<void> submit() async {
      error.value = null;
      busy.value = true;
      final consented = await ref.read(shareProvider.notifier).ensureConsent(context);
      if (!context.mounted) return;
      if (!consented) {
        busy.value = false;
        return;
      }
      final r = await ref.read(helpersProvider.notifier).claim(linkCtl.text, nameCtl.text);
      if (!context.mounted) return;
      busy.value = false;
      if (!r.ok) {
        error.value = r.error;
        return;
      }
      approvalsAtClaim.value = ref.read(helpersProvider).helpingApprovals;
      code.value = r.data['code'] as String?;
    }

    final approved =
        approvalsAtClaim.value != null && helpers.helpingApprovals > approvalsAtClaim.value!;

    final Widget body;
    if (approved) {
      final helping = helpers.view.helping;
      final name = helping.isEmpty || helping.last.name.isEmpty ? 'helping_unnamed'.i18n : helping.last.name;
      body = Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: defaultSize),
          Text('help_friend_approved'.i18n.fill([name]), key: const Key('help_friend.approved'), style: textTheme.titleLarge),
          const SizedBox(height: defaultSize),
          PrimaryButton(label: 'help_friend_done'.i18n, onPressed: () => appRouter.maybePop()),
        ],
      );
    } else if (code.value != null) {
      body = Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: defaultSize),
          Text('help_friend_code_title'.i18n, style: textTheme.titleLarge),
          const SizedBox(height: 12),
          Text(
            code.value!,
            key: const Key('help_friend.code'),
            textAlign: TextAlign.center,
            style: textTheme.displayMedium?.copyWith(letterSpacing: 8),
          ),
          const SizedBox(height: 12),
          Text('help_friend_code_body'.i18n, style: textTheme.bodyMedium),
          const SizedBox(height: 24),
          Row(
            children: [
              const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
              const SizedBox(width: 8),
              Expanded(child: Text('help_friend_waiting'.i18n)),
            ],
          ),
        ],
      );
    } else if (error.value == 'already_claimed') {
      body = Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: defaultSize),
          Icon(Icons.warning_amber_rounded, size: 48, color: Theme.of(context).colorScheme.error),
          const SizedBox(height: 12),
          Text(
            'help_friend_already_claimed_title'.i18n,
            key: const Key('help_friend.already_claimed'),
            style: textTheme.titleLarge,
          ),
          const SizedBox(height: 12),
          Text('help_friend_already_claimed_body'.i18n, style: textTheme.bodyMedium),
        ],
      );
    } else {
      body = ListView(
        padding: const EdgeInsets.symmetric(vertical: defaultSize),
        children: [
          Text('help_friend_intro'.i18n, style: textTheme.bodyMedium),
          const SizedBox(height: defaultSize),
          if (link == null) ...[
            AppTextField(
              controller: linkCtl,
              label: 'help_friend_link_label'.i18n,
              hintText: 'help_friend_paste'.i18n,
            ),
            if (AppBuildInfo.enableQrScanner && PlatformUtils.isMobile)
              AppTextButton(
                label: 'help_friend_scan'.i18n,
                onPressed: () async {
                  final scanned = await appRouter.push(QrCodeScanner());
                  if (scanned is String) linkCtl.text = scanned;
                },
              ),
            const SizedBox(height: defaultSize),
          ],
          AppTextField(
            fieldKey: const Key('help_friend.name'),
            controller: nameCtl,
            label: 'helpers_your_name_label'.i18n,
            hintText: 'helpers_your_name_hint'.i18n,
            maxLength: 60,
          ),
          const SizedBox(height: 8),
          Text('help_friend_sharing_note'.i18n, style: textTheme.bodySmall),
          if (error.value != null) ...[
            const SizedBox(height: 8),
            Text(
              helperErrorText(error.value!),
              key: const Key('help_friend.error'),
              style: textTheme.bodyMedium!.copyWith(color: Theme.of(context).colorScheme.error),
            ),
          ],
          const SizedBox(height: defaultSize),
          KeyedSubtree(
            key: const Key('help_friend.accept'),
            child: PrimaryButton(label: 'help_friend_accept'.i18n, enabled: !busy.value, onPressed: submit),
          ),
        ],
      );
    }
    return BaseScreen(title: 'help_friend_title'.i18n, body: body);
  }
}
```

`AppTextField.fieldKey` puts the key on its `TextFormField`, which `enterText` needs.

- [ ] **Step 4: "You're helping"**

`lib/features/helpers/helping_section.dart`:

```dart
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_hooks/flutter_hooks.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:intl/intl.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/core/extensions/int.dart';
import 'package:lantern/features/action_mode/provider/share_notifier.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// "You're helping": one row per person this device helps, by the name their
/// app sent, with today's totals and "Help others too". It shows no live
/// status for the people helped: that would expose their activity to anyone
/// holding this device. Today's totals cover all sharing, paired and pool
/// together, until per-person attribution exists (open question 1).
class HelpingSection extends HookConsumerWidget {
  const HelpingSection({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final helpers = ref.watch(helpersProvider);
    final sharing = ref.watch(shareProvider.select((s) => s.active || s.probing));
    final notifier = ref.read(helpersProvider.notifier);
    final textTheme = Theme.of(context).textTheme;

    useEffect(() {
      unawaited(notifier.loadHelpOthers());
      return null;
    }, const []);

    final helping = helpers.view.helping;
    if (helping.isEmpty) return const SizedBox.shrink();
    final activity = helpers.view.activity;

    return Padding(
      padding: const EdgeInsets.only(top: defaultSize),
      child: AppCard(
        padding: EdgeInsets.zero,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
              child: Text('helping_title'.i18n, style: textTheme.titleMedium),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: Text(
                'helping_today'.i18n.fill([
                  helpingDuration(activity?.activeSeconds ?? 0),
                  (activity?.relayedBytes ?? 0).siBytes,
                ]),
                key: const Key('helping.today'),
                style: textTheme.labelMedium?.copyWith(color: context.textTertiary),
              ),
            ),
            if (!sharing)
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
                child: Text(
                  'helping_sharing_off_warning'.i18n,
                  key: const Key('helping.sharing_off'),
                  style: textTheme.bodyMedium!.copyWith(color: Theme.of(context).colorScheme.error),
                ),
              ),
            for (final p in helping)
              AppTile(
                label: p.name.isEmpty ? 'helping_unnamed'.i18n : p.name,
                subtitle: p.sinceUnix > 0 ? Text('helping_since'.i18n.fill([_date(p.sinceUnix)])) : null,
                trailing: IconButton(
                  tooltip: 'helping_remove'.i18n,
                  icon: const Icon(Icons.close),
                  onPressed: () => _confirmStop(context, ref, p),
                ),
              ),
            DividerSpace(),
            AppTile(
              label: 'help_others_too'.i18n,
              subtitle: Text(
                'help_others_too_body'.i18n,
                style: textTheme.labelMedium?.copyWith(color: context.textTertiary),
              ),
              trailing: Switch(
                key: const Key('helping.help_others_switch'),
                value: helpers.helpOthers,
                onChanged: notifier.setHelpOthers,
              ),
              onPressed: () => notifier.setHelpOthers(!helpers.helpOthers),
            ),
            // Changing it rebuilds the sharing session (Task 7), so the people
            // helped reconnect after a few seconds.
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
              child: Text(
                'help_others_too_note'.i18n,
                style: textTheme.labelSmall?.copyWith(color: context.textTertiary),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _confirmStop(BuildContext context, WidgetRef ref, HelperPairingView p) {
    final name = p.name.isEmpty ? 'helping_unnamed'.i18n : p.name;
    return AppDialog.show(
      context: context,
      title: 'helping_remove'.i18n,
      body: 'helping_remove_confirm'.i18n.fill([name]),
      primaryLabel: 'helping_remove'.i18n,
      onPrimaryPressed: () => ref.read(helpersProvider.notifier).endPairing(p.pairingId),
      secondaryLabel: 'cancel'.i18n,
    );
  }
}

/// Whole hours and the minutes left over.
(int, int) helpingHoursMinutes(int seconds) => (seconds ~/ 3600, (seconds % 3600) ~/ 60);

/// "3h 20m" or "45m", localized.
String helpingDuration(int seconds) {
  final (h, m) = helpingHoursMinutes(seconds);
  return h > 0 ? 'helping_duration_hm'.i18n.fill([h, m]) : 'helping_duration_m'.i18n.fill([m]);
}

String _date(int unix) => DateFormat.yMMMd().format(DateTime.fromMillisecondsSinceEpoch(unix * 1000));
```

- [ ] **Step 5: Wire the tab, Home, deep links and the route**

In `lib/features/action_mode/action_mode.dart`, add the imports `package:lantern/features/helpers/helping_section.dart` and `package:lantern/features/helpers/provider/helpers_notifier.dart`. In `ActionModeTab.build`, add:

```dart
    final helperInvites = ref.watch(helperInvitesAvailableProvider);
    final helping = ref.watch(helpersProvider.select((s) => s.view.helping.isNotEmpty));
```

In the `Column`'s `children`, after the `_StatusCard(…)`, add the following. "You're helping" needs no flag: a volunteer abroad is never flagged, so that nobody in their country sees the inviter UI (Task 1).

```dart
                    if (AppBuildInfo.enableHelperInvites) const HelpingSection(),
                    if (AppBuildInfo.enableHelperInvites && (helperInvites || helping)) ...[
                      const SizedBox(height: defaultSize),
                      AppTextButton(
                        label: 'help_friend_entry'.i18n,
                        onPressed: () => appRouter.push(HelpFriend()),
                      ),
                    ],
```

In `lib/features/home/home.dart`, add the import `package:lantern/features/helpers/provider/helpers_notifier.dart`. In `Home.build`, after the `ref.listen<VPNStatus>(vpnProvider, …);` block, add:

```dart
    // A device that has used helper invites refreshes them at launch, so a
    // volunteer approved while the app was closed starts helping now. Not
    // behind the helper_invites flag: volunteers abroad are not flagged.
    useEffect(() {
      if (!AppBuildInfo.enableHelperInvites) return null;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (context.mounted) ref.read(helpersProvider.notifier).refreshIfUsed();
      });
      return null;
    }, const []);

    // A newly approved pairing this device helps: start sharing, picking the
    // mode as the toggle does. Not gated on the Unbounded tab being visible:
    // a paired volunteer's widget is turned on server-side (Features[unbounded]).
    ref.listen<int>(helpersProvider.select((s) => s.helpingApprovals), (prev, next) {
      if (next <= (prev ?? 0)) return;
      ref.read(shareProvider.notifier).startForHelper(ref);
    });
```

In `lib/lantern_app.dart`:
- Add `import 'package:lantern/features/helpers/provider/helpers_notifier.dart';`.
- In `_onResume`, add `unawaited(ref.read(helpersProvider.notifier).refreshIfUsed());`.
- In `_handleDeepLinkUri`, before `if (path.startsWith('/affiliate') ||`, insert the following branch, and turn the existing `if` into `else if`:

```dart
    if (_isHelperInviteUri(uri)) {
      // The invite secret is the fragment: never log it.
      appLogger.debug('DeepLink helper invite');
      // Held by the notifier, not put in route arguments.
      ref.read(helpersProvider.notifier).holdLink('https://lantern.io/help/#${uri.fragment}');
      _pushWithHome(HelpFriend(fromLink: true));
      return;
    }
```

Add to `_LanternAppState`:

```dart
  bool _isHelperInviteUri(Uri uri) {
    if (AppBuildInfo.isAppAuthUri(uri) && uri.host == 'help') return true;
    return uri.scheme == 'https' &&
        (uri.host == 'lantern.io' || uri.host == 'www.lantern.io') &&
        (uri.path == '/help' || uri.path == '/help/');
  }
```

In `lib/core/router/router.dart`, after the `/invite-share` route:

```dart
    AutoRoute(
      path: '/help-friend',
      page: HelpFriend.page,
    ),
```

In `android/app/build.gradle`, add to `manifestPlaceholders`, after `appAuthScheme: androidIdentity.appAuthScheme,`:

```groovy
                // Helper invite links open the app only in normal builds;
                // stealth artifacts must not claim lantern.io/help.
                helperLinksEnabled: stealthMode ? "false" : "true",
```

In `android/app/src/main/AndroidManifest.xml`, after the `</activity>` that closes `MainActivity`, add an alias that carries the `/help` filters. An intent filter cannot be disabled on its own, but an activity alias can, so a stealth build ships the filters disabled:

```xml
        <!-- Helper invite links (lantern.io/help#…). Disabled in stealth builds
             through the helperLinksEnabled placeholder. -->
        <activity-alias
            android:name=".HelperInviteLinks"
            android:targetActivity=".MainActivity"
            android:enabled="${helperLinksEnabled}"
            android:exported="true">
            <intent-filter android:autoVerify="true">
                <action android:name="android.intent.action.VIEW" />
                <category android:name="android.intent.category.DEFAULT" />
                <category android:name="android.intent.category.BROWSABLE" />
                <data android:scheme="https" android:host="lantern.io" android:path="/help" />
                <data android:scheme="https" android:host="lantern.io" android:pathPrefix="/help/" />
            </intent-filter>
            <intent-filter>
                <action android:name="android.intent.action.VIEW" />
                <category android:name="android.intent.category.DEFAULT" />
                <category android:name="android.intent.category.BROWSABLE" />
                <data android:scheme="${appAuthScheme}" android:host="help" />
            </intent-filter>
        </activity-alias>
```

The alias opens `MainActivity` with the original intent, so app_links delivers the URI as for the other links. To check the stealth gate:

```bash
(cd android && ./gradlew :app:processDebugMainManifest -PSTEALTH_MODE=stealth-vpn)
grep -rl --include=AndroidManifest.xml HelperInviteLinks build/app/intermediates | xargs grep -A3 HelperInviteLinks
```

Expect `android:enabled="false"`. Without `-PSTEALTH_MODE`, expect `"true"`. `STEALTH_MODE` is the Gradle property `android/app/build.gradle` reads.

iOS and macOS already claim `applinks:lantern.io`. Which paths open the app is set on lantern.io (Task 15).

- [ ] **Step 6: Strings**

Append to `assets/locales/en.po`:

```po
# Helper invites: the volunteer's side.
msgid "help_friend_title"
msgstr "Help a friend stay online"

msgid "help_friend_entry"
msgstr "Help a friend"

msgid "help_friend_intro"
msgstr "A friend asked for your help. While Lantern runs, they can reach the open internet through your connection."

msgid "help_friend_link_label"
msgstr "Invite link"

msgid "help_friend_paste"
msgstr "Paste the link your friend sent"

msgid "help_friend_scan"
msgstr "Scan your friend's QR code"

msgid "help_friend_sharing_note"
msgstr "Lantern helps automatically while it's running. It uses a direct connection when your router allows one, and a browser-style connection otherwise."

msgid "help_friend_accept"
msgstr "Help my friend"

msgid "help_friend_code_title"
msgstr "Tell your friend this code"

msgid "help_friend_code_body"
msgstr "Your friend will check that it matches the code on their phone, then approve you. A call or a different app is safest."

msgid "help_friend_waiting"
msgstr "Waiting for your friend to approve"

# %s is the friend's name.
msgid "help_friend_approved"
msgstr "You're now helping %s."

msgid "help_friend_done"
msgstr "Done"

msgid "help_friend_already_claimed_title"
msgstr "This invite was already claimed"

msgid "help_friend_already_claimed_body"
msgstr "Someone else opened this link first, or it expired. Ask your friend for a new invite, and tell them this one may have been seen by someone else."

msgid "help_friend_bad_link"
msgstr "This isn't a helper invite link."

msgid "help_friend_own_invite"
msgstr "This is your own invite. Send it to the friend you'd like help from."

msgid "helping_title"
msgstr "You're helping"

# First %s is a duration such as "3h 20m"; second is an amount of data such as "1.2 GB".
msgid "helping_today"
msgstr "Today: active %s, %s shared"

# %s is a date.
msgid "helping_since"
msgstr "Since %s"

msgid "helping_unnamed"
msgstr "Someone you help"

msgid "helping_sharing_off_warning"
msgstr "Sharing is off, so the people you help can't connect through you."

msgid "helping_remove"
msgstr "Stop helping"

# %s is the person's name.
msgid "helping_remove_confirm"
msgstr "Stop helping %s? They'll need a new invite to connect through you again."

msgid "help_others_too"
msgstr "Help others too"

msgid "help_others_too_body"
msgstr "Lend spare capacity to other people facing censorship; Lantern only lets them reach allowed sites, which protects your connection from misuse."

msgid "help_others_too_note"
msgstr "Changing this briefly reconnects the people you help."

# %s %s are hours and minutes, as numbers.
msgid "helping_duration_hm"
msgstr "%sh %sm"

# %s is minutes, as a number.
msgid "helping_duration_m"
msgstr "%sm"
```

- [ ] **Step 7: Generate, test, analyze, commit**

```bash
make gen
flutter analyze
flutter test test/features/helpers/ test/features/action_mode/
make find-duplicate-translations
```

Expected:
- `router.gr.dart` gains `HelpFriend`.
- Analyze is clean.
- All tests pass, including the existing action-mode tests: `toggle` is unchanged in behaviour.
- No duplicates.

```bash
git add lib/features/helpers lib/features/action_mode lib/features/home/home.dart lib/lantern_app.dart \
  lib/core/router/router.dart lib/core/router/router.gr.dart android/app/src/main/AndroidManifest.xml android/app/build.gradle \
  assets/locales/en.po test/features/helpers/volunteer_screens_test.dart test/features/action_mode/share_notifier_test.dart
git commit -m "app: accept a helper invite, show the code, help automatically, You're helping"
```

- [ ] **Step 8: Full checks and the PR**

```bash
CGO_ENABLED=1 go test -tags with_gvisor,with_quic,with_wireguard,with_utls,with_grpc ./lantern-core/...
flutter analyze && flutter test
make macos-unit-tests && make ios-compile-check
```

Telemetry audit:

```bash
git diff origin/main -- lib lantern-core ':!*.g.dart' ':!*.gr.dart' ':!*generated*' ':!*_test.*' | grep -nE '^\+.*(appLogger|slog\.)'
```

Expected:
- `'helperCall failed'` twice, with the exception only
- `'helper: the OS keystore is unavailable'`
- `'helper: setting help-others-too failed: ${f.error}'`, an IPC error with no helper data
- `'DeepLink helper invite'`

No link, name, code or pairing ID appears.

Open the PR for Tasks 9–14 with `skills:create-pr`, base `main`. In the PR body, list the [Human] steps of Task 15 that must come first, the radiance and broflake pseudo-versions, and the new native dependency.

---

### Task 15: Rollout [Human]

Every step here is a production action. Do them in this order.

**Already satisfied:** the helper ticket seed is in Vault (`secret/data/lantern_cloud/helper_tickets`), with its Vault policy, in production as of 2026-10-23. Tickets, freddie rooms and the volunteer accepted sets have the key they need, so this is not a gate.

**Gates from plan 3, before Task 6:** lantern-box #380 merged and tagged; plan 3's radiance PR on that tag and merged (Task 6 Step 1).

- [ ] **Step 1: [Human] lantern-cloud.** Merge the Tasks 1–3 PR, apply migrations 000149 and 000150, deploy the API, and check CORS (Task 3 Step 5). This is safe before any client ships:
  - `Features["helper_invites"]` is false wherever the flag is off.
  - The paired-volunteer widget needs an existing pairing, and pairings exist only behind the flag.
- [ ] **Step 2: [Human] lantern.io.**
  - Add `/help` and `/help/*` to the `applinks` paths in `https://lantern.io/.well-known/apple-app-site-association`, for both the iOS and the macOS app IDs.
  - Confirm `https://lantern.io/.well-known/assetlinks.json` lists the Play and sideload signing certificates. It is domain-wide, so no path change is needed.
  - Until plan 5's page exists on `https://help.lantern.io`, `https://lantern.io/help/` must at least serve a page that sends the fragment nowhere, for desktop browsers and for phones without the app. Once that page exists, `lantern.io/help/` redirects to it; the fragment survives the redirect.
  - Confirm `lantern.io/help` loads no analytics or third-party script that could read `location.hash`.
- [ ] **Step 3: [Human] Merge the unbounded and radiance PRs** (Tasks 4–5 and 6–8). Record `UB_SHA` and `RAD_SHA`. No release is needed: both reach users only through the app.
- [ ] **Step 4: [Human] Merge the lantern PR** (Tasks 9–14), after Steps 1–3.
- [ ] **Step 5: [Human] Smoke-test the keystore on every platform with a debug build.**
  - On macOS, Windows, Linux (GNOME with a keyring, and a desktop without one), Android and iOS: open Settings → "Get help from a friend", create an invite, and quit and relaunch.
  - Expect: the invite is still listed; no Keychain prompt appears on macOS; and on Linux without a keyring, the `helper_error_keystore` text appears.
- [ ] **Step 6: [Human] Release the app.**
  - Cut a lantern release with this PR.
  - Submit to Google Play and the App Store. The App Store review notes should explain the camera use (the QR scanner already exists) and the new associated-domain paths.
  - Notarize and publish the desktop builds.
  - Rebuild the Linux packages with the new `libsecret` dependency.
- [ ] **Step 7: [Human] Turn on the pilot flag, for inviters only.**
  - Set `helper_invites` for the pilot's inviters in Iran. **Do not** flag volunteers' countries: a volunteer needs no flag (Task 1), and flagging their countries would show everyone there the inviter UI.
  - Start with device-scoped overrides (`X-Lantern-Feature-Override: helper_invites`, through the developer settings' feature overrides) on test devices, then widen.
  - `unbounded` does not need to be on for the volunteers: Task 1 turns the widget on for paired volunteers.
- [ ] **Step 8: [Human] The pilot checks from the spec's "Production pilot checks".**
  - **A deliberately leaked link, claimed first, is caught by the code comparison.**
    - Device A creates an invite.
    - Device B claims it and shows a code.
    - Device C claims the same link and gets "This invite was already claimed".
    - A's sheet shows B's code. B's and C's codes cannot both match it.
    - A rejects, and the link is dead.
  - **Revocation time is measured.** Approve a pairing with the volunteer sharing in direct mode, then end it from the inviter. Measure until the volunteer's samizdat inbound refuses the inviter, which should be at most one heartbeat (150 s). Repeat over Unbounded, which should be at most one ticket refresh (10 minutes) plus `roomSyncInterval`.
  - **Auto-share.** On a fresh volunteer device that has never shared, accept an invite. After approval, sharing starts by itself in direct mode where a port opens, and in Unbounded otherwise. Advanced stats shows the mode.
  - **Exports.** On the inviter, connect through a helper. Export logs from the Logs screen and file a report. Search both for `helper-`: there must be no hits.

---

## Self-review

### Spec coverage

| Spec requirement | Where | Tasks |
|---|---|---|
| "Sara taps 'Invite someone to help me'. Her app generates a 32-byte random secret" | `create_invite` → `helperinvite.NewSecret` | 4, 9, 13 |
| Registers `H(s)` and the approval public key; 48-hour expiry | `CreateInvite` with `invite_hash` and `approval_pubkey`; `InviteTTL` | 4, 9 |
| Link `lantern.io/help/#<base64url(s)>`, secret in the fragment | `helperinvite.Link` and `ParseLink`; the deep-link handler never logs the fragment | 4, 14 |
| Share sheet or QR; "Sara wants your help staying online." | `InviteShare`: `SharePlus`, `QrImageView`, `invite_share_message` | 13 |
| Lantern installed: the link opens the app | Android intent filter, iOS and macOS applinks (AASA in Task 15), the custom scheme `help` | 14, 15 |
| Not installed: landing page, install, "Continue", no deferred deep-link services | Plan 5 (the page). The app accepts `<scheme>://help#s` for its "Continue" button | 14; plan 5 |
| In person: scans the QR code | `HelpFriend` → `QrCodeScanner` | 14 |
| Volunteer creates a device keypair; derives `K_v`; encrypts its name; sends the claim | `state.DeviceSeed`; `helperinvite.NewClaim` | 4, 9 |
| "already claimed" shown prominently | `CodeAlreadyClaimed` → `help_friend.already_claimed` | 9, 14 |
| Matching code, computed by both apps; suggest a call or another app | `helperinvite.MatchingCode` on both sides; `approve_code_intro`, `help_friend_code_body` | 4, 9, 13, 14 |
| Approve or Reject; the approval signature | `NewApproval` (signature plus inviter blob) → `DecideInvite` | 4, 9, 13 |
| Sara's name reaches the volunteer encrypted with `K_i`, once | The inviter blob from `ListPairings`, opened by the matching claim | 9 |
| When a link leaks: reject, burn, send a new one | Reject path; `reject_explainer` | 9, 13 |
| Either side can end a pairing | `end_pairing` on both sides | 9, 13, 14 |
| Display names stored only on the two devices, encrypted with the OS keystore | Sealed `state.bin`, a name per invite and claim; the key in flutter_secure_storage; a key mismatch keeps `state.bin.bak` and reports `keystore_reset`. Browser names are memory-only | 9, 12 |
| Limits | Server-side (plan 1). The app shows `too_many_invites` and `too_many_pairings` | 9, 12 |
| Mode: direct whenever it can, otherwise Unbounded; no picker; Advanced stats shows it | `ShareNotifier._startAuto`, used by toggle and `startForHelper`; Advanced stats unchanged | 14 |
| "You're helping" rows by name with daily totals | `HelpingSection`, with totals for the section (open question 1) | 8, 9, 14 |
| No live "Sara is online now" | Volunteers never get `online` (Task 2), and the section shows none | 2, 14 |
| "Help others too", on by default, one sentence on the allowlist | Switch → `peer_serve_pool`; direct mode re-registers at once; the widget follows within 30 s | 7, 12, 14 |
| Browser fallback scoped to the pairing's room | wasm `setRoomTickets`, `setServePool`, `lanternHelperInvite` taking the in-memory secret; CORS for `help.lantern.io` only; the protocol in Global Constraints. The page is plan 5 | 3, 5 |
| Inviter: online or offline status, revoke, invite another, nudge to a second helper | `ListPairings.online`; `GetHelp` rows, nudge, remove | 2, 13 |
| "Remove all helpers" | `end_all_helpers` | 9, 13 |
| Telemetry: outcomes and timings only | Rules in Global Constraints; an audit step in each PR task | 1–14 |
| Clients must not export `helper-` tags | Server list filter, Dart log redaction, a guard on lantern-core logs | 11 |
| Testing: shared crypto vectors | `helperinvite` against lantern-cloud's `vectors.json`, plus `names.json` | 4 |
| Testing: state machine, including two claims racing | `TestASecondClaimIsAlreadyClaimed`, `TestCancelledInviteRejectsALaterClaim` and the end-to-end engine test. The server-side race is plan 1's | 9 |
| Production pilot checks | Task 15 Step 8 | 15 |
| Rollout behind a feature flag | `Features["helper_invites"]` gates inviters. Volunteers pass the gate with an invite, claim or pairing, so the flag covers only the pilot's inviters (a spec amendment) | 1, 12, 14, 15 |
| Plan 3 deferrals: toggle IPC; dispatching it at once; invalidating tickets on approval; P13 auto-enable for paired volunteers; browser tickets and wasm exports | IPC allowlist (already on main); re-register (7); `Relayer` `onChange` → `Source.Invalidate` (6); auto-share (14) and `Features[unbounded]` (1); wasm and CORS (3, 5) | 1, 3, 5, 6, 7, 14 |
| Plan 2 deferral: the inviter's helper list with online status | Task 2 and Task 13 | 2, 13 |

### Deferred to plan 5 (landing page and rewards)

- The landing page: the human story; the install call to action; keeping `s` in the browser; "help from this browser now", running the browser protocol above with the wasm built here; and the `<meta>` preview "Sara wants your help staying online.", served without the fragment.
  - **Hard rule:** the page that runs browser volunteers is served only from `https://help.lantern.io`, a dedicated origin with a strict CSP and no third-party JavaScript, analytics or tag managers. It is the only origin in `helperCORS` (Task 3). `lantern.io/help/` redirects there.
  - "Continue in the app": on Android, use an `intent://` URL naming the package, `intent://help/<s>#Intent;scheme=lantern;package=<applicationId>;end`, so Chrome opens Lantern itself and never offers another handler. An intent URL's fragment holds the intent spec, so the secret goes in the path. Android delivers it as `lantern://help/<s>`, and plan 5 extends `_isHelperInviteUri` and the link normalization to accept that form. Elsewhere, use `lantern://help#<s>`.
- Publishing a new `widget.wasm` with Task 5's exports.
- Pro rewards: the welcome week, a week per 30 active days with the 6-hour rule, the censored-country and 3-pairing conditions, the yearly cap, eligibility from heartbeats and the config-fetch country, grants through the referral mechanism, and the "you've earned a free week, create an account to claim it" prompt and claim.
- The share card ("I'm keeping someone in Iran online with Lantern"), never naming the paired person.
- Pro offers at natural moments, never during onboarding.

### Deferred elsewhere (open questions)

- Per-person data relayed and time active: a plan 4b on attribution (open question 1).
- Account-bound pairings: their own plan (open question 3).
- Moving a browser pairing into the app (open question 7).
- A server-side "cancel invite" RPC (open question 9).
- Discreet links (open question 10).

### Placeholder and consistency check

- No step says "TBD", "add error handling" or "similar to Task N". The values not known in advance, `UB_SHA` (Task 5 Step 7) and `RAD_SHA` (Task 8 Step 7), come with the commands that produce them. The one environment-dependent instruction is the migration numbering in Tasks 1 and 2, which says exactly what to do if 000149 is taken. `LB_TAG` comes from the plan 3 gate in Task 6 Step 1.
- Names are consistent across repos:
  - broflake `helperinvite`: `NewSecret`, `InviteHash`, `DeriveKey`, `ApprovalPublic`, `ApprovalMessage`, `Approve`, `MatchingCode`, `VolunteerNameKey`, `InviterNameKey`, `DeviceKey`, `CleanName`, `SealName`, `OpenName`, `Link`, `ParseLink`, `NewClaim`, `NewApproval`, `LinkPrefix` and the `Browser*` functions. All are defined in Tasks 4–5 and used in Tasks 5 and 9.
  - radiance: `helper.Relayer.Relay`, `RelayResult`, `IsRelayOp`, `OpRefreshTickets`, `Source.Invalidate`, `Activity` and `ActivityTracker` (Tasks 6 and 8), used by `ipc.Client.HelperRelay` and `HelperActivity`, and through them by lantern-core's `relayHelper` and `helperActivity` (Task 10).
  - lantern-core: `helper.NewEngine`, `Engine.Call`, `ErrorJSON`, `Error` and the `Code*` constants (Task 9), used by `LanternCore.HelperCall` (Task 10). The ops in the Task 9 table are the strings `HelpersNotifier` sends (Task 12).
  - Dart: `helperCall` (Task 10) is used by `HelpersNotifier` (Task 12). `helpersProvider`, `helperInvitesAvailableProvider`, `HelperResult`, `HelpersState.helpingApprovals` and `helperErrorText` (Task 12) are used in Tasks 13–14. `startForHelper` (Task 14) is called from Home (Task 14).
- The wire names line up:
  - The engine's request structs use lantern-cloud's proto field names (`invite_hash`, `approval_pubkey`, `volunteer_pubkey`, `volunteer_blob`, `approve`, `approval_sig`, `inviter_blob`, `pairing_id`).
  - `listedPairing` reads Task 2's `online` and `created_unix`, the latter as a protojson string.
  - The relay ops in radiance's `relayPaths` are the ops `Engine.post` sends.
- Error codes the UI maps (`helperErrorText`) are the engine's `Code*` values, plus `keystore`, which `HelpersNotifier` sets itself.
