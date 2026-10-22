# Helper Invites, Plan 3: Unbounded Rooms and Ticket-Key Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a paired inviter reach her volunteer over Unbounded (WebRTC) as well as direct mode. freddie gains **rooms**, keyed by opaque pairing ID. A consumer joins its pairings' rooms by presenting inviter tickets, and a volunteer widget presenting volunteer tickets has each genesis message offered to its rooms' waiting consumers before anyone else. A volunteer that has turned off "help others too" (`pool=0`) is offered to nobody else. Tickets refresh in the background with no broflake restart. The plan also adds ticket-key rotation: a key ID in every ticket, a key list endpoint, a `previous_seed`, and a key generation in the pairing short-ID revision.

**Architecture:** lantern-cloud already issues 15-minute ed25519 pairing tickets (plan 1). Each ticket now names its key (`"k"`), and `GET /v1/helper/ticket-key` lists the current key and, during a rotation, the previous one. freddie fetches that public list over HTTPS, so it holds no secret. It verifies tickets with a new broflake package, `common/helperticket`, which is checked against lantern-cloud's golden vectors. freddie's consumer index gains a `byRoom` index beside `byCountry`. `DeliverGenesis` tries rooms first, then, only when the producer serves the pool, country targeting and then broadcast. freddie re-verifies every ticket at every subscribe and genesis, so access ends when tickets stop being issued. In broflake, `WebRTCOptions` gains live room accessors that the producer reads at each genesis and the consumer at each subscribe. lantern-box passes a ticket source from the context into the consumer. radiance fetches tickets from `/v1/helper/tickets`, feeds volunteer tickets to the widget and inviter tickets to the tunnel's Unbounded outbound, and restarts the widget when one of its pairings ends.

```mermaid
sequenceDiagram
    autonumber
    participant V as Volunteer widget<br/>radiance unbounded.go + broflake producer.go
    participant API as lantern-cloud<br/>helper_handler.go
    participant F as freddie<br/>freddie.go + cmd/freddie/tickets.go
    participant C as Inviter consumer<br/>radiance tunnel.go → lantern-box outbound.go → broflake consumer.go

    F->>API: GET /v1/helper/ticket-key (start, every 5 min, on unknown kid)
    API-->>F: {"public_key", "keys":[{"kid","public_key"}]}
    V->>API: POST /v1/helper/tickets (X-Lantern-Device-Id)
    API-->>V: volunteer ticket for pairing P
    C->>API: POST /v1/helper/tickets (X-Lantern-Device-Id)
    API-->>C: inviter ticket for pairing P
    C->>F: GET /v1/signal<br/>X-BF-Room-Tickets: inviter(P)
    Note over F: verify sig, kid, expiry, role=inviter<br/>index C in byRoom[P] and in the pool ⚠️
    V->>F: POST /v1/signal genesis<br/>room-ticket=volunteer(P), pool=0
    rect rgba(200, 230, 255, 0.4)
        Note over F: DeliverGenesis: room P first.<br/>A room consumer took it, so stop.<br/>pool=0 and nobody in P: no fallback, wait out the TTL
    end
    F-->>C: genesis {"Room":true} (preferred over pool genesis)
    C->>F: POST offer (send-to = genesis ReplyTo)
    F-->>V: offer (the genesis POST's response)
    V->>F: answer, ICE
    F-->>C: answer, ICE
    C->>V: WebRTC data channel, QUIC; V forwards to the egress
```

**Tech Stack:** Go; broflake (`github.com/getlantern/broflake`, the unbounded repo) with pion WebRTC and OpenTelemetry; lantern-cloud with protobuf/gRPC via buf, testify and `testdb`; lantern-box (sing-box adapter); radiance (koanf settings, kindling HTTP client).

**Spec:** `docs/superpowers/specs/2026-10-20-helper-invites-design.md` (unbounded repo, branch `docs/helper-invites-spec`): Section 2 "Pairing tickets", "Unbounded" and "Revocation"; the threat model; Section 4 "Telemetry".

**Plan series:** 1 Pairing core (lantern-cloud PR #3660) · 2 Direct-mode routing (lantern-cloud PR #3675, radiance PR #698) · **3 Unbounded rooms and ticket-key rotation (this plan)** · 4 App and browser volunteers · 5 Landing page and rewards.

## Global Constraints

### Wire formats (normative; plan 4's app and browser widget implement against these)

| Name | Definition |
|---|---|
| Ticket | `base64url(payload) + "." + base64url(ed25519 signature over the payload bytes)`, both unpadded (RFC 4648 §5). The payload is compact JSON with exactly these keys in this order: `{"v":1,"k":"<kid>","p":"<pairing uuid>","r":"inviter"\|"volunteer","e":<unix seconds>}`. `p` is the canonical lowercase hyphenated UUID. The lifetime is 15 minutes (`pairing.TicketLifetime`). There is no device ID (plan 1 ruling F1). `"k"` is new in this plan, and `v` stays 1: no verifier was deployed before it, and plan 1's key-ID-less tickets expire within 15 minutes of the lantern-cloud deploy. |
| Ticket verification | Reject a token longer than 512 characters. Split it at the first `.` and decode both halves. The payload must be JSON with `v == 1` and a non-empty `k`. `k` must name a held key, or the result is `ErrUnknownKey`. The signature must verify under that key. `p` must parse as a UUID and equal its canonical form. `r` must be `inviter` or `volunteer`. The ticket is valid while `now < e`. A verifier then checks that the role is the one the request needs: inviter on a consumer's subscribe, volunteer on a producer's genesis. |
| Key ID (`kid`) | `lowercase hex(SHA-256(raw 32-byte ed25519 public key))[:8]`, the first 4 bytes. The test key (seed `0x09`×32) is `dbc29825`, and the test "previous" key (seed `0x08`×32) is `5c29b78f`. |
| Key list | `GET /v1/helper/ticket-key` is public: no device ID, no flag. It returns 200 with `{"public_key":"<std base64>","keys":[{"kid":"<kid>","public_key":"<std base64>"}, …]}`, using proto field names and standard base64 with padding, as all lantern-cloud JSON does. `keys` lists the current key first, then the previous key while `previous_seed` is set. The top-level `public_key` is the current key, kept for anything that already reads it. With no key configured the endpoint returns 503. A verifier refuses a list entry whose `kid` is not `KeyID(public_key)`, and uses the top-level `public_key` alone only when `keys` is empty (an API that predates this plan). |
| Vault | `secret/data/lantern_cloud/helper_tickets` holds three fields. `seed` is base64 of 32 bytes, required, and signs tickets. `previous_seed` is optional and present only during a rotation. `generation` is optional, an integer 0–65535 as a string (`vault kv put`) or a JSON number, with absent meaning 0, and is incremented at every rotation. A malformed `seed` or `generation` disables tickets and direct mode, as a malformed seed does today. A malformed `previous_seed` is ignored and logged. |
| `X-BF-Room-Tickets` | A request header on a consumer's `GET /v1/signal`: the consumer's inviter tickets, comma-separated, with no spaces. It is omitted when the consumer has none, so a consumer without tickets is byte-identical to earlier releases. freddie splits every value of the header at commas, trims, keeps only tickets of 1–512 characters from `[A-Za-z0-9_.-]`, drops duplicates, and considers at most the first 20 (`common.MaxRoomTickets`). Each is verified for role `inviter`. Valid tickets index the consumer under their pairing IDs. Invalid tickets are dropped without a trace. The consumer is always also in the general pool. At the cap the header is about 4.4 KB (a ticket is about 215 characters). |
| `room-ticket` | A repeatable form field on a producer's genesis `POST /v1/signal`: its volunteer tickets, with the same normalization and the same cap of 20. Each is verified for role `volunteer`. It is omitted when the producer has none. |
| `pool` | A form field on a genesis POST. `"0"` means rooms only: offer this genesis to nobody outside the producer's rooms. An absent field, or any other value, means the producer also serves everyone, so clients that predate it are unchanged. Clients send it only as `"0"`. |
| `Room` | `SignalMsg.Room bool` with `json:",omitempty"`. freddie sets `"Room":true` on a genesis message it delivers through a room. Producers cannot set it, because it sits outside their `Payload`. A consumer that hears several genesis messages during its patience window answers a random room genesis if there is one, and a random one of all of them otherwise. |
| Matching precedence | This runs for each genesis POST, and so for each free producer slot, because each slot runs its own genesis loop. (1) **Room.** If the producer's valid volunteer tickets name rooms, the genesis goes, non-blocking, to every parked consumer indexed in any of those rooms, at most once each. If one or more took it, delivery stops. (2) **No fallback.** If `pool=0`, delivery stops: nobody else gets it, the POST waits out `defaultMsgTTL` (5 s) for an offer, returns an empty body, and the slot loops. (3) **Country.** Otherwise it goes to parked consumers from the producer's target countries, as today. (4) **Broadcast.** If no consumer took it in step 3, it goes to everyone. So it is room → country → broadcast, and `pool=0` means steps 3 and 4 never run. A producer with `pool=0` and no valid ticket is offered to nobody. |
| Ticket refresh | radiance fetches `POST /v1/helper/tickets` when tickets are first read. It fetches again 10 minutes after a successful fetch, inside the 15-minute lifetime; 1 minute after a failure, keeping the last tickets; and 1 hour after a 404, which means `helper_invites` is off for this client. That fetch clears the tickets. A read never blocks, so a fetch runs in the background when one is due. The widget re-applies its volunteer tickets every 30 s (`roomSyncInterval`). The consumer reads its inviter tickets at every subscribe, about every 20 s (`consumerTTL`). freddie re-verifies at every subscribe and every genesis. |
| freddie key refresh | freddie fetches the key list at startup and then every 5 minutes. After a failure it retries in 30 s and keeps the last good list. A ticket naming an unknown `kid` triggers an early refresh, at most once every 30 s. The URL is `--helper-ticket-key-url`, or else `https://api.$LC_PUBLIC_DOMAIN/v1/helper/ticket-key`. With neither, rooms are off. |
| Pairing short-ID revision | `revision = version<<36 \| keygen<<20 \| (epoch & 0xFFFFF)`, an `int64`. `keygen` is the Vault `generation`. This supersedes plan 2's `version<<32 \| epoch`. See the bit budget below. |
| Volunteer accepted set | For each pairing, the current key's IDs for epochs `e-1, e, e+1`, then, while `previous_seed` is set, the previous key's IDs for the same epochs. This supersedes plan 2's "three IDs per pairing". The inviter's paired outbound always uses the current key's epoch-`e` ID. |

**Revision bit budget.**

| Bits | Field | Range | Why it is enough |
|---|---|---|---|
| 63 | sign | always 0 | Revisions stay positive, and 0 still means "unknown". |
| 36–62 (27 bits) | `version` | 0 – 134,217,727 | `helper_volunteer_versions.version` bumps once per insert or delete of one volunteer's pairing. With at most 20 pairings and plan 1's rate limits, 134 million changes to one volunteer is unreachable. |
| 20–35 (16 bits) | `keygen` | 0 – 65,535 | One increment per ticket-key rotation. |
| 0–19 (20 bits) | `epoch` | 0 – 1,048,575 days | That is 2,870 years after 1970, until about the year 4840. Today (2026-10-22) is epoch 20,748, which also fits 15 bits, so the brief's `epoch & 0xffff` would have wrapped in 2149. 20 bits removes the wrap without costing `version` anything it could use. |

Ordering: a bump in `version` outranks any `keygen` and `epoch`, and a bump in `keygen` outranks any `epoch`. So a pairing change is always newest, and a key rotation is newer than every set from before it at the same version. **Upgrade from plan 2's encoding:** a radiance volunteer may hold a revision `v<<32 | e` from an API running plan 2. For `v ≥ 1`, `v<<36 ≥ 16v·2^32 > (v+1)·2^32 > v<<32 | e`. For `v = 0`, both encodings give `keygen<<20 | e ≥ e`. A new revision is therefore never lower than an old one at the same or a later version. Task 8 tests this.

### Rules

- **Telemetry:** no log line, span attribute, span event, span status or metric anywhere in this plan may contain a pairing ID, a ticket, a ticket's payload, or a count of rooms, tickets or room members. That covers freddie, cmd/freddie, the broflake clients, lantern-box and radiance. Two things are allowed:
  - The counters `freddie.genesis.room_match{matched=true|false}`, recorded once per genesis that carried at least one valid room ticket, and `freddie.ticket_key.refresh{ok=true|false}`.
  - Fixed-message logs and errors whose text contains no ticket. The `helperticket` errors are fixed sentinels.

  freddie's spans gain no room attributes. The existing `genesis.targeting` attribute is still set, but only when the pool step runs. radiance's fetch errors never include the response body, because the body is tickets.
- **Dependency bumps:** after every `go get module@version`, run `go mod tidy`, check `git diff` for unexpected changes, and commit `go.mod` and `go.sum` together in the same commit as the bump. broflake is consumed by pseudo-version and is not tagged. lantern-box is consumed by tag.
- **sqlc (lantern-cloud):** this plan changes no query or migration. If a rebase brings one in, run `cd cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`, not `make sqlc`, and commit the generated `cmd/api/dal/` files.
- **proto (lantern-cloud):** after editing `helper.proto`, run `make proto`, then revert every generated file that does not belong to `helper.proto`, because `make proto` rewrites unrelated swagger files. Keep only:
  - `cmd/api/apipb/services/helper.pb.go`
  - `helper_grpc.pb.go`
  - `cmd/backoffice/src/gen/services/helper_pb.ts`
  - `helper-HelperService_connectquery.ts`
  - `openapiv2/services/helper.swagger.json`
- **Tests (lantern-cloud):**
  - DB-backed tests run as `bin/testsetup go test -tags=test,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme ./cmd/api/ -run '<pattern>' -v`. This document writes that as `$LC_TEST`, so `$LC_TEST 'Pattern'` means exactly that command with that pattern.
  - Pure packages run as `cd cmd/api && go test ./pairing/ -v`, and freddie runs as `go test -race ./cmd/freddie/ -v`.
  - New test files in `cmd/api` that use the peer test helpers carry `//go:build !codeanalysis`.
- **Tests (broflake):** CI runs `go test -race ./...` and `GOOS=js GOARCH=wasm go build -o /dev/null ./cmd` (`.github/workflows/build-widget-wasm.yml`). Every broflake task must pass both, because `clientcore` and `common` compile to the browser widget.
- **Tests (lantern-box):** the end-to-end test runs as `go test -race -timeout 10m -run 'TestUnbounded' -v ./test/e2e/`, and is skipped under `-short`.
- **Model:** use the strongest available model for every task and every review. This plan crosses four repos, and its correctness depends on byte-exact formats.
- **PRs:** open every PR with the `skills:create-pr` skill. A hook blocks a bare `gh pr create`.
- **[Human]** marks a step that needs a person: merging, anything that tags a release, deploying freddie or the API, Vault writes, and every production change.

### Repos, branches and worktrees (in execution order)

| Repo | Worktree | Branch | Base |
|---|---|---|---|
| unbounded (`github.com/getlantern/broflake`) | `~/go/src/github.com/getlantern/unbounded-rooms` | `fisk/helper-rooms` | `origin/main` (0ef956d). The main checkout is on the docs branch, which is behind. |
| lantern-cloud | `~/go/src/github.com/getlantern/lantern-cloud-helper-rooms` | `fisk/helper-rooms` | `origin/fisk/helper-direct-mode` (plan 2, PR #3675, being rebased now: check its head first, Task 6 Step 1) |
| lantern-box | `~/go/src/github.com/getlantern/lantern-box-rooms` | `fisk/unbounded-rooms` | `origin/main` (f9d7290, v0.0.170) |
| radiance | `~/go/src/github.com/getlantern/radiance-helper-rooms` | `fisk/helper-rooms` | `origin/main` if PR #698 has merged, else `origin/fisk/helper-direct-mode`, because this plan reuses #698's `settings.PeerServePoolKey`. The main checkout is on another feature branch. |

Two values come from earlier tasks: `UB_SHA`, the unbounded merge commit (Task 5 Step 8), and `LB_TAG`, the lantern-box release tag (Task 11 Step 6).

## File Structure

| Repo | File | Responsibility |
|---|---|---|
| unbounded | `common/helperticket/helperticket.go`, `helperticket_test.go`, `testdata/vectors.json` | Ticket verification, key IDs, `Sign` for tests and tools, `Peek`, `RoomFunc`; golden vectors copied from lantern-cloud |
| unbounded | `common/rooms.go`, `common/rooms_test.go` | `X-BF-Room-Tickets`, `room-ticket`, `pool`, ticket normalization, `IsRoomGenesis` |
| unbounded | `common/resource.go` | `SignalMsg.Room` |
| unbounded | `clientcore/settings.go`, `settings_test.go` | `RoomTickets`, `RoomTicketSource`, `RoomsOnly` and their accessors |
| unbounded | `clientcore/producer.go`, `consumer.go`, `rooms_test.go` | `genesisForm`, `newSubscribeRequest`, `pickGenesis` and their use in the FSMs |
| unbounded | `freddie/freddie.go` | `byRoom` index, `DeliverGenesis`, `RoomTicket` hook, CORS, `freddie.genesis.room_match` |
| unbounded | `freddie/freddie_test.go`, `freddie/rooms_test.go` | Index tests; `startTestServer` extracted from `newTestServer`; handler and telemetry tests |
| lantern-cloud | `cmd/api/pairing/ticket.go`, `ticket_test.go`, `invite_test.go`, `testdata/vectors.json` | `TicketKeyID`, `"k"`, vectors |
| lantern-cloud | `cmd/api/apipb/proto/services/helper.proto` (+ generated) | `HelperTicketKey.keys` |
| lantern-cloud | `cmd/api/helper_handler.go`, `helper_ticket_key_test.go`, `helper_handler_db_test.go` | `loadHelperTicketKeys`, the key list |
| lantern-cloud | `cmd/api/main.go` | Key, previous key, generation and both `K_sid` keys on `lanternService` |
| lantern-cloud | `cmd/api/helper_direct.go`, `helper_direct_db_test.go` | Revision with `keygen`; previous-key short IDs |
| lantern-cloud | `cmd/freddie/tickets.go`, `tickets_test.go`, `main.go`, `go.mod`, `go.sum` | Key list fetcher and `RoomTicket` hook; broflake bump |
| lantern-box | `adapter/room_tickets.go`, `room_tickets_test.go` | `ContextWithRoomTicketSource` |
| lantern-box | `protocol/unbounded/outbound.go`, `option/unbounded.go`, `go.mod`, `go.sum` | Wire the source into broflake; broflake bump |
| lantern-box | `test/e2e/unbounded_test.go` | `TestUnboundedRoomE2E` |
| radiance | `helper/tickets.go`, `helper/tickets_test.go` | `Fetcher`, `Split`, `Source` |
| radiance | `unbounded/unbounded.go`, `unbounded/rooms_test.go`, `backend/radiance.go` | Volunteer tickets and serve-pool on the widget; restart when a pairing ends; wiring |
| radiance | `vpn/tunnel.go`, `vpn/room_tickets_test.go`, `go.mod`, `go.sum` | Consumer ticket source on the box context; lantern-box and broflake bumps |

---

### Task 1: unbounded — `common/helperticket` verifier and golden vectors

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-rooms`

**Files:**
- Create: `common/helperticket/helperticket.go`
- Create: `common/helperticket/helperticket_test.go`
- Create: `common/helperticket/testdata/vectors.json`
- Create: `common/rooms.go`, holding only `MaxRoomTicketLen` for now; Task 2 fills in the rest

**Interfaces:**
- Consumes: the ticket format in the wire table, and lantern-cloud's ticket vectors as Task 6 will generate them. They are deterministic: ed25519 signatures do not depend on a random nonce.
- Produces (package `helperticket`):
  - `type Role string`, with `RoleInviter = "inviter"` and `RoleVolunteer = "volunteer"`
  - `var ErrInvalid, ErrExpired, ErrUnknownKey error`
  - `type Ticket struct { KeyID, PairingID string; Role Role; Expires time.Time }`
  - `type Key struct { ID string; Public ed25519.PublicKey }`, `func KeyID(ed25519.PublicKey) string`, `func NewKey(ed25519.PublicKey) Key`
  - `func Sign(priv ed25519.PrivateKey, t Ticket) (string, error)`
  - `func Verify(keys []Key, token string, now time.Time) (Ticket, error)`
  - `func Peek(token string) (Role, string)`: the role and pairing ID, unverified
  - `func RoomFunc(keys func() []Key, now func() time.Time) func(token string, role Role) (room string, ok bool)`
  - `common.MaxRoomTicketLen = 512`

- [ ] **Step 1: Create the worktree**

```bash
git -C ~/go/src/github.com/getlantern/unbounded fetch origin
git -C ~/go/src/github.com/getlantern/unbounded worktree add ~/go/src/github.com/getlantern/unbounded-rooms -b fisk/helper-rooms origin/main
git -C ~/go/src/github.com/getlantern/unbounded-rooms log --oneline -1
```

Expected: `0ef956d freddie: don't mark late signaling replies as span errors (#447)`, or a later main commit. If main has moved, re-read `freddie/freddie.go` and `clientcore/{settings,producer,consumer}.go` before Tasks 2–5, and adjust the line anchors below.

- [ ] **Step 2: Add the golden vectors**

`common/helperticket/testdata/vectors.json` is the `ticket` section of lantern-cloud's `cmd/api/pairing/testdata/vectors.json` as Task 6 regenerates it. Task 6 Step 6 diffs the two files. The values below come from lantern-cloud's test keys: seed `0x09`×32 is current, and seed `0x08`×32 is previous.

```json
{
  "ticket": {
    "expired_at_unix": "1800000900",
    "inviter_token": "eyJ2IjoxLCJrIjoiZGJjMjk4MjUiLCJwIjoiNmYxYzJiMGUtOGE3ZC00YzFlLTlmMDAtMDEyMzQ1Njc4OWFiIiwiciI6Imludml0ZXIiLCJlIjoxODAwMDAwOTAwfQ.n6a05OiRDaY974qpiTraSK5nDjL8-DPcb8Yf2DvSYcCGoXFmiYqEas_NJug6RkZtA_FUTnH4udgO0CcoFGdYDQ",
    "kid": "dbc29825",
    "pairing_id": "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab",
    "payload_json": "{\"v\":1,\"k\":\"dbc29825\",\"p\":\"6f1c2b0e-8a7d-4c1e-9f00-0123456789ab\",\"r\":\"volunteer\",\"e\":1800000900}",
    "previous_kid": "5c29b78f",
    "previous_public_key_hex": "1398f62c6d1a457c51ba6a4b5f3dbd2f69fca93216218dc8997e416bd17d93ca",
    "previous_token": "eyJ2IjoxLCJrIjoiNWMyOWI3OGYiLCJwIjoiNmYxYzJiMGUtOGE3ZC00YzFlLTlmMDAtMDEyMzQ1Njc4OWFiIiwiciI6InZvbHVudGVlciIsImUiOjE4MDAwMDA5MDB9.qVLwDKhjB3T2vDjNsJGpgmz3iE0AalgjZlMWdsiE3jdpiqtNuLjtdIpqE9HhofgZ49MA8XefyWeXPoCcXsbqCQ",
    "public_key_hex": "fd1724385aa0c75b64fb78cd602fa1d991fdebf76b13c58ed702eac835e9f618",
    "token": "eyJ2IjoxLCJrIjoiZGJjMjk4MjUiLCJwIjoiNmYxYzJiMGUtOGE3ZC00YzFlLTlmMDAtMDEyMzQ1Njc4OWFiIiwiciI6InZvbHVudGVlciIsImUiOjE4MDAwMDA5MDB9.D_GcmByzgEhByMPPX1Ok-FR4b7ZNor_d1kyK2IFWbl6E6dvmlouiOT1RunJgwPxzJHDBMlTTL4rnaBM6zxq9Bg",
    "verify_at_unix": "1800000000"
  }
}
```

- [ ] **Step 3: Write the failing tests**

`common/helperticket/helperticket_test.go`:

```go
package helperticket

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// vectors is the "ticket" section of lantern-cloud's
// cmd/api/pairing/testdata/vectors.json.
func vectors(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Ticket map[string]string `json:"ticket"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Ticket
}

func unixAt(t *testing.T, s string) time.Time {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.Unix(n, 0)
}

func pubFromHex(t *testing.T, s string) ed25519.PublicKey {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		t.Fatalf("bad public key %q: %v", s, err)
	}
	return b
}

// lanternCloudTestKey is lantern-cloud's pairing test key, seed 0x09 x 32.
func lanternCloudTestKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
}

// signRaw signs an arbitrary payload, for tickets Sign refuses to make.
func signRaw(priv ed25519.PrivateKey, payload string) string {
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(ed25519.Sign(priv, []byte(payload)))
}

func TestVectors(t *testing.T) {
	v := vectors(t)
	cur := NewKey(pubFromHex(t, v["public_key_hex"]))
	prev := NewKey(pubFromHex(t, v["previous_public_key_hex"]))
	if cur.ID != v["kid"] || prev.ID != v["previous_kid"] {
		t.Fatalf("key IDs %s, %s; vectors say %s, %s", cur.ID, prev.ID, v["kid"], v["previous_kid"])
	}
	at := unixAt(t, v["verify_at_unix"])
	exp := unixAt(t, v["expired_at_unix"])
	keys := []Key{cur, prev}

	tk, err := Verify(keys, v["token"], at)
	if err != nil {
		t.Fatalf("Verify(token): %v", err)
	}
	if tk.PairingID != v["pairing_id"] || tk.Role != RoleVolunteer || tk.KeyID != v["kid"] || !tk.Expires.Equal(exp) {
		t.Errorf("token decoded to %+v", tk)
	}
	enc, _, _ := strings.Cut(v["token"], ".")
	if payload, _ := b64.DecodeString(enc); string(payload) != v["payload_json"] {
		t.Errorf("payload %s, want %s", payload, v["payload_json"])
	}

	if tk, err := Verify(keys, v["inviter_token"], at); err != nil || tk.Role != RoleInviter {
		t.Errorf("inviter_token: %+v, %v", tk, err)
	}
	if tk, err := Verify(keys, v["previous_token"], at); err != nil || tk.KeyID != v["previous_kid"] {
		t.Errorf("previous_token with both keys: %+v, %v", tk, err)
	}
	if _, err := Verify([]Key{cur}, v["previous_token"], at); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("previous_token without the previous key: %v, want ErrUnknownKey", err)
	}
	if _, err := Verify(keys, v["token"], exp); !errors.Is(err, ErrExpired) {
		t.Errorf("token at its expiry: %v, want ErrExpired", err)
	}

	// The signer is lantern-cloud's format byte for byte: ed25519 is
	// deterministic, so signing the same ticket reproduces the token.
	priv := lanternCloudTestKey()
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), cur.Public) {
		t.Fatal("seed 0x09 is not the vectors' key")
	}
	got, err := Sign(priv, Ticket{PairingID: v["pairing_id"], Role: RoleVolunteer, Expires: exp})
	if err != nil || got != v["token"] {
		t.Errorf("Sign = %s, %v; want the vector token", got, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	priv := lanternCloudTestKey()
	keys := []Key{NewKey(priv.Public().(ed25519.PublicKey))}
	now := time.Unix(1_800_000_000, 0)
	good, err := Sign(priv, Ticket{PairingID: "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab", Role: RoleInviter, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	payload, sig, _ := strings.Cut(good, ".")
	const p = `"p":"6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"`
	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"no separator":        {"abc", ErrInvalid},
		"tampered payload":    {"f" + good[1:], ErrInvalid},
		"short signature":     {payload + "." + sig[:20], ErrInvalid},
		"too long":            {good + strings.Repeat("A", 512), ErrInvalid},
		"version 2":           {signRaw(priv, `{"v":2,"k":"dbc29825",`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"no key id":           {signRaw(priv, `{"v":1,`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"non-canonical uuid":  {signRaw(priv, `{"v":1,"k":"dbc29825","p":"6F1C2B0E-8A7D-4C1E-9F00-0123456789AB","r":"inviter","e":1800000060}`), ErrInvalid},
		"unknown role":        {signRaw(priv, `{"v":1,"k":"dbc29825",`+p+`,"r":"admin","e":1800000060}`), ErrInvalid},
		"key id of another":   {signRaw(priv, `{"v":1,"k":"00000000",`+p+`,"r":"inviter","e":1800000060}`), ErrUnknownKey},
		"signed by another":   {signRaw(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)), `{"v":1,"k":"dbc29825",`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"not JSON":            {signRaw(priv, `not json`), ErrInvalid},
		"base64 with padding": {payload + "==." + sig, ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(keys, tc.token, now); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := Verify(nil, good, now); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("no keys: %v, want ErrUnknownKey", err)
	}
	if _, err := Sign(priv, Ticket{PairingID: "not-a-uuid", Role: RoleInviter}); err == nil {
		t.Error("Sign accepted a malformed pairing ID")
	}
	if _, err := Sign(priv, Ticket{PairingID: "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab", Role: "admin"}); err == nil {
		t.Error("Sign accepted an unknown role")
	}
}

func TestPeek(t *testing.T) {
	v := vectors(t)
	if role, id := Peek(v["inviter_token"]); role != RoleInviter || id != v["pairing_id"] {
		t.Errorf("Peek(inviter_token) = %q, %q", role, id)
	}
	if role, id := Peek(v["token"]); role != RoleVolunteer || id != v["pairing_id"] {
		t.Errorf("Peek(token) = %q, %q", role, id)
	}
	for _, junk := range []string{"", "x", "!!.!!", base64.RawURLEncoding.EncodeToString([]byte(`{"r":"admin"}`)) + ".x"} {
		if role, id := Peek(junk); role != "" || id != "" {
			t.Errorf("Peek(%q) = %q, %q; want nothing", junk, role, id)
		}
	}
}

func TestRoomFunc(t *testing.T) {
	v := vectors(t)
	keys := []Key{NewKey(pubFromHex(t, v["public_key_hex"]))}
	now := unixAt(t, v["verify_at_unix"])
	room := RoomFunc(func() []Key { return keys }, func() time.Time { return now })

	if got, ok := room(v["inviter_token"], RoleInviter); !ok || got != v["pairing_id"] {
		t.Errorf("inviter ticket as inviter: %q, %v", got, ok)
	}
	if _, ok := room(v["inviter_token"], RoleVolunteer); ok {
		t.Error("an inviter ticket admitted a volunteer")
	}
	now = unixAt(t, v["expired_at_unix"])
	if _, ok := room(v["inviter_token"], RoleInviter); ok {
		t.Error("an expired ticket admitted its bearer")
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/unbounded-rooms && go test ./common/helperticket/`
Expected: the build fails with `undefined: NewKey`, `undefined: Verify` and `undefined: b64`.

- [ ] **Step 5: Implement**

`common/rooms.go` (Task 2 extends it):

```go
package common

// MaxRoomTicketLen bounds one helper pairing ticket. A real ticket is about
// 215 characters; anything longer is refused before it is decoded.
const MaxRoomTicketLen = 512
```

`common/helperticket/helperticket.go`:

```go
// Package helperticket verifies Lantern helper pairing tickets: short-lived
// statements, signed by lantern-cloud, that their bearer may use a pairing in
// one role until an expiry. Freddie verifies them to put consumers and
// producers in rooms keyed by pairing ID.
//
// The format is lantern-cloud's (cmd/api/pairing/ticket.go). testdata/
// vectors.json is a copy of the "ticket" section of lantern-cloud's golden
// vectors, and the two must not drift.
//
// Tickets are credentials and pairing IDs are private: callers must never
// log, trace or count either.
package helperticket

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/getlantern/broflake/common"
)

// Role says which side of a pairing a ticket was issued to.
type Role string

const (
	RoleInviter   Role = "inviter"
	RoleVolunteer Role = "volunteer"
)

var (
	ErrInvalid    = errors.New("helper ticket invalid")
	ErrExpired    = errors.New("helper ticket expired")
	ErrUnknownKey = errors.New("helper ticket signed by an unknown key")
)

// Ticket is a ticket's contents. PairingID is the room it admits to: an
// opaque, canonical lowercase UUID.
type Ticket struct {
	KeyID     string
	PairingID string
	Role      Role
	Expires   time.Time
}

// Key is a public key tickets are verified with, and its ID.
type Key struct {
	ID     string
	Public ed25519.PublicKey
}

// KeyID names pub: the first 8 hex characters of SHA-256 of the raw key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// NewKey returns pub with its ID.
func NewKey(pub ed25519.PublicKey) Key {
	return Key{ID: KeyID(pub), Public: append(ed25519.PublicKey(nil), pub...)}
}

// wire is the payload. Its field order is the format's key order.
type wire struct {
	V int    `json:"v"`
	K string `json:"k"`
	P string `json:"p"`
	R Role   `json:"r"`
	E int64  `json:"e"`
}

var b64 = base64.RawURLEncoding

// Sign encodes and signs t with priv, naming priv's key; t.KeyID is ignored.
// lantern-cloud is the only production signer: this is for tests and tools.
func Sign(priv ed25519.PrivateKey, t Ticket) (string, error) {
	if t.Role != RoleInviter && t.Role != RoleVolunteer {
		return "", fmt.Errorf("helperticket: unknown role %q", t.Role)
	}
	id, err := uuid.Parse(t.PairingID)
	if err != nil {
		return "", fmt.Errorf("helperticket: pairing ID: %w", err)
	}
	payload, err := json.Marshal(wire{V: 1, K: KeyID(priv.Public().(ed25519.PublicKey)), P: id.String(), R: t.Role, E: t.Expires.Unix()})
	if err != nil {
		return "", fmt.Errorf("helperticket: encode: %w", err)
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// decode splits and decodes token without verifying it.
func decode(token string) (payload, sig []byte, w wire, err error) {
	if len(token) > common.MaxRoomTicketLen {
		return nil, nil, wire{}, ErrInvalid
	}
	enc, encSig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, nil, wire{}, ErrInvalid
	}
	payload, err1 := b64.DecodeString(enc)
	sig, err2 := b64.DecodeString(encSig)
	if err1 != nil || err2 != nil {
		return nil, nil, wire{}, ErrInvalid
	}
	if err := json.Unmarshal(payload, &w); err != nil {
		return nil, nil, wire{}, ErrInvalid
	}
	return payload, sig, w, nil
}

// Verify checks token against the key in keys that it names, then its
// expiry at now. An expired ticket's contents come back with ErrExpired.
func Verify(keys []Key, token string, now time.Time) (Ticket, error) {
	payload, sig, w, err := decode(token)
	if err != nil {
		return Ticket{}, err
	}
	if w.V != 1 || w.K == "" || len(sig) != ed25519.SignatureSize {
		return Ticket{}, ErrInvalid
	}
	var key *Key
	for i := range keys {
		if keys[i].ID == w.K {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return Ticket{}, ErrUnknownKey
	}
	if len(key.Public) != ed25519.PublicKeySize || !ed25519.Verify(key.Public, payload, sig) {
		return Ticket{}, ErrInvalid
	}
	id, err := uuid.Parse(w.P)
	if err != nil || id.String() != w.P || (w.R != RoleInviter && w.R != RoleVolunteer) {
		return Ticket{}, ErrInvalid
	}
	t := Ticket{KeyID: w.K, PairingID: w.P, Role: w.R, Expires: time.Unix(w.E, 0)}
	if !now.Before(t.Expires) {
		return t, ErrExpired
	}
	return t, nil
}

// Peek returns the role and pairing ID token claims, without verifying it,
// or "", "" if it cannot be read. Clients use it to sort and compare their
// own tickets; nothing may trust it.
func Peek(token string) (Role, string) {
	_, _, w, err := decode(token)
	if err != nil || (w.R != RoleInviter && w.R != RoleVolunteer) || w.P == "" {
		return "", ""
	}
	return w.R, w.P
}

// RoomFunc returns a hook for freddie.Freddie.RoomTicket that verifies tickets
// against keys() at now(), requires role, and returns the pairing ID.
func RoomFunc(keys func() []Key, now func() time.Time) func(token string, role Role) (string, bool) {
	return func(token string, role Role) (string, bool) {
		t, err := Verify(keys(), token, now())
		if err != nil || t.Role != role {
			return "", false
		}
		return t.PairingID, true
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./common/... && GOOS=js GOARCH=wasm go build -o /dev/null ./cmd`
Expected: PASS for `TestVectors`, `TestVerifyRejects` (12 subtests), `TestPeek` and `TestRoomFunc`; the wasm build succeeds.

- [ ] **Step 7: Commit**

```bash
git add common/rooms.go common/helperticket
git commit -m "helperticket: verify lantern-cloud helper pairing tickets"
```

---

### Task 2: unbounded — room wire constants and live `WebRTCOptions` accessors

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-rooms`

**Files:**
- Modify: `common/rooms.go`
- Modify: `common/resource.go` (`SignalMsg`)
- Create: `common/rooms_test.go`
- Modify: `clientcore/settings.go`
- Modify: `clientcore/settings_test.go`

**Interfaces:**
- Consumes: the `SetTargetCountries`/`targetCountries` pattern (`settings.go:89-105`), and `MaxRoomTicketLen` (Task 1).
- Produces:
  - `common.RoomTicketsHeader`, `RoomTicketField`, `PoolField`, `MaxRoomTickets`
  - `func NormalizeRoomTickets([]string) []string`, `func ParseRoomTicketsHeader([]string) []string`
  - `SignalMsg.Room`, `func IsRoomGenesis([]byte) bool`
  - `WebRTCOptions.RoomTickets`, `RoomTicketSource`, `RoomsOnly`, with `SetRoomTickets`, `SetServePool`, `CurrentRoomTickets` and `ServesPool`. The read accessors are exported, unlike `targetCountries`, because radiance's tests read them (Task 13).

- [ ] **Step 1: Write the failing tests**

`common/rooms_test.go`:

```go
package common

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeRoomTickets(t *testing.T) {
	long := strings.Repeat("a", MaxRoomTicketLen)
	got := NormalizeRoomTickets([]string{" a.b ", "", "a.b", "bad ticket", "x,y", "c-d_e.f", long, long + "a"})
	if want := []string{"a.b", "c-d_e.f", long}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if NormalizeRoomTickets(nil) != nil || NormalizeRoomTickets([]string{"", " "}) != nil {
		t.Error("nothing valid must normalize to nil")
	}
	var many []string
	for i := 0; i < MaxRoomTickets+5; i++ {
		many = append(many, fmt.Sprintf("t%d.x", i))
	}
	if got := NormalizeRoomTickets(many); len(got) != MaxRoomTickets || got[0] != "t0.x" {
		t.Errorf("kept %d tickets starting %q; want the first %d", len(got), got[0], MaxRoomTickets)
	}
}

func TestParseRoomTicketsHeader(t *testing.T) {
	got := ParseRoomTicketsHeader([]string{"a.b, c.d", "e.f,,a.b"})
	if want := []string{"a.b", "c.d", "e.f"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if ParseRoomTicketsHeader(nil) != nil {
		t.Error("no header must parse to nil")
	}
}

func TestSignalMsgRoom(t *testing.T) {
	plain, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}"})
	if strings.Contains(string(plain), "Room") {
		t.Errorf("a non-room message changed on the wire: %s", plain)
	}
	room, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}", Room: true})
	if !IsRoomGenesis(room) || IsRoomGenesis(plain) {
		t.Error("IsRoomGenesis must report exactly the room message")
	}
	offer, _ := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgOffer, Payload: "{}", Room: true})
	if IsRoomGenesis(offer) || IsRoomGenesis([]byte("not json")) {
		t.Error("only a genesis message can be a room genesis")
	}
	if rt, _, err := DecodeSignalMsg(room); err != nil || rt != "r" {
		t.Errorf("DecodeSignalMsg(room) = %q, %v", rt, err)
	}
}
```

Append to `clientcore/settings_test.go`:

```go
func TestWebRTCOptions_RoomTickets(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	if got := o.CurrentRoomTickets(); got != nil {
		t.Fatalf("default room tickets = %q, want none", got)
	}
	if !o.ServesPool() {
		t.Fatal("a new producer must serve the pool")
	}
	var zero WebRTCOptions
	if !zero.ServesPool() {
		t.Fatal("the zero value must serve the pool, as every release before rooms did")
	}

	o.SetRoomTickets([]string{"a.b", "", "a.b", "bad ticket", "c.d"})
	if got := o.CurrentRoomTickets(); !slices.Equal(got, []string{"a.b", "c.d"}) {
		t.Errorf("got %q, want [a.b c.d]", got)
	}
	o.SetServePool(false)
	if o.ServesPool() {
		t.Error("SetServePool(false) left the pool on")
	}
	o.SetServePool(true)
	if !o.ServesPool() {
		t.Error("SetServePool(true) left the pool off")
	}

	// A source, as lantern-box injects for a consumer, wins over the set value.
	o.RoomTicketSource = func() []string { return []string{"e.f", "e.f", ""} }
	if got := o.CurrentRoomTickets(); !slices.Equal(got, []string{"e.f"}) {
		t.Errorf("with a source got %q, want [e.f]", got)
	}
}

// Workers read the room settings while radiance sets them; run with -race.
func TestWebRTCOptions_RoomTicketsConcurrent(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.SetRoomTickets([]string{"a.b"})
				o.SetServePool(j%2 == 0)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.CurrentRoomTickets()
				_ = o.ServesPool()
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./common/ ./clientcore/ -run 'Room|SignalMsgRoom'`
Expected: the build fails with `undefined: NormalizeRoomTickets`, `unknown field Room in struct literal`, and `o.CurrentRoomTickets undefined`.

- [ ] **Step 3: Implement**

Replace `common/rooms.go`:

```go
package common

import (
	"encoding/json"
	"slices"
	"strings"
)

// Helper pairing rooms. A consumer and a producer that belong to the same
// helper pairing meet in a room keyed by the pairing's opaque ID. Each proves
// membership with a short-lived ticket lantern-cloud signs (see
// common/helperticket), which Freddie verifies on every request. Tickets ride
// outside the JSON signaling payload, like target countries, so Freddie stays
// payload-agnostic. Every field is optional, so an old Freddie ignores them and
// an old client never sends them.
const (
	// RoomTicketsHeader carries a consumer's inviter tickets, comma-separated,
	// on its GET of the genesis stream.
	RoomTicketsHeader = "X-BF-Room-Tickets"

	// RoomTicketField is the (repeatable) form field on a genesis POST
	// carrying one of the producer's volunteer tickets.
	RoomTicketField = "room-ticket"

	// PoolField on a genesis POST is "0" when the producer serves only its
	// rooms. Absent, or any other value, means it also serves everyone.
	PoolField = "pool"

	// MaxRoomTickets bounds how many tickets Freddie verifies per request.
	// It matches lantern-cloud's limit of 20 pairings per side.
	MaxRoomTickets = 20

	// MaxRoomTicketLen bounds one ticket. A real ticket is about 215
	// characters; anything longer is refused before it is decoded.
	MaxRoomTicketLen = 512
)

// NormalizeRoomTickets drops empty, oversized and malformed tickets (a ticket
// is base64url and '.') and duplicates, and truncates to MaxRoomTickets.
// Returns nil if nothing remains. It does not verify them: Freddie does.
func NormalizeRoomTickets(tickets []string) []string {
	var out []string
	for _, tk := range tickets {
		if len(out) == MaxRoomTickets {
			break
		}
		tk = strings.TrimSpace(tk)
		if !wellFormedRoomTicket(tk) || slices.Contains(out, tk) {
			continue
		}
		out = append(out, tk)
	}
	return out
}

// ParseRoomTicketsHeader splits every value of RoomTicketsHeader at commas
// and normalizes the result.
func ParseRoomTicketsHeader(values []string) []string {
	var all []string
	for _, v := range values {
		all = append(all, strings.Split(v, ",")...)
	}
	return NormalizeRoomTickets(all)
}

func wellFormedRoomTicket(tk string) bool {
	if tk == "" || len(tk) > MaxRoomTicketLen {
		return false
	}
	for i := 0; i < len(tk); i++ {
		switch c := tk[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// IsRoomGenesis reports whether raw, a signaling message from Freddie, is a
// genesis message Freddie delivered through one of the consumer's rooms.
func IsRoomGenesis(raw []byte) bool {
	var msg SignalMsg
	return json.Unmarshal(raw, &msg) == nil && msg.Type == SignalMsgGenesis && msg.Room
}
```

In `common/resource.go`, replace the `SignalMsg` struct:

```go
type SignalMsg struct {
	ReplyTo string
	Type    SignalMsgType
	Payload string
	// Room is set by Freddie on a genesis message it delivered to this
	// consumer through one of its rooms, so the consumer can prefer its own
	// helpers. Producers cannot set it: they only supply Payload. Omitted
	// otherwise, so every other message is unchanged on the wire.
	Room bool `json:",omitempty"`
}
```

In `clientcore/settings.go`, add these fields to `WebRTCOptions` after `TargetCountries`:

```go
	// RoomTickets optionally lists helper pairing tickets (see
	// common/helperticket) for pairings this client belongs to. A producer
	// sends its volunteer tickets with every genesis message, and Freddie
	// offers that genesis to the consumers waiting in those pairings' rooms
	// before anyone else. A consumer sends its inviter tickets when it
	// subscribes, and Freddie puts it in those rooms. Freddie verifies every
	// ticket and ignores invalid ones. Empty is byte-identical on the wire to
	// releases before this field.
	//
	// Set it before NewBroflake; on a running instance use SetRoomTickets,
	// which takes effect from each worker's next genesis message or
	// subscribe. RoomTicketSource, when set, takes precedence.
	RoomTickets []string
	// RoomTicketSource, if non-nil, is called for the current tickets at each
	// genesis message and each subscribe instead of reading RoomTickets. It
	// runs on the signaling path, so it must be cheap and must not block. Set
	// it before NewBroflake.
	RoomTicketSource func() []string
	// RoomsOnly, on a producer, tells Freddie to offer its genesis messages
	// to the consumers in its rooms and nobody else. The zero value serves
	// everyone, as every release before rooms did, and sends nothing new on
	// the wire. On a running instance use SetServePool.
	RoomsOnly bool
```

Add after `targetCountries()`:

```go
// roomsMu guards RoomTickets and RoomsOnly, which workers read on every
// genesis message and subscribe while SetRoomTickets and SetServePool may
// write them. Package-level, like targetCountriesMu, so WebRTCOptions stays
// safe to copy.
var roomsMu sync.RWMutex

// SetRoomTickets replaces RoomTickets on a live instance. Malformed tickets
// are dropped. Tickets expire after 15 minutes, so the caller renews them;
// each worker picks up the change with its next genesis message or subscribe.
func (o *WebRTCOptions) SetRoomTickets(tickets []string) {
	normalized := common.NormalizeRoomTickets(tickets)
	roomsMu.Lock()
	defer roomsMu.Unlock()
	o.RoomTickets = normalized
}

// SetServePool sets whether a producer's genesis messages may go to consumers
// outside its rooms. Each producer worker picks up the change with its next
// genesis message; established connections are unaffected.
func (o *WebRTCOptions) SetServePool(serve bool) {
	roomsMu.Lock()
	defer roomsMu.Unlock()
	o.RoomsOnly = !serve
}

// CurrentRoomTickets returns the tickets the next genesis message or subscribe
// will carry: RoomTicketSource's if it is set, else RoomTickets.
func (o *WebRTCOptions) CurrentRoomTickets() []string {
	if src := o.RoomTicketSource; src != nil {
		return common.NormalizeRoomTickets(src())
	}
	roomsMu.RLock()
	defer roomsMu.RUnlock()
	return common.NormalizeRoomTickets(o.RoomTickets)
}

// ServesPool reports whether a producer's genesis messages may go to consumers
// outside its rooms.
func (o *WebRTCOptions) ServesPool() bool {
	roomsMu.RLock()
	defer roomsMu.RUnlock()
	return !o.RoomsOnly
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./common/... ./clientcore/ && GOOS=js GOARCH=wasm go build -o /dev/null ./cmd`
Expected: PASS, including the existing `TestWebRTCOptions_TargetCountries*` and `common` tests; the wasm build succeeds.

- [ ] **Step 5: Commit**

```bash
git add common/rooms.go common/rooms_test.go common/resource.go clientcore/settings.go clientcore/settings_test.go
git commit -m "common, clientcore: room ticket wire fields and live room accessors"
```

---

### Task 3: unbounded — producer genesis form, consumer subscribe header, room preference

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-rooms`

**Files:**
- Modify: `clientcore/producer.go` (state 2, lines 157-204)
- Modify: `clientcore/consumer.go` (state 1, lines 146-276)
- Create: `clientcore/rooms_test.go`

**Interfaces:**
- Consumes: `CurrentRoomTickets`, `ServesPool` and `targetCountries` (Task 2), and `common.IsRoomGenesis`.
- Produces:
  - `func genesisForm(options *WebRTCOptions, g []byte) url.Values`
  - `func newSubscribeRequest(ctx context.Context, options *WebRTCOptions) (*http.Request, error)`
  - `func pickGenesis(all, room []string) string`

  The FSMs are refactored onto these three, so the wire behavior is unit-tested here and end to end in Task 11.

- [ ] **Step 1: Write the failing tests**

`clientcore/rooms_test.go`:

```go
package clientcore

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/getlantern/broflake/common"
)

// A producer that uses no rooms sends exactly the form releases before rooms
// sent, so an old Freddie and a new one see the same request.
func TestGenesisFormWithoutRoomsIsUnchanged(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.SetTargetCountries([]string{"IR"})
	got := genesisForm(o, []byte(`{"x":1}`))
	want := url.Values{
		"data":                    {`{"x":1}`},
		"send-to":                 {o.GenesisAddr},
		"type":                    {strconv.Itoa(int(common.SignalMsgGenesis))},
		common.TargetCountryField: {"IR"},
	}
	if got.Encode() != want.Encode() {
		t.Errorf("form %s, want %s", got.Encode(), want.Encode())
	}
}

func TestGenesisFormCarriesRoomsAndPool(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.SetRoomTickets([]string{"a.b", "c.d"})
	f := genesisForm(o, []byte(`{}`))
	if got := f[common.RoomTicketField]; !slices.Equal(got, []string{"a.b", "c.d"}) {
		t.Errorf("room tickets %q", got)
	}
	if _, ok := f[common.PoolField]; ok {
		t.Error("a producer serving the pool must omit the pool field")
	}
	o.SetServePool(false)
	if got := genesisForm(o, []byte(`{}`)).Get(common.PoolField); got != "0" {
		t.Errorf("pool = %q, want 0", got)
	}
}

func TestSubscribeRequestCarriesTickets(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	req, err := newSubscribeRequest(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.URL.String() != o.DiscoverySrv+o.Endpoint {
		t.Errorf("request %s %s", req.Method, req.URL)
	}
	if req.Header.Get(common.VersionHeader) != common.Version {
		t.Error("missing version header")
	}
	if _, ok := req.Header[common.RoomTicketsHeader]; ok {
		t.Error("a consumer without tickets must not send the header")
	}

	o.RoomTicketSource = func() []string { return []string{"a.b", "c.d"} }
	req, err = newSubscribeRequest(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get(common.RoomTicketsHeader); got != "a.b,c.d" {
		t.Errorf("header %q, want a.b,c.d", got)
	}
}

func TestPickGenesisPrefersRooms(t *testing.T) {
	all := []string{"x", "y", "z"}
	for i := 0; i < 50; i++ {
		if got := pickGenesis(all, []string{"y"}); got != "y" {
			t.Fatalf("picked %q over the room genesis", got)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[pickGenesis(all, nil)] = true
	}
	if len(seen) != 3 {
		t.Errorf("without rooms the pick must stay random over all; saw %v", seen)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./clientcore/ -run 'GenesisForm|SubscribeRequest|PickGenesis'`
Expected: the build fails with `undefined: genesisForm`, `undefined: newSubscribeRequest` and `undefined: pickGenesis`.

- [ ] **Step 3: Implement**

In `clientcore/producer.go` state 2, replace the block from `// Signal the genesis message` through the target-country loop (lines 175-188) with:

```go
			// Signal the genesis message
			form := genesisForm(options, g)
```

Append to `producer.go`:

```go
// genesisForm is a genesis POST's form: the genesis message g, plus the
// producer's target countries, room tickets and pool preference. Each of
// those is omitted when it says nothing, so a producer that uses none of them
// sends exactly what releases before them did. They are form fields rather
// than part of the payload so Freddie needn't parse signaling messages to
// honor them.
func genesisForm(options *WebRTCOptions, g []byte) url.Values {
	form := url.Values{
		"data":    {string(g)},
		"send-to": {options.GenesisAddr},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	for _, c := range options.targetCountries() {
		form.Add(common.TargetCountryField, c)
	}
	for _, tk := range options.CurrentRoomTickets() {
		form.Add(common.RoomTicketField, tk)
	}
	if !options.ServesPool() {
		form.Set(common.PoolField, "0")
	}
	return form
}
```

In `clientcore/consumer.go` state 1, replace the request construction (the `http.NewRequestWithContext` call, its error check and `req.Header.Add(common.VersionHeader, common.Version)`) with:

```go
			// Listen for genesis messages
			req, err := newSubscribeRequest(ctx, options)
			if err != nil {
				slog.Debug("Error constructing request", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
```

Below `genesisCandidates := []string{}`, add:

```go
			// Genesis messages Freddie delivered through one of our rooms:
			// they come from our own helpers, so they are answered first.
			roomCandidates := []string{}
```

In the `case <-genesisMsg:` branch, replace `genesisCandidates = append(genesisCandidates, rt)` with:

```go
					genesisCandidates = append(genesisCandidates, rt)
					if common.IsRoomGenesis(rawMsg) {
						roomCandidates = append(roomCandidates, rt)
					}
```

Replace the endgame pick (the three lines from `idx := rand.Intn(len(genesisCandidates))` to the `slog.Debug("Sending offer for genesis message", ...)`) with:

```go
			replyTo := pickGenesis(genesisCandidates, roomCandidates)
			slog.Debug("Sending offer for genesis message", "total", len(genesisCandidates), "patience", options.Patience)
```

Append to `consumer.go`:

```go
// newSubscribeRequest builds a consumer's GET of the genesis stream. It
// carries the consumer's room tickets in RoomTicketsHeader when it has any,
// and is otherwise unchanged from releases before rooms.
func newSubscribeRequest(ctx context.Context, options *WebRTCOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", options.DiscoverySrv+options.Endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add(common.VersionHeader, common.Version)
	if tickets := options.CurrentRoomTickets(); len(tickets) > 0 {
		req.Header.Set(common.RoomTicketsHeader, strings.Join(tickets, ","))
	}
	return req, nil
}

// pickGenesis picks the genesis message to answer: a random one of those
// delivered through one of our rooms if there are any, and a random one of
// all otherwise.
func pickGenesis(all, room []string) string {
	if len(room) > 0 {
		return room[rand.Intn(len(room))]
	}
	return all[rand.Intn(len(all))]
}
```

`consumer.go` already imports `context`, `math/rand`, `net/http` and `strings`, and `producer.go` already imports `net/url` and `strconv`. Check that nothing became unused:

Run: `go vet ./clientcore/`
Expected: no output.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./clientcore/ ./common/... && GOOS=js GOARCH=wasm go build -o /dev/null ./cmd`
Expected: PASS, including the existing consumer and worker tests; the wasm build succeeds.

- [ ] **Step 5: Commit**

```bash
git add clientcore/producer.go clientcore/consumer.go clientcore/rooms_test.go
git commit -m "clientcore: send room tickets and the pool preference, and prefer room genesis"
```

---

### Task 4: unbounded — freddie `byRoom` index and room-first `DeliverGenesis`

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-rooms`

**Files:**
- Modify: `freddie/freddie.go` (`consumerIndex`, lines 124-202)
- Modify: `freddie/freddie_test.go` (index tests)

**Interfaces:**
- Consumes: `consumerIndex`, `BroadcastGenesis` and the `genesis*` outcomes.
- Produces:
  - `func (t *consumerIndex) AddConsumer(userID, country string, rooms []string) chan string`, with `AddWithCountry` becoming `AddConsumer(userID, country, nil)`
  - `func (t *consumerIndex) broadcastToRooms(msg string, rooms []string) int`
  - `func (t *consumerIndex) DeliverGenesis(msg, roomMsg string, rooms []string, servePool bool, targets []string) (roomMatched bool, outcome string)`. `outcome` is `BroadcastGenesis`'s result, or `""` when the pool step did not run.

- [ ] **Step 1: Write the failing tests**

Append to `freddie/freddie_test.go`:

```go
// recv returns the message waiting on ch, or "" without blocking.
func recv(ch chan string) string {
	select {
	case m := <-ch:
		return m
	default:
		return ""
	}
}

// Precedence per genesis message: room, then (only when the producer serves
// the pool) country, then everyone.
func TestConsumerIndex_RoomsFirst(t *testing.T) {
	idx := newConsumerIndex(1)
	paired := idx.AddConsumer("paired", "RU", []string{"room-a"})
	ir := idx.AddConsumer("ir", "IR", nil)
	other := idx.AddWithCountry("other", "")

	matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, true, []string{"IR"})
	if !matched || outcome != "" {
		t.Fatalf("room-a: matched=%v outcome=%q, want a room match and no pool step", matched, outcome)
	}
	if got := recv(paired); got != "r" {
		t.Errorf("paired consumer got %q, want the room message", got)
	}
	if received(ir) || received(other) {
		t.Error("a room match still reached pool consumers")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, true, []string{"IR"})
	if matched || outcome != genesisMatched {
		t.Fatalf("room-b, IR: matched=%v outcome=%q, want the country step", matched, outcome)
	}
	if recv(ir) != "m" || received(paired) || received(other) {
		t.Error("with nobody in room-b the IR consumer, and only it, should get the plain message")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, true, []string{"CN"})
	if matched || outcome != genesisFallback {
		t.Fatalf("room-b, CN: matched=%v outcome=%q, want the fallback", matched, outcome)
	}
	if recv(paired) != "m" || recv(ir) != "m" || recv(other) != "m" {
		t.Error("the fallback must reach everyone with the plain message, room members included")
	}

	matched, outcome = idx.DeliverGenesis("m", "r", []string{"room-b"}, false, []string{"IR"})
	if matched || outcome != "" {
		t.Fatalf("pool=0, nobody in room-b: matched=%v outcome=%q, want no delivery", matched, outcome)
	}
	matched, outcome = idx.DeliverGenesis("m", "r", nil, false, nil)
	if matched || outcome != "" {
		t.Fatalf("pool=0, no rooms: matched=%v outcome=%q, want no delivery", matched, outcome)
	}
	if received(paired) || received(ir) || received(other) {
		t.Error("a pool=0 genesis with no room consumer reached someone")
	}

	matched, _ = idx.DeliverGenesis("m", "r", []string{"room-a"}, false, nil)
	if !matched || recv(paired) != "r" {
		t.Error("pool=0 must still reach the room")
	}
}

// A room member that can't take the message right now is not a match: a
// producer serving the pool falls back, and one that doesn't reaches nobody.
func TestConsumerIndex_UndeliverableRoomMatch(t *testing.T) {
	idx := newConsumerIndex(1)
	paired := idx.AddConsumer("paired", "", []string{"room-a"})
	pool := idx.AddWithCountry("pool", "")
	paired <- "already full"

	if matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, true, nil); matched || outcome != genesisUntargeted {
		t.Fatalf("matched=%v outcome=%q, want an untargeted pool broadcast", matched, outcome)
	}
	if recv(pool) != "m" {
		t.Error("the pool consumer did not get the fallback")
	}
	if matched, outcome := idx.DeliverGenesis("m", "r", []string{"room-a"}, false, nil); matched || outcome != "" {
		t.Fatalf("pool=0: matched=%v outcome=%q, want nothing", matched, outcome)
	}
	if received(pool) {
		t.Error("a pool=0 genesis reached the pool")
	}
}

// A consumer in several of the producer's rooms gets the message once.
func TestConsumerIndex_RoomMessageOncePerConsumer(t *testing.T) {
	idx := newConsumerIndex(2)
	ch := idx.AddConsumer("u", "", []string{"room-a", "room-b"})
	if n := idx.broadcastToRooms("r", []string{"room-a", "room-b"}); n != 1 {
		t.Errorf("delivered %d times, want 1", n)
	}
	if len(ch) != 1 {
		t.Errorf("%d messages queued, want 1", len(ch))
	}
}

func TestConsumerIndex_DeleteCleansRooms(t *testing.T) {
	idx := newConsumerIndex(1)
	idx.AddConsumer("a", "IR", []string{"room-a", "room-b"})
	idx.AddConsumer("b", "", []string{"room-a"})

	idx.Delete("a")
	if _, ok := idx.byRoom["room-b"]; ok {
		t.Error("an empty room was not removed")
	}
	if len(idx.byRoom["room-a"]) != 1 {
		t.Errorf("room-a has %d members, want 1", len(idx.byRoom["room-a"]))
	}
	idx.Delete("b")
	if idx.Size() != 0 || len(idx.rooms) != 0 || len(idx.byRoom) != 0 || len(idx.byCountry) != 0 {
		t.Errorf("index not empty after deleting everyone: size=%d rooms=%d byRoom=%d byCountry=%d",
			idx.Size(), len(idx.rooms), len(idx.byRoom), len(idx.byCountry))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./freddie/ -run 'ConsumerIndex'`
Expected: the build fails with `idx.AddConsumer undefined`, `idx.DeliverGenesis undefined` and `idx.byRoom undefined`.

- [ ] **Step 3: Implement**

In `freddie/freddie.go`, replace the `consumerIndex` doc comment and struct, `newConsumerIndex`, `AddWithCountry` and `Delete` with:

```go
// A consumerIndex is a userTable which also indexes its users by server-derived country, so that
// genesis messages can be steered toward consumers from the countries a producer wants to serve,
// and by helper pairing room, so that a producer's genesis messages reach the consumers it is
// paired with first. Users whose country is unknown, or who are in no room, are still in the
// table and still receive untargeted and fallback broadcasts.
//
// Rooms are opaque pairing IDs from verified tickets. They are never logged, traced or counted.
type consumerIndex struct {
	userTable
	country   map[string]string              // userID -> country, only for users whose country is known
	byCountry map[string]map[string]struct{} // country -> set of userIDs
	rooms     map[string][]string            // userID -> rooms, only for users in at least one
	byRoom    map[string]map[string]struct{} // room -> set of userIDs
}

func newConsumerIndex(bufferSz int) *consumerIndex {
	return &consumerIndex{
		userTable: userTable{Data: make(map[string]chan string), BufferSz: bufferSz},
		country:   make(map[string]string),
		byCountry: make(map[string]map[string]struct{}),
		rooms:     make(map[string][]string),
		byRoom:    make(map[string]map[string]struct{}),
	}
}

// AddWithCountry is AddConsumer in no room.
func (t *consumerIndex) AddWithCountry(userID, country string) chan string {
	return t.AddConsumer(userID, country, nil)
}

// AddConsumer is Add, plus indexing userID under country and under each of rooms. country must
// already be normalized; "" means the user's country is unknown and it is not indexed. rooms must
// come from verified tickets.
func (t *consumerIndex) AddConsumer(userID, country string, rooms []string) chan string {
	t.Lock()
	defer t.Unlock()
	t.Data[userID] = make(chan string, t.BufferSz)
	if country != "" {
		t.country[userID] = country
		users, ok := t.byCountry[country]
		if !ok {
			users = make(map[string]struct{})
			t.byCountry[country] = users
		}
		users[userID] = struct{}{}
	}
	if len(rooms) > 0 {
		t.rooms[userID] = rooms
		for _, room := range rooms {
			users, ok := t.byRoom[room]
			if !ok {
				users = make(map[string]struct{})
				t.byRoom[room] = users
			}
			users[userID] = struct{}{}
		}
	}
	return t.Data[userID]
}

func (t *consumerIndex) Delete(userID string) {
	t.Lock()
	defer t.Unlock()
	delete(t.Data, userID)
	if country, ok := t.country[userID]; ok {
		delete(t.country, userID)
		delete(t.byCountry[country], userID)
		if len(t.byCountry[country]) == 0 {
			delete(t.byCountry, country)
		}
	}
	for _, room := range t.rooms[userID] {
		delete(t.byRoom[room], userID)
		if len(t.byRoom[room]) == 0 {
			delete(t.byRoom, room)
		}
	}
	delete(t.rooms, userID)
}
```

After `BroadcastGenesis`, add:

```go
// broadcastToRooms sends msg, with Broadcast's unreliable delivery semantics, to every user indexed
// under one of rooms, at most once per user. It returns the number of users the message was
// delivered to.
func (t *consumerIndex) broadcastToRooms(msg string, rooms []string) int {
	t.RLock()
	defer t.RUnlock()
	delivered := 0
	seen := make(map[string]struct{})
	for _, room := range rooms {
		for userID := range t.byRoom[room] {
			if _, dup := seen[userID]; dup {
				continue
			}
			seen[userID] = struct{}{}
			select {
			case t.Data[userID] <- msg:
				delivered++
			default:
			}
		}
	}
	return delivered
}

// DeliverGenesis delivers a genesis message from a producer in rooms. A waiting consumer in one of
// rooms comes first: if any takes roomMsg, nobody else gets the genesis. Otherwise, if servePool, it
// goes on to BroadcastGenesis(msg, targets), the country-then-everyone step. Without servePool it
// goes nowhere: the producer waits out its TTL and its slot loops, so a waiting room consumer gets
// the next free slot of a producer before any pool consumer does. roomMatched reports whether a
// room consumer took it; outcome is BroadcastGenesis's, or "" when the pool step did not run.
func (t *consumerIndex) DeliverGenesis(msg, roomMsg string, rooms []string, servePool bool, targets []string) (roomMatched bool, outcome string) {
	if len(rooms) > 0 && t.broadcastToRooms(roomMsg, rooms) > 0 {
		return true, ""
	}
	if !servePool {
		return false, ""
	}
	return false, t.BroadcastGenesis(msg, targets)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./freddie/`
Expected: PASS for the four new tests and every existing freddie test (`TestConsumerIndex_*`, `TestHandleSignal_*`).

- [ ] **Step 5: Commit**

```bash
git add freddie/freddie.go freddie/freddie_test.go
git commit -m "freddie: index consumers by helper pairing room and deliver genesis room-first"
```

---

### Task 5: unbounded — freddie ticket verification, `pool`, CORS and telemetry

**Repo / worktree:** unbounded, `~/go/src/github.com/getlantern/unbounded-rooms`

**Files:**
- Modify: `freddie/freddie.go` (the `Freddie` struct, `New`, `handleSignal`, `handleSignalGet`, `handleSignalPost`)
- Modify: `freddie/freddie_test.go` (extract `startTestServer` from `newTestServer`)
- Create: `freddie/rooms_test.go`

**Interfaces:**
- Consumes: `helperticket` (Task 1), the `common` room fields (Task 2), and `DeliverGenesis` and `AddConsumer` (Task 4).
- Produces:
  - `Freddie.RoomTicket func(ticket string, role helperticket.Role) (room string, ok bool)`
  - the counter `freddie.genesis.room_match{matched}`
  - `X-BF-Room-Tickets` in the CORS allow-list
  - test helpers: `startTestServer(t, hook) *testServer`, with the methods `subscribe(country, query string, tickets ...string)`, `genesis(form url.Values)`, `expect`, `waitForConsumers` and `hangUp`. `newTestServer` keeps its signature and delegates to it, so the existing tests are unchanged.

- [ ] **Step 1: Extract `startTestServer`**

In `freddie/freddie_test.go`, replace `newTestServer` (lines 117-220) with:

```go
// testServer is Freddie over HTTP with a fresh consumer table.
type testServer struct {
	t      *testing.T
	f      *Freddie
	url    string
	ctx    context.Context
	cancel context.CancelFunc
}

// startTestServer starts Freddie over HTTP with a fresh consumer table and the given country hook.
// Set s.f.RoomTicket before subscribing to enable rooms.
func startTestServer(t *testing.T, hook func(*http.Request) string) *testServer {
	t.Helper()
	consumerTable = newConsumerIndex(0)

	f, err := New(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	f.ConsumerCountry = hook
	srv := httptest.NewServer(f.srv.Handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &testServer{t: t, f: f, url: srv.URL, ctx: ctx, cancel: cancel}
}

// subscribe opens a consumer's genesis stream and returns the messages it receives. tickets, if
// any, go comma-separated in the room tickets header.
func (s *testServer) subscribe(country, query string, tickets ...string) chan string {
	u := s.url + "/v1/signal"
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set(common.VersionHeader, common.Version)
	if country != "" {
		req.Header.Set(testCountryHeader, country)
	}
	if len(tickets) > 0 {
		req.Header.Set(common.RoomTicketsHeader, strings.Join(tickets, ","))
	}
	msgs := make(chan string, 16)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			msgs <- scanner.Text()
		}
	}()
	return msgs
}

// genesis posts a genesis message with extra's fields added to the required ones.
func (s *testServer) genesis(extra url.Values) {
	form := url.Values{
		"data":    {"{}"},
		"send-to": {"genesis"},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	for k, vs := range extra {
		for _, v := range vs {
			form.Add(k, v)
		}
	}
	// Nobody answers a genesis message here, so don't wait out its TTL
	pctx, pcancel := context.WithTimeout(s.ctx, 200*time.Millisecond)
	defer pcancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, s.url+"/v1/signal",
		strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(common.VersionHeader, common.Version)
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
	}
}

func (s *testServer) expect(name string, ch chan string, want bool) {
	s.t.Helper()
	select {
	case <-ch:
		if !want {
			s.t.Errorf("%s consumer received a genesis message it should not have", name)
		}
	case <-time.After(300 * time.Millisecond):
		if want {
			s.t.Errorf("%s consumer did not receive a genesis message", name)
		}
	}
}

func (s *testServer) waitForConsumers(n int) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for consumerTable.Size() < n {
		if time.Now().After(deadline) {
			s.t.Fatal("consumers never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let each handler park in its select loop, since unbuffered broadcast only reaches parked
	// receivers
	time.Sleep(100 * time.Millisecond)
}

// hangUp ends every open subscription and waits for Freddie to drop them.
func (s *testServer) hangUp() {
	s.t.Helper()
	s.cancel()
	deadline := time.Now().Add(5 * time.Second)
	for consumerTable.Size() > 0 {
		if time.Now().After(deadline) {
			s.t.Fatal("subscriptions never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The handler's span ends after its deferred Delete
	time.Sleep(50 * time.Millisecond)
}

// newTestServer starts Freddie over HTTP with a fresh consumer table and the given hook, and
// returns helpers to subscribe consumers and post genesis messages.
func newTestServer(t *testing.T, hook func(*http.Request) string) (
	subscribe func(country, query string) chan string,
	genesis func(targets ...string),
	expect func(name string, ch chan string, want bool),
	waitForConsumers func(n int),
) {
	t.Helper()
	s := startTestServer(t, hook)
	subscribe = func(country, query string) chan string { return s.subscribe(country, query) }
	genesis = func(targets ...string) {
		form := url.Values{}
		for _, c := range targets {
			form.Add(common.TargetCountryField, c)
		}
		s.genesis(form)
	}
	return subscribe, genesis, s.expect, s.waitForConsumers
}
```

Run: `go test -race ./freddie/`
Expected: PASS. The refactor changes no behavior.

- [ ] **Step 2: Write the failing handler and telemetry tests**

`freddie/rooms_test.go`:

```go
package freddie

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

const (
	pairingP = "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"
	pairingQ = "0b6f3a52-2d4e-4f7a-8c1d-9e0f1a2b3c4d"
	ticketOK = 15 * time.Minute
)

// roomKeys is a ticket signing key standing in for lantern-cloud's.
type roomKeys struct {
	t    *testing.T
	priv ed25519.PrivateKey
}

func newRoomKeys(t *testing.T) *roomKeys {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &roomKeys{t: t, priv: priv}
}

// ticket signs a ticket for pairing in role that expires after ttl; a negative ttl has expired.
func (k *roomKeys) ticket(pairing string, role helperticket.Role, ttl time.Duration) string {
	k.t.Helper()
	tk, err := helperticket.Sign(k.priv, helperticket.Ticket{PairingID: pairing, Role: role, Expires: time.Now().Add(ttl)})
	if err != nil {
		k.t.Fatal(err)
	}
	return tk
}

// startRoomServer is startTestServer with rooms verified against k's key.
func startRoomServer(t *testing.T, k *roomKeys) *testServer {
	t.Helper()
	s := startTestServer(t, headerHook)
	key := helperticket.NewKey(k.priv.Public().(ed25519.PublicKey))
	s.f.RoomTicket = helperticket.RoomFunc(func() []helperticket.Key { return []helperticket.Key{key} }, time.Now)
	return s
}

// roomGenesis posts a genesis message carrying volunteer tickets, with pool=0 unless pool, and
// target countries.
func (s *testServer) roomGenesis(tickets []string, pool bool, targets ...string) {
	form := url.Values{}
	for _, tk := range tickets {
		form.Add(common.RoomTicketField, tk)
	}
	if !pool {
		form.Set(common.PoolField, "0")
	}
	for _, c := range targets {
		form.Add(common.TargetCountryField, c)
	}
	s.genesis(form)
}

// expectFrom is expect for a message that must arrive, through a room (room) or the pool (!room).
func (s *testServer) expectFrom(name string, ch chan string, room bool) {
	s.t.Helper()
	select {
	case raw := <-ch:
		if got := common.IsRoomGenesis([]byte(raw)); got != room {
			s.t.Errorf("%s consumer: room=%v, want %v", name, got, room)
		}
	case <-time.After(300 * time.Millisecond):
		s.t.Errorf("%s consumer did not receive a genesis message", name)
	}
}

// Room, then country, then everyone, per genesis message, for a volunteer that serves the pool.
func TestHandleSignal_RoomPrecedence(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	ir := s.subscribe("IR", "")
	other := s.subscribe("", "")
	s.waitForConsumers(3)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	vQ := k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)

	// The consumer in the volunteer's room takes it, even over a country match.
	s.roomGenesis([]string{vP}, true, "IR")
	s.expectFrom("paired", paired, true)
	s.expect("IR", ir, false)
	s.expect("other", other, false)

	// Nobody waits in room Q, so country targeting applies.
	s.roomGenesis([]string{vQ}, true, "IR")
	s.expectFrom("IR", ir, false)
	s.expect("paired", paired, false)
	s.expect("other", other, false)

	// Nor in the target country, so everyone gets the plain message.
	s.roomGenesis([]string{vQ}, true, "CN")
	s.expectFrom("paired", paired, false)
	s.expectFrom("IR", ir, false)
	s.expectFrom("other", other, false)
}

// pool=0: the room or nobody.
func TestHandleSignal_PoolOffHasNoFallback(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	paired := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	pool := s.subscribe("IR", "")
	s.waitForConsumers(2)

	s.roomGenesis([]string{k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, false, "IR")
	s.expect("paired", paired, false)
	s.expect("pool", pool, false)

	s.roomGenesis(nil, false)
	s.expect("paired", paired, false)
	s.expect("pool", pool, false)

	s.roomGenesis([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, false)
	s.expectFrom("paired", paired, true)
	s.expect("pool", pool, false)
}

// Invalid, expired, wrong-role and foreign tickets are ignored on both sides, without spoiling
// valid ones beside them.
func TestHandleSignal_BadRoomTicketsIgnored(t *testing.T) {
	k := newRoomKeys(t)
	stranger := newRoomKeys(t)
	s := startRoomServer(t, k)
	good := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	valid := s.subscribe("", "", good)
	bad := map[string]chan string{
		"expired":    s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, -time.Minute)),
		"wrong role": s.subscribe("", "", k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)),
		"wrong key":  s.subscribe("", "", stranger.ticket(pairingP, helperticket.RoleInviter, ticketOK)),
		"garbage":    s.subscribe("", "", "not-a-ticket", "f"+good[1:]),
	}
	s.waitForConsumers(5)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)

	s.roomGenesis([]string{vP}, false)
	s.expectFrom("valid", valid, true)
	for name, ch := range bad {
		s.expect(name, ch, false)
	}

	// A volunteer's own bad tickets admit it to no room, so with pool=0 nobody gets its genesis.
	for name, tk := range map[string]string{
		"expired":    k.ticket(pairingP, helperticket.RoleVolunteer, -time.Minute),
		"wrong role": k.ticket(pairingP, helperticket.RoleInviter, ticketOK),
		"wrong key":  stranger.ticket(pairingP, helperticket.RoleVolunteer, ticketOK),
	} {
		s.roomGenesis([]string{tk}, false)
		s.expect("valid, volunteer ticket "+name, valid, false)
	}

	s.roomGenesis([]string{"junk", k.ticket(pairingP, helperticket.RoleVolunteer, -time.Minute), vP}, false)
	s.expectFrom("valid, beside bad tickets", valid, true)
}

// Without a RoomTicket hook tickets are ignored: everyone is in the pool, and a pool=0 producer
// reaches nobody.
func TestHandleSignal_NoRoomHook(t *testing.T) {
	k := newRoomKeys(t)
	s := startTestServer(t, headerHook)
	paired := s.subscribe("", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)
	vP := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	s.roomGenesis([]string{vP}, false)
	s.expect("paired", paired, false)
	s.roomGenesis([]string{vP}, true)
	s.expectFrom("paired", paired, false)
}

func TestHandleSignal_CORSAllowsRoomTickets(t *testing.T) {
	s := startTestServer(t, nil)
	req, err := http.NewRequest(http.MethodOptions, s.url+"/v1/signal", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, common.RoomTicketsHeader) {
		t.Errorf("Access-Control-Allow-Headers %q lacks %s", got, common.RoomTicketsHeader)
	}
}

// No ticket or pairing ID reaches a log, a span or a metric, and the one room metric carries only
// a boolean.
func TestHandleSignal_RoomsLeaveNoTrace(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	prevTP, prevMP, prevLog := otel.GetTracerProvider(), otel.GetMeterProvider(), slog.Default()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
		slog.SetDefault(prevLog)
	})

	k := newRoomKeys(t)
	s := startRoomServer(t, k) // after the providers, so New picks them up
	inv := k.ticket(pairingP, helperticket.RoleInviter, ticketOK)
	expired := k.ticket(pairingP, helperticket.RoleInviter, -time.Minute)
	vol := k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)
	volQ := k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)
	paired := s.subscribe("", "", inv, expired)
	s.waitForConsumers(1)
	s.roomGenesis([]string{vol}, false)
	s.expectFrom("paired", paired, true)
	s.roomGenesis([]string{volQ, "junk"}, true)
	s.expectFrom("paired", paired, false)
	s.hangUp()
	// Each genesis POST waits defaultMsgTTL for an offer nobody sends here, and its span ends
	// only then.
	time.Sleep((defaultMsgTTL + 1) * time.Second)

	secrets := []string{pairingP, pairingQ, inv, expired, vol, volQ}
	check := func(where, text string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains a ticket or pairing ID: %q", where, text)
			}
		}
	}
	ended := spans.Ended()
	if len(ended) == 0 {
		t.Fatal("no spans recorded")
	}
	for _, sp := range ended {
		for _, kv := range sp.Attributes() {
			check("span "+sp.Name()+" attribute "+string(kv.Key), kv.Value.Emit())
		}
		for _, ev := range sp.Events() {
			check("span "+sp.Name()+" event", ev.Name)
			for _, kv := range ev.Attributes {
				check("span "+sp.Name()+" event attribute "+string(kv.Key), kv.Value.Emit())
			}
		}
		check("span "+sp.Name()+" status", sp.Status().Description)
	}
	check("log", logs.String())

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	roomMatches := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					check("metric "+m.Name+" attribute "+string(kv.Key), kv.Value.Emit())
				}
				if m.Name == "freddie.genesis.room_match" {
					roomMatches += int(dp.Value)
					if attrs := dp.Attributes.ToSlice(); len(attrs) != 1 || attrs[0].Key != "matched" {
						t.Errorf("room_match carries %v, want only matched", attrs)
					}
				}
			}
		}
	}
	if roomMatches != 2 {
		t.Errorf("room_match counted %d genesis messages, want 2 (one matched, one not)", roomMatches)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./freddie/ -run 'Room|CORS'`
Expected: the build fails with `s.f.RoomTicket undefined (type *Freddie has no field or method RoomTicket)`.

- [ ] **Step 4: Implement**

In `freddie/freddie.go`, add `"slices"` and `"github.com/getlantern/broflake/common/helperticket"` to the imports. Add the hook to `Freddie` after `ConsumerCountry`:

```go
	// RoomTicket, if non-nil, verifies a helper pairing ticket presented for role and returns the
	// room it admits to, the pairing's opaque ID, or ok=false. A consumer's inviter tickets, in
	// common.RoomTicketsHeader, put it in their rooms; a producer's volunteer tickets, in
	// common.RoomTicketField, send its genesis messages to those rooms' consumers first. It must
	// check the signature, the key, the expiry and the role, and must not log, trace or count the
	// ticket or the room. helperticket.RoomFunc is one. nil disables rooms: tickets are ignored, so
	// a producer that sends pool=0 is offered to nobody. Set it after New and before serving; it is
	// read without synchronization by concurrent request handlers.
	RoomTicket func(ticket string, role helperticket.Role) (room string, ok bool)
```

Add `roomMatches metric.Int64Counter` after `consumerSubs` in the struct. In `New`, after creating `f.consumerSubs`, add:

```go
	f.roomMatches, err = f.meter.Int64Counter("freddie.genesis.room_match",
		metric.WithDescription("genesis messages from producers with valid room tickets, by whether a consumer in one of their rooms took it"),
		metric.WithUnit("message"))
	if err != nil {
		return nil, err
	}
```

In `handleSignal`, change the allow-headers line to:

```go
				"Access-Control-Request-Method, Access-Control-Request-Headers, "+common.VersionHeader+", "+
				common.RoomTicketsHeader,
```

Add a method after `handleSignal`:

```go
// verifyRoomTickets returns the distinct rooms that tickets, already normalized, admit a client in
// role to. Invalid tickets are dropped without a trace: neither they nor the rooms are logged,
// traced or counted.
func (f *Freddie) verifyRoomTickets(tickets []string, role helperticket.Role) []string {
	if f.RoomTicket == nil {
		return nil
	}
	var rooms []string
	for _, tk := range tickets {
		if room, ok := f.RoomTicket(tk, role); ok && room != "" && !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	return rooms
}
```

In `handleSignalGet`, replace `consumerChan := consumerTable.AddWithCountry(consumerID, country)` with:

```go
	// Verified at every subscribe, so an ended pairing's consumer leaves its room within one
	// ticket lifetime plus one consumerTTL. Rooms are never put on the span.
	rooms := f.verifyRoomTickets(common.ParseRoomTicketsHeader(r.Header.Values(common.RoomTicketsHeader)), helperticket.RoleInviter)
	consumerChan := consumerTable.AddConsumer(consumerID, country, rooms)
```

In `handleSignalPost`, replace the `if sendTo == "genesis" { ... }` branch (lines 485-494) with:

```go
	if sendTo == "genesis" {
		// It's a genesis message. Producers' rooms come first; then, unless the producer serves only
		// its rooms, its target countries; then everyone. See consumerIndex.DeliverGenesis.
		rooms := f.verifyRoomTickets(common.NormalizeRoomTickets(r.Form[common.RoomTicketField]), helperticket.RoleVolunteer)
		servePool := r.Form.Get(common.PoolField) != "0"
		targets := common.NormalizeCountries(r.Form[common.TargetCountryField])
		var roomMsg []byte
		if len(rooms) > 0 {
			roomMsg, err = json.Marshal(common.SignalMsg{ReplyTo: reqID, Type: common.SignalMsgType(msgType), Payload: data, Room: true})
			if err != nil {
				span.SetStatus(codes.Error, "malformed request")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("400\n"))
				return
			}
		}
		matched, outcome := consumerTable.DeliverGenesis(string(msg), string(roomMsg), rooms, servePool, targets)
		if len(rooms) > 0 {
			f.roomMatches.Add(ctx, 1, metric.WithAttributes(attribute.Bool("matched", matched)))
		}
		if outcome != "" {
			span.SetAttributes(
				attribute.StringSlice("genesis.target_countries", targets),
				attribute.String("genesis.targeting", outcome),
			)
			f.genesisBroadcasts.Add(ctx, 1, metric.WithAttributes(attribute.String("targeting", outcome)))
		}
	} else {
```

The `else` branch and everything after it are unchanged. A consumer that hangs up leaves its rooms through the existing deferred `consumerTable.Delete`, the same way it leaves its country.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./freddie/ -v -run 'Room|CORS|PoolOff|BadRoom' && go test -race ./... && GOOS=js GOARCH=wasm go build -o /dev/null ./cmd`
Expected: PASS for `TestHandleSignal_RoomPrecedence`, `_PoolOffHasNoFallback`, `_BadRoomTicketsIgnored`, `_NoRoomHook`, `_CORSAllowsRoomTickets` and `_RoomsLeaveNoTrace`. The whole module passes under `-race`, and the wasm build succeeds.

- [ ] **Step 6: Telemetry audit**

Run: `git diff origin/main -- freddie/ clientcore/ common/ | grep -nE '^\+.*(slog\.|SetAttributes|attribute\.|AddEvent|RecordError)'`
Expected: the only added lines are the `room_match` counter with `attribute.Bool("matched", …)`, the unchanged `genesis.target_countries` and `genesis.targeting` attributes (moved inside `if outcome != ""`), and the consumer's `"Sending offer for genesis message"` debug line with `total` and `patience`. None carries a ticket or room.

- [ ] **Step 7: Commit and open the PR**

```bash
git add freddie/freddie.go freddie/freddie_test.go freddie/rooms_test.go
git commit -m "freddie: helper pairing rooms from verified tickets, with pool=0 and no traces"
```

Open the PR against `main` with `skills:create-pr`. Its description carries the architecture diagram from this plan's header, and says that a consumer or producer without tickets is byte-identical on the wire.

- [ ] **Step 8: [Human] Merge the PR to `main`.** broflake is consumed by pseudo-version and needs no tag. Record the merge commit for Tasks 9, 10 and 12:

```bash
git -C ~/go/src/github.com/getlantern/unbounded fetch origin
export UB_SHA=$(git -C ~/go/src/github.com/getlantern/unbounded rev-parse origin/main)
echo "$UB_SHA"
```

---

### Task 6: lantern-cloud — key ID in tickets, and the shared vectors

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-rooms`

**Files:**
- Modify: `cmd/api/pairing/ticket.go`
- Modify: `cmd/api/pairing/ticket_test.go`
- Modify: `cmd/api/pairing/invite_test.go` (`TestVectors`)
- Modify: `cmd/api/pairing/testdata/vectors.json` (regenerated)

**Interfaces:**
- Consumes: plan 1's `SignTicket`, `VerifyTicket` and `ticketWire`, and the broflake copy of the vectors (Task 1).
- Produces:
  - `func TicketKeyID(pub ed25519.PublicKey) string`
  - `Ticket.KeyID`, filled by `VerifyTicket` and ignored by `SignTicket`, which names the signing key
  - `VerifyTicket` rejects a ticket whose `"k"` is not its key's ID
  - the vectors' `ticket` section, byte-identical in meaning to `common/helperticket/testdata/vectors.json`

- [ ] **Step 1: Check plan 2's branch, then create the worktree**

PR #3675 is being rebased while this plan is written. Base on whatever is pushed, never on a local copy:

```bash
git -C ~/go/src/github.com/getlantern/lantern-cloud fetch origin
gh pr view 3675 -R getlantern/lantern-cloud --json state,headRefName,headRefOid,baseRefName
git -C ~/go/src/github.com/getlantern/lantern-cloud rev-parse origin/fisk/helper-direct-mode
git -C ~/go/src/github.com/getlantern/lantern-cloud-helper-direct status -sb | head -1
```

Expected: `state` is `OPEN`, and `headRefOid` equals the `rev-parse` output. If the plan-2 worktree's status line says `ahead` or `diverged`, the rebase has not been pushed yet. Wait for it, and re-run these commands. If `state` is `MERGED`, base on `origin/main` instead.

```bash
git -C ~/go/src/github.com/getlantern/lantern-cloud worktree add ~/go/src/github.com/getlantern/lantern-cloud-helper-rooms -b fisk/helper-rooms origin/fisk/helper-direct-mode
cd ~/go/src/github.com/getlantern/lantern-cloud-helper-rooms
grep -n 'func SignTicket\|func VerifyTicket\|type ticketWire' cmd/api/pairing/ticket.go
grep -n 'func pairingSetRevision\|func loadHelperTicketKey\|func (s helperService) TicketKey' cmd/api/*.go
```

Expected: each function exists once. After the rebase the line numbers may differ from the ones quoted in Tasks 6–8, so place edits by the anchors these commands print.

- [ ] **Step 2: Write the failing tests**

In `cmd/api/pairing/ticket_test.go`, add after `testTicketKey`:

```go
// testPreviousTicketKey stands for the key testTicketKey replaced in a rotation.
func testPreviousTicketKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
}

func TestTicketKeyID(t *testing.T) {
	assert.Equal(t, "dbc29825", TicketKeyID(testTicketKey().Public().(ed25519.PublicKey)))
	assert.Equal(t, "5c29b78f", TicketKeyID(testPreviousTicketKey().Public().(ed25519.PublicKey)))
}

// A ticket names its key, so a verifier holding two keys mid-rotation knows
// which one to use; one naming another key is refused even if the signature
// verifies.
func TestVerifyTicketChecksKeyID(t *testing.T) {
	priv := testTicketKey()
	pub := priv.Public().(ed25519.PublicKey)
	now := time.Unix(1_800_000_000, 0)
	tok, err := SignTicket(priv, Ticket{Version: 1, PairingID: uuid.New(), Role: RoleInviter, Expires: now.Add(time.Minute)})
	require.NoError(t, err)
	got, err := VerifyTicket(pub, tok, now)
	require.NoError(t, err)
	assert.Equal(t, "dbc29825", got.KeyID)

	payload := []byte(`{"v":1,"k":"00000000","p":"6f1c2b0e-8a7d-4c1e-9f00-0123456789ab","r":"inviter","e":1800000060}`)
	forged := b64.EncodeToString(payload) + "." + b64.EncodeToString(ed25519.Sign(priv, payload))
	_, err = VerifyTicket(pub, forged, now)
	assert.ErrorIs(t, err, ErrTicketInvalid)
}
```

In `TestTicketPayloadCarriesNoDeviceID`, change the expected payload to:

```go
	assert.Equal(t, `{"v":1,"k":"dbc29825","p":"6f1c2b0e-8a7d-4c1e-9f00-0123456789ab","r":"volunteer","e":1800000900}`, string(payload))
```

In `cmd/api/pairing/invite_test.go`, add `"strings"` to the imports, and replace the block from `tpriv := testTicketKey()` through the closing brace of `ticketVec := map[string]string{...}` with:

```go
	tpriv := testTicketKey()
	ppriv := testPreviousTicketKey()
	ticketPairing := uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab")
	ticketExp := time.Unix(1_800_000_900, 0)
	signVec := func(priv ed25519.PrivateKey, role Role) string {
		tok, err := SignTicket(priv, Ticket{Version: 1, PairingID: ticketPairing, Role: role, Expires: ticketExp})
		require.NoError(t, err)
		return tok
	}
	tok := signVec(tpriv, RoleVolunteer)
	encPayload, _, _ := strings.Cut(tok, ".")
	ticketPayload, err := b64.DecodeString(encPayload)
	require.NoError(t, err)
	tpub := tpriv.Public().(ed25519.PublicKey)
	ppub := ppriv.Public().(ed25519.PublicKey)
	// broflake's common/helperticket/testdata/vectors.json is a copy of this
	// section; freddie verifies tickets against it.
	ticketVec := map[string]string{
		"public_key_hex":          hex.EncodeToString(tpub),
		"kid":                     TicketKeyID(tpub),
		"pairing_id":              ticketPairing.String(),
		"payload_json":            string(ticketPayload),
		"token":                   tok,
		"inviter_token":           signVec(tpriv, RoleInviter),
		"previous_public_key_hex": hex.EncodeToString(ppub),
		"previous_kid":            TicketKeyID(ppub),
		"previous_token":          signVec(ppriv, RoleVolunteer),
		"verify_at_unix":          "1800000000",
		"expired_at_unix":         "1800000900",
	}
```

The `sidKey := PeerSIDKey(tpriv.Seed())` line and the rest of the function stay as they are.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd cmd/api && go test ./pairing/ -run 'Ticket|Vectors' -v`
Expected: the build fails with `undefined: TicketKeyID` and `got.KeyID undefined`.

- [ ] **Step 4: Implement**

In `cmd/api/pairing/ticket.go`, add `"crypto/sha256"` and `"encoding/hex"` to the imports. Replace everything from `type Ticket struct` to the end of the file (`Ticket`, `ticketWire`, `b64`, `SignTicket` and `VerifyTicket`) with:

```go
// Ticket says its holder may use PairingID, in Role, until Expires. It names
// no device: verifiers learn only the opaque pairing ID, never a credential
// they could replay against the API. KeyID is the ID of the key that signed
// it; SignTicket ignores it and names the key it signs with.
type Ticket struct {
	Version   int
	KeyID     string
	PairingID uuid.UUID
	Role      Role
	Expires   time.Time
}

// ticketWire is the payload. Its field order is the wire format's key order.
type ticketWire struct {
	V int    `json:"v"`
	K string `json:"k"`
	P string `json:"p"`
	R Role   `json:"r"`
	E int64  `json:"e"`
}

var b64 = base64.RawURLEncoding

// TicketKeyID names a ticket key: the first 8 hex characters of SHA-256 of
// the raw public key. A ticket carries its signing key's ID as "k", so a
// verifier holding the current and previous keys mid-rotation knows which to
// use, and GET /v1/helper/ticket-key lists keys under these IDs.
func TicketKeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// SignTicket encodes and signs t.
func SignTicket(priv ed25519.PrivateKey, t Ticket) (string, error) {
	if t.Role != RoleInviter && t.Role != RoleVolunteer {
		return "", fmt.Errorf("pairing: unknown ticket role %q", t.Role)
	}
	kid := TicketKeyID(priv.Public().(ed25519.PublicKey))
	payload, err := json.Marshal(ticketWire{V: t.Version, K: kid, P: t.PairingID.String(), R: t.Role, E: t.Expires.Unix()})
	if err != nil {
		return "", fmt.Errorf("pairing: encode ticket: %w", err)
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// VerifyTicket checks token's signature, that it names pub's key, and its
// expiry at now.
func VerifyTicket(pub ed25519.PublicKey, token string, now time.Time) (Ticket, error) {
	enc, encSig, ok := strings.Cut(token, ".")
	if !ok || len(pub) != ed25519.PublicKeySize {
		return Ticket{}, ErrTicketInvalid
	}
	payload, err1 := b64.DecodeString(enc)
	sig, err2 := b64.DecodeString(encSig)
	if err1 != nil || err2 != nil || !ed25519.Verify(pub, payload, sig) {
		return Ticket{}, ErrTicketInvalid
	}
	var w ticketWire
	if err := json.Unmarshal(payload, &w); err != nil || w.V != 1 || w.K != TicketKeyID(pub) {
		return Ticket{}, ErrTicketInvalid
	}
	id, err := uuid.Parse(w.P)
	if err != nil || (w.R != RoleInviter && w.R != RoleVolunteer) {
		return Ticket{}, ErrTicketInvalid
	}
	t := Ticket{Version: w.V, KeyID: w.K, PairingID: id, Role: w.R, Expires: time.Unix(w.E, 0)}
	if !now.Before(t.Expires) {
		return t, ErrTicketExpired
	}
	return t, nil
}
```

- [ ] **Step 5: Regenerate the vectors and run the tests**

```bash
cd cmd/api && go test ./pairing/ -run TestVectors -update && go test ./pairing/ -v
```

Expected: PASS for every pairing test, including `TestTicketKeyID`, `TestVerifyTicketChecksKeyID`, `TestTicketPayloadCarriesNoDeviceID` and `TestVectors`. `git diff --stat` shows `vectors.json` changed only in its `ticket` section: the `invites` and `peer_sid` sections are unchanged.

- [ ] **Step 6: Check the vectors against broflake's copy**

```bash
diff <(jq -S .ticket cmd/api/pairing/testdata/vectors.json) \
     <(jq -S .ticket ~/go/src/github.com/getlantern/unbounded-rooms/common/helperticket/testdata/vectors.json) && echo identical
```

Expected: `identical`. If the unbounded worktree has been removed after Task 5 merged, compare against `git -C ~/go/src/github.com/getlantern/unbounded show origin/main:common/helperticket/testdata/vectors.json` instead. A difference means one side's format drifted: fix the code, never just the file.

- [ ] **Step 7: Commit**

```bash
git add cmd/api/pairing/ticket.go cmd/api/pairing/ticket_test.go cmd/api/pairing/invite_test.go cmd/api/pairing/testdata/vectors.json
git commit -m "pairing: name the signing key in helper tickets"
```

---

### Task 7: lantern-cloud — the ticket key list and `previous_seed`

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-rooms`

**Files:**
- Modify: `cmd/api/apipb/proto/services/helper.proto` (+ generated files, per the proto rule)
- Modify: `cmd/api/helper_handler.go` (`TicketKey`, `loadHelperTicketKey`)
- Modify: `cmd/api/main.go` (the `lanternService` fields at lines 76-80, and the wiring at lines 463-465)
- Modify: `cmd/api/helper_ticket_key_test.go`
- Modify: `cmd/api/helper_handler_db_test.go` (`TestHelperTicketKeyIsPublic`)

**Interfaces:**
- Consumes: `pairing.TicketKeyID` (Task 6), and `helperPeerSIDKeyFor` (plan 2).
- Produces:
  - `type helperTicketKeys struct { current, previous ed25519.PrivateKey; generation uint16 }`
  - `func loadHelperTicketKeys(ctx, readKV) helperTicketKeys`, replacing `loadHelperTicketKey`
  - the `lanternService` fields `helperPreviousTicketKey ed25519.PrivateKey`, `helperKeyGeneration uint16` and `helperPreviousPeerSIDKey []byte`
  - `HelperTicketKey.keys` (`repeated HelperTicketKeyEntry`, with `kid` and `public_key`)

- [ ] **Step 1: Extend the proto**

In `cmd/api/apipb/proto/services/helper.proto`, replace `message HelperTicketKey { ... }` with:

```proto
message HelperTicketKey {
  // public_key is the current key, kept for clients that predate keys.
  bytes public_key = 1;
  // keys are every key a ticket may be verified with: the current one first,
  // then, while a rotation is in flight, the one it replaced.
  repeated HelperTicketKeyEntry keys = 2;
}

message HelperTicketKeyEntry {
  // kid is the key's ID, the "k" of the tickets it verifies: the first 8
  // hex characters of SHA-256 of public_key.
  string kid = 1;
  bytes public_key = 2;
}
```

```bash
make proto
git status --short
```

Revert every changed generated file except the five `helper` files the proto rule lists, for example `git checkout -- openapiv2/services/peer.swagger.json` for each one. Then:

Run: `git status --short`
Expected: only `helper.proto`, `helper.pb.go`, `helper_pb.ts` and `helper.swagger.json` (and `helper_grpc.pb.go` or `helper-HelperService_connectquery.ts` if they changed) are modified.

- [ ] **Step 2: Write the failing tests**

In `cmd/api/helper_ticket_key_test.go`, replace `fakeKVReader`, its `read` method and `TestLoadHelperTicketKey` with:

```go
// fakeKVReader answers loadHelperTicketKeys's reads from errs in turn, then
// with seed and extra.
type fakeKVReader struct {
	errs  []error
	seed  string
	extra map[string]any
	calls int
}

func (f *fakeKVReader) read(_ context.Context, _ string, v any) error {
	f.calls++
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	data := map[string]any{"seed": f.seed}
	for k, val := range f.extra {
		data[k] = val
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, v)
}

func TestLoadHelperTicketKeys(t *testing.T) {
	fastHelperTicketKeyRetries(t)
	seed := base64.StdEncoding.EncodeToString(testTicketSeed())
	prevSeedBytes := bytes.Repeat([]byte{8}, ed25519.SeedSize)
	prevSeed := base64.StdEncoding.EncodeToString(prevSeedBytes)
	want := ed25519.NewKeyFromSeed(testTicketSeed())
	wantPrev := ed25519.NewKeyFromSeed(prevSeedBytes)
	transient := errors.New("connection refused")
	notFound := &vault.PathError{Op: "read", Path: "p", Err: fmt.Errorf("secret not found: %w", vault.ErrNoSecret)}

	for _, c := range []struct {
		name           string
		reader         fakeKVReader
		wantKey        bool
		wantPrev       bool
		wantGeneration uint16
		wantCalls      int
	}{
		{"present", fakeKVReader{seed: seed}, true, false, 0, 1},
		{"transient errors are retried", fakeKVReader{errs: []error{transient, transient}, seed: seed}, true, false, 0, 3},
		{"absent is not retried", fakeKVReader{errs: []error{notFound}, seed: seed}, false, false, 0, 1},
		{"persistent errors give up after the retries", fakeKVReader{errs: []error{transient, transient, transient, transient, transient}, seed: seed}, false, false, 0, 4},
		{"malformed seed is not retried", fakeKVReader{seed: "not base64!"}, false, false, 0, 1},
		{"short seed is not retried", fakeKVReader{seed: base64.StdEncoding.EncodeToString([]byte("short"))}, false, false, 0, 1},
		{"rotation: previous seed and generation as written by vault kv put",
			fakeKVReader{seed: seed, extra: map[string]any{"previous_seed": prevSeed, "generation": "3"}}, true, true, 3, 1},
		{"generation as a JSON number", fakeKVReader{seed: seed, extra: map[string]any{"generation": 4}}, true, false, 4, 1},
		{"empty previous seed and generation", fakeKVReader{seed: seed, extra: map[string]any{"previous_seed": "", "generation": ""}}, true, false, 0, 1},
		{"malformed previous seed is ignored", fakeKVReader{seed: seed, extra: map[string]any{"previous_seed": "nope"}}, true, false, 0, 1},
		{"previous seed equal to the seed is ignored", fakeKVReader{seed: seed, extra: map[string]any{"previous_seed": seed}}, true, false, 0, 1},
		{"malformed generation disables tickets", fakeKVReader{seed: seed, extra: map[string]any{"generation": "x"}}, false, false, 0, 1},
		{"out-of-range generation disables tickets", fakeKVReader{seed: seed, extra: map[string]any{"generation": "70000"}}, false, false, 0, 1},
		{"fractional generation disables tickets", fakeKVReader{seed: seed, extra: map[string]any{"generation": 1.5}}, false, false, 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.reader
			got := loadHelperTicketKeys(context.Background(), r.read)
			if c.wantKey {
				assert.Equal(t, want, got.current)
			} else {
				assert.Equal(t, helperTicketKeys{}, got, "a disabled load leaves nothing behind")
			}
			if c.wantPrev {
				assert.Equal(t, wantPrev, got.previous)
			} else {
				assert.Nil(t, got.previous)
			}
			assert.Equal(t, c.wantGeneration, got.generation)
			assert.Equal(t, c.wantCalls, r.calls)
		})
	}
}
```

In `TestLoadHelperTicketKeyStopsWhenStartupIsCancelled`, change `require.Nil(t, loadHelperTicketKey(ctx, r.read))` to `require.Nil(t, loadHelperTicketKeys(ctx, r.read).current)`. Add `"bytes"` to the file's imports.

In `cmd/api/helper_handler_db_test.go`, replace `TestHelperTicketKeyIsPublic` with:

```go
type ticketKeyJSON struct {
	PublicKey []byte `json:"public_key"`
	Keys      []struct {
		Kid       string `json:"kid"`
		PublicKey []byte `json:"public_key"`
	} `json:"keys"`
}

func getTicketKey(t *testing.T, mux http.Handler) ticketKeyJSON {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/helper/ticket-key", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, "no device id or flag needed: body=%s", w.Body.String())
	var resp ticketKeyJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func TestHelperTicketKeyIsPublic(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	resp := getTicketKey(t, mux)
	pub := []byte(ls.helperTicketKey.Public().(ed25519.PublicKey))
	assert.Equal(t, pub, resp.PublicKey, "the top-level key stays for clients that predate the list")
	require.Len(t, resp.Keys, 1)
	assert.Equal(t, "dbc29825", resp.Keys[0].Kid, "the test key's ID, as in testdata/vectors.json")
	assert.Equal(t, pub, resp.Keys[0].PublicKey)
}

func TestHelperTicketKeyListsThePreviousKeyDuringARotation(t *testing.T) {
	ls, _ := newHelperTestService(t)
	ls.helperPreviousTicketKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	mux := newHelperTestRouter(t, ls)
	resp := getTicketKey(t, mux)
	require.Len(t, resp.Keys, 2)
	assert.Equal(t, "dbc29825", resp.Keys[0].Kid, "the current key comes first")
	assert.Equal(t, "5c29b78f", resp.Keys[1].Kid)
	assert.Equal(t, []byte(ls.helperPreviousTicketKey.Public().(ed25519.PublicKey)), resp.Keys[1].PublicKey)
	assert.Equal(t, []byte(ls.helperTicketKey.Public().(ed25519.PublicKey)), resp.PublicKey, "the top-level key is the current one")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `$LC_TEST 'TestLoadHelperTicketKey|TestHelperTicketKey'`
Expected: the build fails with `undefined: loadHelperTicketKeys`, `undefined: helperTicketKeys` and `ls.helperPreviousTicketKey undefined`.

- [ ] **Step 4: Implement**

In `cmd/api/main.go`, replace the `helperTicketKey` and `helperPeerSIDKey` fields of `lanternService` with:

```go
	// helperTicketKey signs helper pairing tickets; nil disables ticket issuance.
	helperTicketKey ed25519.PrivateKey
	// helperPreviousTicketKey is the key helperTicketKey replaced, kept while
	// a rotation is in flight: its public half is still served, so tickets it
	// signed still verify, and pairing short IDs derived from it are still
	// accepted. nil outside a rotation.
	helperPreviousTicketKey ed25519.PrivateKey
	// helperKeyGeneration counts ticket key rotations. Pairing-set revisions
	// carry it, so a volunteer applies a new key's short IDs at once.
	helperKeyGeneration uint16
	// helperPeerSIDKey is K_sid, derived from helperTicketKey's seed; nil
	// turns direct-mode pairing short IDs off.
	helperPeerSIDKey []byte
	// helperPreviousPeerSIDKey is K_sid derived from helperPreviousTicketKey;
	// nil outside a rotation.
	helperPreviousPeerSIDKey []byte
```

Replace the two wiring lines after `// Helper pairing tickets:` with:

```go
	// Helper pairing tickets: signing keys from Vault; no seed disables issuance.
	helperKeys := loadHelperTicketKeys(ctx, vault.ReadKV)
	ls.helperTicketKey = helperKeys.current
	ls.helperPreviousTicketKey = helperKeys.previous
	ls.helperKeyGeneration = helperKeys.generation
	ls.helperPeerSIDKey = helperPeerSIDKeyFor(helperKeys.current)
	ls.helperPreviousPeerSIDKey = helperPeerSIDKeyFor(helperKeys.previous)
```

In `cmd/api/helper_handler.go`, add `"math"`, `"strconv"` and `"strings"` to the imports. Replace `TicketKey` with:

```go
// TicketKey needs no device ID or flag: the public keys are not secret, and
// freddie fetches them before any client is involved. It lists the current
// key, then, during a rotation, the previous one, each under its key ID.
func (s helperService) TicketKey(context.Context, *emptypb.Empty) (*services.HelperTicketKey, error) {
	if s.helperTicketKey == nil {
		return nil, &router.HTTPError{Status: http.StatusServiceUnavailable, Msg: "tickets unavailable"}
	}
	pub := s.helperTicketKey.Public().(ed25519.PublicKey)
	out := &services.HelperTicketKey{
		PublicKey: pub,
		Keys:      []*services.HelperTicketKeyEntry{{Kid: pairing.TicketKeyID(pub), PublicKey: pub}},
	}
	if s.helperPreviousTicketKey != nil {
		prev := s.helperPreviousTicketKey.Public().(ed25519.PublicKey)
		out.Keys = append(out.Keys, &services.HelperTicketKeyEntry{Kid: pairing.TicketKeyID(prev), PublicKey: prev})
	}
	return out, nil
}
```

Replace `loadHelperTicketKey` (from its doc comment to its closing brace) with:

```go
// helperTicketKeys is the helper ticket signing key and what a rotation needs
// beside it. The zero value disables tickets and direct mode.
type helperTicketKeys struct {
	// current signs tickets and derives K_sid.
	current ed25519.PrivateKey
	// previous is the key current replaced, while a rotation is in flight.
	previous ed25519.PrivateKey
	// generation counts rotations.
	generation uint16
}

// loadHelperTicketKeys reads the ticket signing keys from Vault with readKV
// (vault.ReadKV outside tests): seed, and during a rotation previous_seed and
// generation. When seed is absent or malformed, or generation is malformed,
// ticket issuance is disabled at once rather than fatal: a wrong generation
// would stop volunteers applying the new key's pairing short IDs. A malformed
// previous_seed is ignored. Any other read error is retried with backoff
// within helperTicketKeyReadBudget, then also disables.
func loadHelperTicketKeys(ctx context.Context, readKV func(ctx context.Context, path string, v any) error) helperTicketKeys {
	ctx, cancel := context.WithTimeout(ctx, helperTicketKeyReadBudget)
	defer cancel()
	var secret struct {
		Seed         string `json:"seed"`
		PreviousSeed string `json:"previous_seed"`
		// Generation is a string when written with vault kv put, and a
		// number when written as JSON.
		Generation any `json:"generation"`
	}
	for attempt := 0; ; attempt++ {
		err := readKV(ctx, "secret/data/lantern_cloud/helper_tickets", &secret)
		if err == nil {
			break
		}
		if errors.Is(err, vault.ErrNoSecret) {
			log.WarnErr(ctx, "helper ticket key not configured, helper tickets disabled", err, nil)
			return helperTicketKeys{}
		}
		if attempt >= len(helperTicketKeyRetryDelays) {
			log.Error(ctx, "helper ticket key unreadable, helper tickets disabled", err, nil)
			return helperTicketKeys{}
		}
		log.WarnErr(ctx, "helper ticket key read failed, retrying", err, nil)
		select {
		case <-ctx.Done():
			log.Error(ctx, "helper ticket key unreadable, helper tickets disabled", err, nil)
			return helperTicketKeys{}
		case <-time.After(helperTicketKeyRetryDelays[attempt]):
		}
	}
	current, err := parseHelperTicketSeed(secret.Seed)
	if err != nil {
		log.Error(ctx, "helper ticket key is malformed, helper tickets disabled", err, nil)
		return helperTicketKeys{}
	}
	generation, err := parseHelperKeyGeneration(secret.Generation)
	if err != nil {
		log.Error(ctx, "helper ticket key generation is malformed, helper tickets disabled", err, nil)
		return helperTicketKeys{}
	}
	keys := helperTicketKeys{current: current, generation: generation}
	if strings.TrimSpace(secret.PreviousSeed) != "" {
		previous, err := parseHelperTicketSeed(secret.PreviousSeed)
		switch {
		case err != nil:
			log.Error(ctx, "previous helper ticket key is malformed, ignoring it", err, nil)
		case !previous.Equal(current):
			keys.previous = previous
		}
	}
	return keys
}

// parseHelperTicketSeed decodes a base64 32-byte ed25519 seed.
func parseHelperTicketSeed(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("decode helper ticket seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("helper ticket seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// parseHelperKeyGeneration reads the generation field: absent or empty is 0.
func parseHelperKeyGeneration(v any) (uint16, error) {
	switch g := v.(type) {
	case nil:
		return 0, nil
	case string:
		g = strings.TrimSpace(g)
		if g == "" {
			return 0, nil
		}
		n, err := strconv.ParseUint(g, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("generation %q: want an integer from 0 to 65535", g)
		}
		return uint16(n), nil
	case float64:
		if g < 0 || g > math.MaxUint16 || g != math.Trunc(g) {
			return 0, fmt.Errorf("generation %v: want an integer from 0 to 65535", g)
		}
		return uint16(g), nil
	default:
		return 0, fmt.Errorf("generation is a %T, want a string or a number", v)
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `$LC_TEST 'TestLoadHelperTicketKey|TestHelperTicketKey|TestHelperIssueTickets|TestHelperNoTicketsAfterEnd' && go vet ./cmd/api/`
Expected: PASS for all 14 loader subtests, both key-list tests, and plan 1's ticket issuance tests; `go vet` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/api/apipb/proto/services/helper.proto cmd/api/apipb/services/helper*.go cmd/backoffice/src/gen/services/helper* openapiv2/services/helper.swagger.json \
  cmd/api/helper_handler.go cmd/api/main.go cmd/api/helper_ticket_key_test.go cmd/api/helper_handler_db_test.go
git commit -m "api: list helper ticket keys by ID and load a previous key for rotation"
```

---

### Task 8: lantern-cloud — key generation in the pairing-set revision, previous-key short IDs

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-rooms`

**Files:**
- Modify: `cmd/api/helper_direct.go` (`pairingSetRevision`, `volunteerPairingShortIDs`)
- Modify: `cmd/api/helper_direct_db_test.go`

**Interfaces:**
- Consumes: `ls.helperKeyGeneration` and `ls.helperPreviousPeerSIDKey` (Task 7); plan 2's `pairing.AcceptedPeerShortIDs`, `pairing.PeerSIDEpoch`, `ListVolunteerPairingsWithVersion`, and the test helpers `acceptedHex`, `pinHelperNow`, `registerAs`, `heartbeat` and `revision`.
- Produces:
  - `func pairingSetRevision(version int64, keygen uint16, now time.Time) int64`, encoded as `version<<36 | keygen<<20 | epoch&0xFFFFF`. This is plan 2's deferred key-generation item.
  - a volunteer's accepted set gains the previous key's IDs during a rotation

- [ ] **Step 1: Update the existing call sites and write the failing tests**

```bash
perl -pi -e 's/pairingSetRevision\((\d+), /pairingSetRevision($1, 0, /g' cmd/api/helper_direct_db_test.go
grep -n 'pairingSetRevision(' cmd/api/helper_direct_db_test.go
```

Expected: every call now passes three arguments (`(1, 0, now)`, `(2, 0, now)`, `(1, 0, t0.Add(time.Second))`, …).

Then replace `TestHelperPairingSetRevisionEncoding` with:

```go
// The "next epoch" case above relies on the version outranking any key
// generation and epoch, and a rotation relies on the generation outranking
// any epoch.
func TestHelperPairingSetRevisionEncoding(t *testing.T) {
	now := time.Unix(1_800_000_450, 0)
	e := int64(pairing.PeerSIDEpoch(now))
	assert.Equal(t, e, pairingSetRevision(0, 0, now))
	assert.Equal(t, int64(7)<<36|int64(2)<<20|e, pairingSetRevision(7, 2, now))

	farFuture := time.Unix(1<<40, 0)
	assert.Less(t, pairingSetRevision(1, 65535, farFuture), pairingSetRevision(2, 0, now),
		"a version bump outranks any key generation and epoch")
	assert.Less(t, pairingSetRevision(3, 0, farFuture), pairingSetRevision(3, 1, now),
		"a key rotation outranks any epoch at an unchanged version")
	assert.Less(t, pairingSetRevision(3, 1, now), pairingSetRevision(3, 1, now.Add(24*time.Hour)),
		"the daily rollover is newer at an unchanged version and key")
	assert.Greater(t, pairingSetRevision(1<<27-1, 65535, farFuture), int64(0), "the largest version stays positive")
	assert.Less(t, pairing.PeerSIDEpoch(time.Date(4800, 1, 1, 0, 0, 0, 0, time.UTC)), uint64(1<<20),
		"epochs fit 20 bits for millennia")

	// A volunteer may hold a revision from plan 2's version<<32|epoch encoding.
	// The new encoding never ranks below it at the same or a later version.
	for _, v := range []int64{0, 1, 2, 1000, 1 << 26} {
		assert.LessOrEqual(t, v<<32|e, pairingSetRevision(v, 0, now), "version %d", v)
	}
}

// During a rotation a volunteer accepts both keys' short IDs, current first,
// so inviters still holding a config from before it keep working. Ending the
// rotation bumps the generation, so the smaller set is newer and applied.
func TestPeerVolunteerShortIDsDuringKeyRotation(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	ls.helperPeerSIDKey = helperPeerSIDKeyFor(ed25519.NewKeyFromSeed(testTicketSeed()))
	ls.helperPreviousPeerSIDKey = helperPeerSIDKeyFor(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize)))
	ls.helperKeyGeneration = 1
	now := time.Unix(1_800_000_450, 0)
	pinHelperNow(t, now)
	ctx := context.Background()
	q := dal.New(tdb.GetPool())
	id := uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: id, InviterDeviceID: "friend", VolunteerDeviceID: "device-A"}))

	mux := newPeerTestRouter(t, ls)
	reg := registerAs(t, mux, "device-A")
	want := append(acceptedHex(ls.helperPeerSIDKey, id, now), acceptedHex(ls.helperPreviousPeerSIDKey, id, now)...)
	assert.Equal(t, want, reg.PairingShortIDs, "the current key's IDs, then the previous key's")
	assert.Equal(t, pairingSetRevision(1, 1, now), revision(t, reg.PairingShortIDsRevision))

	ls.helperPreviousPeerSIDKey = nil
	ls.helperKeyGeneration = 2
	hb := heartbeat(t, mux, "device-A", reg.RouteID)
	assert.Equal(t, acceptedHex(ls.helperPeerSIDKey, id, now), hb.PairingShortIDs)
	assert.Greater(t, revision(t, hb.PairingShortIDsRevision), revision(t, reg.PairingShortIDsRevision),
		"the end of the rotation is a newer set, so the volunteer drops the old key's IDs")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `$LC_TEST 'TestHelperPairingSetRevisionEncoding|TestPeerVolunteer'`
Expected: the build fails with `too many arguments in call to pairingSetRevision`.

- [ ] **Step 3: Implement**

In `cmd/api/helper_direct.go`, replace `pairingSetRevision` and its doc comment with:

```go
// The pairing_short_ids_revision layout. Bit 63 stays 0, so revisions are
// positive and 0 still means "unknown".
const (
	revisionEpochBits    = 20 // days since 1970: until about the year 4840
	revisionKeygenBits   = 16 // ticket key rotations
	revisionKeygenShift  = revisionEpochBits
	revisionVersionShift = revisionEpochBits + revisionKeygenBits // 27 bits of version
	revisionEpochMask    = 1<<revisionEpochBits - 1
)

// pairingSetRevision is the pairing_short_ids_revision of a known set read at
// version, the volunteer's helper_volunteer_versions version in the same
// snapshot as the set, with short IDs derived from ticket key generation
// keygen at now:
//
//	version<<36 | keygen<<20 | PeerSIDEpoch(now) & 0xFFFFF
//
// A peer applies a set only if its revision beats the last one it applied, and
// responses can arrive out of order. Every insert or delete of one of the
// volunteer's pairings bumps version in the same transaction, so a set read
// after a change to the pairings always has a higher revision than one read
// before it, whatever order the lookups started or finished in. keygen, below
// it, makes a ticket key rotation a newer revision at an unchanged version, so
// the new key's IDs are applied at once. The epoch, below that, makes the
// daily rollover newer at an unchanged version and key; it is never 0 in
// practice, so a never-paired volunteer's known empty set is applied too.
//
// The epoch orders only sets read at one version and key. now is taken before
// the read, so near the epoch boundary a lookup stamped with epoch e can read
// a later snapshot than one stamped e+1; the e+1 set still wins. That cannot
// reorder pairing state, because both read the same pairings: any change to
// them bumps the version, which outranks the rest. version would need 2^27
// changes to one volunteer's pairings to overflow.
//
// Plan 2 encoded version<<32 | epoch. For version v ≥ 1, v<<36 exceeds
// (v+1)<<32, and at version 0 both are keygen<<20 | epoch ≥ epoch, so no set
// in this encoding ranks below one a volunteer kept from the old one.
func pairingSetRevision(version int64, keygen uint16, now time.Time) int64 {
	return version<<revisionVersionShift |
		int64(keygen)<<revisionKeygenShift |
		int64(pairing.PeerSIDEpoch(now)&revisionEpochMask)
}
```

In `volunteerPairingShortIDs`, replace the inner loop and the return:

```go
	var out []string
	for _, r := range rows {
		if !r.PairingID.Valid {
			continue
		}
		// The previous key's IDs follow during a rotation, so an inviter whose
		// config predates it still connects until her next fetch.
		for _, key := range [][]byte{ls.helperPeerSIDKey, ls.helperPreviousPeerSIDKey} {
			if len(key) == 0 {
				continue
			}
			for _, sid := range pairing.AcceptedPeerShortIDs(key, r.PairingID.UUID, now) {
				out = append(out, hex.EncodeToString(sid[:]))
			}
		}
	}
	return out, pairingSetRevision(rows[0].Version, ls.helperKeyGeneration, now), true, nil
```

Update the function's doc comment: change "the IDs of epochs e-1, e and e+1" to "the IDs of epochs e-1, e and e+1 under the current ticket key and, during a rotation, then under the previous one".

- [ ] **Step 4: Run the tests**

Run: `$LC_TEST 'TestHelper|TestPeer'`
Expected: PASS for every helper and peer test, including plan 2's `TestPeerVolunteerGetsPairingShortIDs`, the revision-ordering tests, and the two new tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/api/helper_direct.go cmd/api/helper_direct_db_test.go
git commit -m "api: carry the ticket key generation in pairing-set revisions"
```

---

### Task 9: lantern-cloud — freddie fetches the keys and verifies room tickets

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-rooms`

**Files:**
- Modify: `go.mod`, `go.sum` (broflake bump to `UB_SHA`)
- Create: `cmd/freddie/tickets.go`
- Create: `cmd/freddie/tickets_test.go`
- Modify: `cmd/freddie/main.go`

**Interfaces:**
- Consumes: `freddie.Freddie.RoomTicket` and `helperticket` (Tasks 1 and 5, merged as `UB_SHA`); the key list (Task 7); `cmd/api/pairing/testdata/vectors.json` (Task 6).
- Produces:
  - `type ticketKeys`, with `newTicketKeys(url, client)`, `refresh(ctx) error`, `run(ctx)` and `Room(ticket, role) (string, bool)`
  - `func parseTicketKeys(ticketKeyResponse) ([]helperticket.Key, error)`
  - `func ticketKeyURL(flagValue, publicDomain string) string`
  - the counter `freddie.ticket_key.refresh{ok}`

**Why fetch rather than read Vault:** freddie already reads Vault, but `secret/data/lantern_cloud/helper_tickets` holds the private seed. Granting freddie that path would let a compromised freddie mint tickets for any pairing. The public key list holds nothing secret, so freddie's only new trust is in the TLS connection to the API. An `ErrUnknownKey` kick covers the gap between the API signing with a new key and freddie's next scheduled fetch.

- [ ] **Step 1: Bump broflake**

```bash
go get github.com/getlantern/broflake@$UB_SHA
go mod tidy
git diff --stat
grep -n 'quic-go-unbounded-fork' go.mod
git -C ~/go/src/github.com/getlantern/unbounded show $UB_SHA:go.mod | grep -n 'quic-go-unbounded-fork'
```

Expected: the diff touches only `go.mod` and `go.sum`. `github.com/getlantern/broflake` moves from `v0.0.0-20261019200128-0ef956d8c621` to the pseudo-version of `UB_SHA`. Both `quic-go-unbounded-fork` lines name the same version (`v0.62.1-unbounded`); if broflake's moved, mirror it in the `replace`, as the comment at `go.mod:575` requires.

```bash
go build ./... && git add go.mod go.sum && git commit -m "Bump broflake for helper pairing rooms"
```

- [ ] **Step 2: Write the failing tests**

`cmd/freddie/tickets_test.go`:

```go
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getlantern/broflake/common/helperticket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ticketVectors is the "ticket" section of the API's golden vectors: tickets
// lantern-cloud signs, checked here with the verifier freddie runs.
func ticketVectors(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "api", "pairing", "testdata", "vectors.json"))
	require.NoError(t, err)
	var v struct {
		Ticket map[string]string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal(b, &v))
	return v.Ticket
}

func vectorTime(t *testing.T, s string) time.Time {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	require.NoError(t, err)
	return time.Unix(n, 0)
}

func vectorKey(t *testing.T, hexKey string) []byte {
	t.Helper()
	b, err := hex.DecodeString(hexKey)
	require.NoError(t, err)
	return b
}

// keyList is the API's JSON for keys, current first.
func keyList(t *testing.T, keys ...[]byte) string {
	t.Helper()
	type entry struct {
		Kid       string `json:"kid"`
		PublicKey []byte `json:"public_key"`
	}
	body := struct {
		PublicKey []byte  `json:"public_key"`
		Keys      []entry `json:"keys"`
	}{PublicKey: keys[0]}
	for _, k := range keys {
		body.Keys = append(body.Keys, entry{Kid: helperticket.KeyID(k), PublicKey: k})
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return string(b)
}

// keyServer serves *body with status *status and counts requests.
func keyServer(t *testing.T, body string) (srv *httptest.Server, setBody func(string), status *atomic.Int32, hits *atomic.Int32) {
	t.Helper()
	var b atomic.Pointer[string]
	b.Store(&body)
	status, hits = &atomic.Int32{}, &atomic.Int32{}
	status.Store(http.StatusOK)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, "/v1/helper/ticket-key", r.URL.Path)
		w.WriteHeader(int(status.Load()))
		w.Write([]byte(*b.Load()))
	}))
	t.Cleanup(srv.Close)
	return srv, func(s string) { b.Store(&s) }, status, hits
}

func TestTicketKeysVerifyLanternCloudTickets(t *testing.T) {
	v := ticketVectors(t)
	srv, _, _, _ := keyServer(t, keyList(t, vectorKey(t, v["public_key_hex"]), vectorKey(t, v["previous_public_key_hex"])))
	k := newTicketKeys(srv.URL+"/v1/helper/ticket-key", srv.Client())
	at := vectorTime(t, v["verify_at_unix"])
	k.now = func() time.Time { return at }
	require.NoError(t, k.refresh(context.Background()))

	room, ok := k.Room(v["token"], helperticket.RoleVolunteer)
	assert.True(t, ok)
	assert.Equal(t, v["pairing_id"], room)
	room, ok = k.Room(v["inviter_token"], helperticket.RoleInviter)
	assert.True(t, ok)
	assert.Equal(t, v["pairing_id"], room)
	_, ok = k.Room(v["token"], helperticket.RoleInviter)
	assert.False(t, ok, "a volunteer ticket must not admit an inviter")
	_, ok = k.Room(v["previous_token"], helperticket.RoleVolunteer)
	assert.True(t, ok, "mid-rotation the previous key still verifies")
	_, ok = k.Room("f"+v["token"][1:], helperticket.RoleVolunteer)
	assert.False(t, ok, "a tampered ticket")

	at = vectorTime(t, v["expired_at_unix"])
	_, ok = k.Room(v["token"], helperticket.RoleVolunteer)
	assert.False(t, ok, "an expired ticket")
}

func TestParseTicketKeys(t *testing.T) {
	v := ticketVectors(t)
	cur := vectorKey(t, v["public_key_hex"])

	var legacy ticketKeyResponse
	require.NoError(t, json.Unmarshal([]byte(`{"public_key":"/RckOFqgx1tk+3jNYC+h2ZH96/drE8WO1wLqyDXp9hg="}`), &legacy))
	keys, err := parseTicketKeys(legacy)
	require.NoError(t, err, "an API that predates the list sends only public_key")
	require.Len(t, keys, 1)
	assert.Equal(t, v["kid"], keys[0].ID)

	var mislabeled ticketKeyResponse
	require.NoError(t, json.Unmarshal([]byte(keyList(t, cur)), &mislabeled))
	mislabeled.Keys[0].Kid = "00000000"
	_, err = parseTicketKeys(mislabeled)
	assert.Error(t, err, "a key listed under another key's ID")

	_, err = parseTicketKeys(ticketKeyResponse{Keys: []ticketKeyEntry{{Kid: "x", PublicKey: []byte("short")}}})
	assert.Error(t, err, "a malformed key")
	_, err = parseTicketKeys(ticketKeyResponse{})
	assert.Error(t, err, "an empty list")
}

func TestTicketKeysKeepTheLastGoodSet(t *testing.T) {
	v := ticketVectors(t)
	srv, setBody, status, _ := keyServer(t, keyList(t, vectorKey(t, v["public_key_hex"])))
	k := newTicketKeys(srv.URL+"/v1/helper/ticket-key", srv.Client())
	k.now = func() time.Time { return vectorTime(t, v["verify_at_unix"]) }
	require.NoError(t, k.refresh(context.Background()))

	status.Store(http.StatusInternalServerError)
	assert.Error(t, k.refresh(context.Background()))
	status.Store(http.StatusOK)
	setBody(`{"keys":[]}`)
	assert.Error(t, k.refresh(context.Background()))

	_, ok := k.Room(v["token"], helperticket.RoleVolunteer)
	assert.True(t, ok, "failed refreshes keep the last good keys")
}

// A ticket naming a key freddie lacks means a rotation began since the last
// fetch: freddie refreshes early, and the ticket verifies at the client's next
// request.
func TestTicketKeysUnknownKeyRefreshesEarly(t *testing.T) {
	v := ticketVectors(t)
	prevInterval, prevKick := ticketKeyRefreshInterval, ticketKeyKickInterval
	ticketKeyRefreshInterval, ticketKeyKickInterval = time.Hour, 0
	t.Cleanup(func() { ticketKeyRefreshInterval, ticketKeyKickInterval = prevInterval, prevKick })

	srv, setBody, _, hits := keyServer(t, keyList(t, vectorKey(t, v["previous_public_key_hex"])))
	k := newTicketKeys(srv.URL+"/v1/helper/ticket-key", srv.Client())
	k.now = func() time.Time { return vectorTime(t, v["verify_at_unix"]) }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go k.run(ctx)
	require.Eventually(t, func() bool { return hits.Load() == 1 }, 5*time.Second, 10*time.Millisecond)

	setBody(keyList(t, vectorKey(t, v["public_key_hex"]), vectorKey(t, v["previous_public_key_hex"])))
	_, ok := k.Room(v["token"], helperticket.RoleVolunteer)
	assert.False(t, ok, "the new key is not known yet")
	require.Eventually(t, func() bool { return hits.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		_, ok := k.Room(v["token"], helperticket.RoleVolunteer)
		return ok
	}, 5*time.Second, 10*time.Millisecond)
}

func TestTicketKeyURL(t *testing.T) {
	assert.Equal(t, "https://x.test/k", ticketKeyURL("https://x.test/k", "iantem.io"))
	assert.Equal(t, "https://api.iantem.io/v1/helper/ticket-key", ticketKeyURL("", "iantem.io"))
	assert.Equal(t, "https://api.staging.iantem.io/v1/helper/ticket-key", ticketKeyURL("", "staging.iantem.io"))
	assert.Equal(t, "", ticketKeyURL("", ""), "no URL turns rooms off")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/freddie/ -run 'TicketKey|ParseTicketKeys'`
Expected: the build fails with `undefined: newTicketKeys`, `undefined: ticketKeyResponse`, `undefined: parseTicketKeys` and `undefined: ticketKeyURL`.

- [ ] **Step 4: Implement**

`cmd/freddie/tickets.go`:

```go
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/getlantern/broflake/common/helperticket"

	"github.com/getlantern/lantern-cloud/log"
	"github.com/getlantern/lantern-cloud/metrics"
)

const (
	ticketKeyFetchTimeout  = 10 * time.Second
	ticketKeyMaxBody       = 64 << 10
	metricTicketKeyRefresh = "freddie.ticket_key.refresh"
)

// Variables so tests can shorten them.
var (
	// ticketKeyRefreshInterval is how often the key list is re-fetched.
	ticketKeyRefreshInterval = 5 * time.Minute
	// ticketKeyRetryInterval is the wait after a failed fetch.
	ticketKeyRetryInterval = 30 * time.Second
	// ticketKeyKickInterval bounds early refreshes caused by tickets naming
	// an unknown key, so made-up key IDs cannot make freddie hammer the API.
	ticketKeyKickInterval = 30 * time.Second
)

// ticketKeys holds the public keys helper pairing tickets are verified with,
// fetched from lantern-cloud's public GET /v1/helper/ticket-key. freddie
// holds no secret for this: the Vault secret the API signs with is the
// private seed, and a freddie that could read it could mint tickets.
type ticketKeys struct {
	url    string
	client *http.Client
	now    func() time.Time
	keys   atomic.Pointer[[]helperticket.Key]
	kick   chan struct{}
}

func newTicketKeys(url string, client *http.Client) *ticketKeys {
	return &ticketKeys{url: url, client: client, now: time.Now, kick: make(chan struct{}, 1)}
}

type ticketKeyEntry struct {
	Kid       string `json:"kid"`
	PublicKey []byte `json:"public_key"`
}

// ticketKeyResponse is the API's HelperTicketKey as JSON.
type ticketKeyResponse struct {
	PublicKey []byte           `json:"public_key"`
	Keys      []ticketKeyEntry `json:"keys"`
}

// parseTicketKeys validates a key list. Each key's ID must be its own
// helperticket.KeyID: a mismatch is a malformed list, not a key to trust under
// another name. An API that predates the list sends only public_key, which is
// then used on its own.
func parseTicketKeys(body ticketKeyResponse) ([]helperticket.Key, error) {
	var keys []helperticket.Key
	for _, e := range body.Keys {
		if len(e.PublicKey) != ed25519.PublicKeySize {
			return nil, errors.New("ticket key list: malformed public key")
		}
		key := helperticket.NewKey(ed25519.PublicKey(e.PublicKey))
		if key.ID != e.Kid {
			return nil, fmt.Errorf("ticket key list: kid %q is not its key's ID %q", e.Kid, key.ID)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 && len(body.PublicKey) == ed25519.PublicKeySize {
		keys = append(keys, helperticket.NewKey(ed25519.PublicKey(body.PublicKey)))
	}
	if len(keys) == 0 {
		return nil, errors.New("ticket key list is empty")
	}
	return keys, nil
}

// refresh fetches the key list once and installs it. A failed fetch or an
// unusable list leaves the last good keys in place.
func (k *ticketKeys) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ticketKeyFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return fmt.Errorf("build ticket key request: %w", err)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch ticket keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch ticket keys: status %d", resp.StatusCode)
	}
	var body ticketKeyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, ticketKeyMaxBody)).Decode(&body); err != nil {
		return fmt.Errorf("decode ticket keys: %w", err)
	}
	keys, err := parseTicketKeys(body)
	if err != nil {
		return err
	}
	k.keys.Store(&keys)
	return nil
}

// run refreshes the keys at once, then every ticketKeyRefreshInterval, sooner
// after a failure, and early when a ticket names an unknown key, until ctx is
// done. Requests never wait on it: until the first load succeeds no ticket
// verifies, so rooms are empty and freddie serves the pool as before.
func (k *ticketKeys) run(ctx context.Context) {
	for {
		last := k.now()
		wait := ticketKeyRefreshInterval
		if err := k.refresh(ctx); err != nil {
			log.Error(ctx, "failed to load helper ticket keys, keeping the last good set", err, nil)
			metrics.Inc(ctx, metricTicketKeyRefresh, "ok", "false")
			wait = ticketKeyRetryInterval
		} else {
			metrics.Inc(ctx, metricTicketKeyRefresh, "ok", "true")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-k.kick:
			if d := ticketKeyKickInterval - k.now().Sub(last); d > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(d):
				}
			}
		}
	}
}

// Room implements freddie.Freddie.RoomTicket. It logs, traces and counts
// nothing: tickets and the pairing IDs in them must never leave freddie's
// memory.
func (k *ticketKeys) Room(ticket string, role helperticket.Role) (string, bool) {
	var keys []helperticket.Key
	if p := k.keys.Load(); p != nil {
		keys = *p
	}
	t, err := helperticket.Verify(keys, ticket, k.now())
	if errors.Is(err, helperticket.ErrUnknownKey) {
		select {
		case k.kick <- struct{}{}:
		default:
		}
	}
	if err != nil || t.Role != role {
		return "", false
	}
	return t.PairingID, true
}

// ticketKeyURL is where freddie fetches the ticket keys: flagValue if set,
// else lantern-cloud's public API in this environment, else "" (rooms off).
func ticketKeyURL(flagValue, publicDomain string) string {
	if flagValue != "" {
		return flagValue
	}
	if publicDomain == "" {
		return ""
	}
	return "https://api." + publicDomain + "/v1/helper/ticket-key"
}
```

In `cmd/freddie/main.go`, add `"github.com/getlantern/lantern-cloud/log"` to the imports. Add the flag after `help`:

```go
	ticketKeyURLFlag := flag.String("helper-ticket-key-url", "",
		"where to fetch helper ticket keys; default https://api.$LC_PUBLIC_DOMAIN/v1/helper/ticket-key")
```

Insert after `f.ConsumerCountry = ...`:

```go
	// Helper pairing rooms: tickets verify against lantern-cloud's public
	// ticket keys, fetched in the background.
	if u := ticketKeyURL(*ticketKeyURLFlag, os.Getenv("LC_PUBLIC_DOMAIN")); u != "" {
		keys := newTicketKeys(u, &http.Client{Timeout: ticketKeyFetchTimeout})
		go keys.run(ctx)
		f.RoomTicket = keys.Room
	} else {
		log.Warn(ctx, "no helper ticket key URL, helper pairing rooms disabled", nil)
	}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./cmd/freddie/ -v && go vet ./cmd/freddie/ && $LC_TEST 'TestHelper|TestPeer'`
Expected: PASS for `TestTicketKeysVerifyLanternCloudTickets`, `TestParseTicketKeys`, `TestTicketKeysKeepTheLastGoodSet`, `TestTicketKeysUnknownKeyRefreshesEarly`, `TestTicketKeyURL` and the existing country tests; `go vet` prints nothing; the API tests still pass on the bumped broflake.

- [ ] **Step 6: Full suite and telemetry audit**

```bash
make test
git diff origin/fisk/helper-direct-mode -- cmd/ | grep -nE '^\+.*(log\.(Error|Warn|Info|Debug)|metrics\.|SetAttributes|attribute\.)'
```

Expected: `make test` passes. The audit lists only fixed-message log calls with `err` (fetch, parse and Vault errors, none built from a ticket), and `metrics.Inc(ctx, metricTicketKeyRefresh, "ok", …)`.

- [ ] **Step 7: Commit and open the PR**

```bash
git add cmd/freddie/tickets.go cmd/freddie/tickets_test.go cmd/freddie/main.go
git commit -m "freddie: verify helper pairing room tickets against the API's key list"
```

Open the PR with `skills:create-pr`, with base `fisk/helper-direct-mode`, or `main` if #3675 has merged. Its description includes the rotation runbook in Step 9 and the header's sequence diagram.

- [ ] **Step 8: [Human] Merge and deploy.**
  - Merge after #3660 and #3675.
  - A merge to `main` that touches `go.mod` deploys both the API (`deploy-api.yml`) and freddie (`deploy-freddie.yml`) to production. freddie is a single instance holding its signaling tables in memory, so its redeploy drops in-flight handshakes for a few seconds. Merge at a quiet hour.
  - Vault needs no change: with no `previous_seed` or `generation`, the generation is 0. That gives every volunteer a revision of at least what plan 2 gave it (the upgrade argument in the bit budget).
  - After the deploy, check both endpoints. `curl -s https://api.iantem.io/v1/helper/ticket-key | jq` should return one `keys` entry with `kid` equal to the first 8 hex characters of `sha256` of the decoded `public_key`. In SigNoz, `freddie.ticket_key.refresh{ok="true"}` should rise every 5 minutes.
  - Rooms stay unused until the radiance release (Task 14) ships.

- [ ] **Step 9: [Human] Ticket-key rotation runbook** (record it in the PR description; there is nothing to run now)
  1. Generate a seed: `openssl rand -base64 32`.
  2. Write the new seed, keeping the old one as previous: `vault kv patch secret/lantern_cloud/helper_tickets seed=<new> previous_seed=<current seed> generation=<current generation + 1>`.
  3. Redeploy the API, which reads the secret at startup. freddie picks up the new key at its next fetch, or at once, rate-limited to every 30 s, when a ticket names it. Volunteers apply the new key's short IDs at their next heartbeat, because the generation outranks their old revision. Inviters keep working on the previous key's IDs until their next config fetch.
  4. After at least 30 minutes (one ticket lifetime, plus inviter config polls of up to 15 minutes), end the rotation: `vault kv patch secret/lantern_cloud/helper_tickets previous_seed="" generation=<generation + 1>`, then redeploy the API. Bumping the generation again makes volunteers drop the previous key's IDs promptly.
  5. After a suspected compromise, skip the overlap: write the new seed with `previous_seed=""` and `generation+1` in one patch. Paired direct-mode sessions then reconnect at the next config fetch.

---

### Task 10: lantern-box — broflake bump and the room ticket source

**Repo / worktree:** lantern-box, `~/go/src/github.com/getlantern/lantern-box-rooms`

**Files:**
- Modify: `go.mod`, `go.sum` (broflake `e0237743ac3d` → `UB_SHA`)
- Create: `adapter/room_tickets.go`
- Create: `adapter/room_tickets_test.go`
- Modify: `protocol/unbounded/outbound.go` (`NewOutbound`)
- Modify: `option/unbounded.go` (the doc comment on runtime dependencies)

**Interfaces:**
- Consumes: `WebRTCOptions.RoomTicketSource` (Task 2), and the `ContextWithDirectTransport` pattern (`adapter/direct_transport.go`).
- Produces:
  - `func ContextWithRoomTicketSource(ctx context.Context, src func() []string) context.Context`
  - `func RoomTicketSourceFromContext(ctx context.Context) func() []string`
  - The Unbounded outbound passes the source to broflake, which reads it at every subscribe.

**Design choice:** the outbound gets a source function, not a setter driven by a refresher goroutine. Options are consumed once at `Start`, and the consumer already re-subscribes about every 20 s. A function read at each subscribe gives fresh tickets with no goroutine, no lifecycle tie to `Close`, and no new broflake API beyond the `RoomTicketSource` field.

- [ ] **Step 1: Create the worktree and bump broflake**

```bash
git -C ~/go/src/github.com/getlantern/lantern-box fetch origin --tags
git -C ~/go/src/github.com/getlantern/lantern-box worktree add ~/go/src/github.com/getlantern/lantern-box-rooms -b fisk/unbounded-rooms origin/main
cd ~/go/src/github.com/getlantern/lantern-box-rooms
go get github.com/getlantern/broflake@$UB_SHA
go mod tidy
git diff --stat
grep -n 'quic-go-unbounded-fork' go.mod
```

Expected: only `go.mod` and `go.sum` change, and the `quic-go` replace still names `v0.62.1-unbounded`, matching broflake's.

```bash
go build ./... && go test ./protocol/unbounded/ && git add go.mod go.sum && git commit -m "Bump broflake for helper pairing rooms"
```

- [ ] **Step 2: Write the failing test**

`adapter/room_tickets_test.go`:

```go
package adapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoomTicketSourceFromContext(t *testing.T) {
	assert.Nil(t, RoomTicketSourceFromContext(context.Background()), "no source by default")
	assert.Nil(t, RoomTicketSourceFromContext(ContextWithRoomTicketSource(context.Background(), nil)),
		"a nil source registers nothing")

	calls := 0
	ctx := ContextWithRoomTicketSource(context.Background(), func() []string {
		calls++
		return []string{"a.b"}
	})
	src := RoomTicketSourceFromContext(ctx)
	require.NotNil(t, src)
	assert.Equal(t, []string{"a.b"}, src())
	assert.Equal(t, []string{"a.b"}, src())
	assert.Equal(t, 2, calls, "the source is called on every read, so its tickets can change")
}
```

Run: `go test ./adapter/ -run RoomTicketSource`
Expected: the build fails with `undefined: RoomTicketSourceFromContext`.

- [ ] **Step 3: Implement**

`adapter/room_tickets.go`:

```go
package adapter

import "context"

type roomTicketSourceKey struct{}

// ContextWithRoomTicketSource returns a copy of ctx carrying src, which returns
// the helper pairing tickets an Unbounded consumer presents to freddie to join
// its pairings' rooms (the inviter-role tickets lantern-cloud issues the
// device). The unbounded outbound calls src each time it subscribes for a
// volunteer, about every 20 seconds, so src must be cheap and must not block,
// and its result may change while the outbound runs: tickets live 15 minutes
// and the caller (radiance) renews them in the background.
//
// Tickets are credentials: neither src nor anything that reads it may log them.
// With no source, the outbound joins no rooms and uses the pool as before.
func ContextWithRoomTicketSource(ctx context.Context, src func() []string) context.Context {
	if src == nil {
		return ctx
	}
	return context.WithValue(ctx, roomTicketSourceKey{}, src)
}

// RoomTicketSourceFromContext returns the source stored in ctx, or nil.
func RoomTicketSourceFromContext(ctx context.Context) func() []string {
	src, _ := ctx.Value(roomTicketSourceKey{}).(func() []string)
	return src
}
```

In `protocol/unbounded/outbound.go` `NewOutbound`, after `rtcOpt.HTTPClient = signalingClient(...)`, add:

```go
	// Helper pairing rooms: broflake reads the tickets at every subscribe, so
	// radiance can renew them without restarting the outbound.
	rtcOpt.RoomTicketSource = lbAdapter.RoomTicketSourceFromContext(ctx)
```

In `option/unbounded.go`, change the runtime-dependencies sentence of the `UnboundedOutboundOptions` doc comment to:

```go
// Only JSON-serializable fields live here. Runtime-only dependencies — the
// HTTP transport used for signaling, the current STUN-server list, a logger,
// the helper pairing tickets — are injected from the hosting process (e.g.
// radiance) via context (adapter.ContextWithDirectTransport,
// adapter.ContextWithRoomTicketSource) at service startup, not via this
// struct.
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./adapter/ ./protocol/unbounded/ && go vet ./adapter/ ./protocol/unbounded/`
Expected: PASS, and `go vet` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add adapter/room_tickets.go adapter/room_tickets_test.go protocol/unbounded/outbound.go option/unbounded.go
git commit -m "unbounded: join helper pairing rooms with tickets from the context"
```

---

### Task 11: lantern-box — end-to-end room test

**Repo / worktree:** lantern-box, `~/go/src/github.com/getlantern/lantern-box-rooms`

**Files:**
- Modify: `test/e2e/unbounded_test.go`

**Interfaces:**
- Consumes:
  - `TestUnboundedE2E` and its helpers `startWidget`, `startConsumer`, `waitForHTTP` and `freePort`
  - `freddie.Freddie.RoomTicket` and `helperticket.Sign` and `RoomFunc`
  - `WebRTCOptions.SetRoomTickets` and `SetServePool`
  - `ContextWithRoomTicketSource` (Task 10)
- Produces:
  - `TestUnboundedRoomE2E`
  - the helpers `startEgress(t, ctx) string`, `proxiedGetOK`, `requireProxies` and `requireNeverProxies`
  - `startWidget` gains a variadic `configure`, and `startConsumer` a variadic `roomTickets`, so `TestUnboundedE2E`'s calls are unchanged

- [ ] **Step 1: Write the test (it fails to build first)**

In `test/e2e/unbounded_test.go`, add `"crypto/ed25519"`, `"slices"` and `"github.com/getlantern/broflake/common/helperticket"` to the imports.

Replace step 3 of `TestUnboundedE2E` (from `egL, err := net.Listen` through `egressURL := "ws://" + egL.Addr().String()`) with:

```go
	egressURL := startEgress(t, ctx)
```

Change `startWidget`'s signature and apply the options before `NewBroflake`:

```go
func startWidget(t *testing.T, ctx context.Context, freddieURL, egressURL string,
	configure ...func(*UBClientcore.BroflakeOptions, *UBClientcore.WebRTCOptions)) {
```

```go
	egOpt := UBClientcore.NewDefaultEgressOptions()
	egOpt.Addr = egressURL

	for _, c := range configure {
		c(bfOpt, rtcOpt)
	}

	_, ui, err := UBClientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
```

Change `startConsumer`'s signature, and replace the `_ = lbAdapter.DirectTransportFromContext(boxCtx)` line and its comment:

```go
func startConsumer(t *testing.T, ctx context.Context, freddieURL, egressURL string, roomTickets ...string) string {
```

```go
	// No direct transport registered: the consumer has no VPN tunnel in this
	// test, so signaling falls back to the outbound's own dialer.
	if len(roomTickets) > 0 {
		tickets := slices.Clone(roomTickets)
		boxCtx = lbAdapter.ContextWithRoomTicketSource(boxCtx, func() []string { return tickets })
	}
```

In the same function, give each consumer its own session ID:

```go
		ConsumerSessionID: fmt.Sprintf("e2e-consumer-%d", consumerSeq.Add(1)),
```

Append to the file:

```go
// consumerSeq numbers consumers so that each has its own session ID.
var consumerSeq atomic.Int64

// TestUnboundedRoomE2E checks helper pairing rooms end to end. A volunteer
// widget that holds a pairing's volunteer ticket and serves only its rooms
// (pool=0) serves the consumer holding the same pairing's inviter ticket.
// freddie never offers it to a consumer without that ticket, even while that
// consumer is the only one waiting.
//
// freddie keeps its tables in package globals shared by every freddie in the
// process, so this test must not overlap another Unbounded test. Go runs them
// one after another, and TestUnboundedE2E's cleanup stops its pool widget.
func TestUnboundedRoomE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e (requires live WebRTC bootstrap)")
	}
	UBCommon.SetDebugLogger(stdlog.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// This test's stand-in for lantern-cloud's ticket key.
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keys := []helperticket.Key{helperticket.NewKey(priv.Public().(ed25519.PublicKey))}
	const pairingID = "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"
	ticket := func(role helperticket.Role) string {
		tk, err := helperticket.Sign(priv, helperticket.Ticket{PairingID: pairingID, Role: role, Expires: time.Now().Add(15 * time.Minute)})
		require.NoError(t, err)
		return tk
	}

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "hello")
	}))
	t.Cleanup(upstream.Close)

	freddieAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	f, err := freddie.New(ctx, freddieAddr)
	require.NoError(t, err, "freddie.New")
	f.RoomTicket = helperticket.RoomFunc(func() []helperticket.Key { return keys }, time.Now)
	go func() { _ = f.ListenAndServe() }()
	freddieURL := "http://" + freddieAddr
	waitForHTTP(t, freddieURL+"/v1/", 5*time.Second)

	egressURL := startEgress(t, ctx)

	var accepted atomic.Int64
	startWidget(t, ctx, freddieURL, egressURL, func(bfOpt *UBClientcore.BroflakeOptions, rtcOpt *UBClientcore.WebRTCOptions) {
		bfOpt.OnConnectionChangeFunc = func(state int, _ int, _ net.IP) {
			if state == 1 {
				accepted.Add(1)
			}
		}
		rtcOpt.SetRoomTickets([]string{ticket(helperticket.RoleVolunteer)})
		rtcOpt.SetServePool(false)
	})

	// Alone with a volunteer that served the pool, this consumer would be
	// matched at once. This volunteer serves only its room.
	unpaired := startConsumer(t, ctx, freddieURL, egressURL)
	requireNeverProxies(t, unpaired, upstream.URL, &upstreamHits, 15*time.Second)
	require.Zero(t, accepted.Load(), "the pool=0 volunteer accepted a consumer outside its room")

	paired := startConsumer(t, ctx, freddieURL, egressURL, ticket(helperticket.RoleInviter))
	requireProxies(t, paired, upstream.URL, 60*time.Second)
	require.GreaterOrEqual(t, accepted.Load(), int64(1), "the volunteer saw the paired consumer connect")

	// The volunteer now has free slots, and the unpaired consumer still never
	// gets one.
	requireNeverProxies(t, unpaired, upstream.URL, &upstreamHits, 5*time.Second)
}

// startEgress starts the broflake egress layer (QUIC over WebSocket) in front
// of a plain SOCKS5 server, and returns its ws:// URL.
func startEgress(t *testing.T, ctx context.Context) string {
	t.Helper()
	egL, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen for egress")
	egressWrapped, err := egress.NewListener(ctx, egL, &tls.Config{
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	})
	require.NoError(t, err, "egress.NewListener")
	t.Cleanup(func() { _ = egressWrapped.Close() })

	socksServer, err := socks5.New(&socks5.Config{})
	require.NoError(t, err, "socks5.New")
	go func() { _ = socksServer.Serve(egressWrapped) }()
	return "ws://" + egL.Addr().String()
}

// proxiedGetOK reports whether a GET of target through the HTTP proxy at
// proxyAddr returned 200 within timeout.
func proxiedGetOK(proxyAddr, target string, timeout time.Duration) bool {
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	c := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true},
		Timeout:   timeout,
	}
	resp, err := c.Get(target)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// requireProxies fails unless a request through proxyAddr reaches target
// within the deadline: matchmaking, ICE, DTLS, QUIC and SOCKS5 all take time.
func requireProxies(t *testing.T, proxyAddr, target string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if proxiedGetOK(proxyAddr, target, 10*time.Second) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("no request through %s succeeded within %s", proxyAddr, within)
}

// requireNeverProxies fails if any request through proxyAddr reaches target
// during d.
func requireNeverProxies(t *testing.T, proxyAddr, target string, hits *atomic.Int64, d time.Duration) {
	t.Helper()
	before := hits.Load()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		require.False(t, proxiedGetOK(proxyAddr, target, 3*time.Second),
			"a consumer outside the volunteer's room got through")
		time.Sleep(500 * time.Millisecond)
	}
	require.Equal(t, before, hits.Load(), "the upstream was reached through a consumer outside the room")
}
```

Run: `go vet ./test/e2e/`
Expected: no output. `TestUnboundedE2E` still compiles unchanged apart from `startEgress`.

- [ ] **Step 2: Check the test catches a broken room**

Temporarily change `rtcOpt.SetServePool(false)` to `rtcOpt.SetServePool(true)` in `TestUnboundedRoomE2E`:

Run: `go test -race -timeout 10m -run 'TestUnboundedRoomE2E' -v ./test/e2e/`
Expected: FAIL at the first `requireNeverProxies` with "a consumer outside the volunteer's room got through", because a pool volunteer serves the unpaired consumer. Revert the change.

- [ ] **Step 3: Run the tests**

Run: `go test -race -timeout 10m -run 'TestUnbounded' -v ./test/e2e/ && make test`
Expected: PASS for `TestUnboundedE2E` and `TestUnboundedRoomE2E`. The room test takes roughly 25–80 s: the 15 s negative window, then matchmaking. `make test` passes.

- [ ] **Step 4: Commit and open the PR**

```bash
git add test/e2e/unbounded_test.go
git commit -m "e2e: a pool=0 volunteer serves its paired consumer and nobody else"
```

Open the PR with `skills:create-pr`.

- [ ] **Step 5: Review gate.** The PR must show both Unbounded e2e tests passing in CI. If CI skips e2e (`-short`), paste the Step 3 output into the PR.

- [ ] **Step 6: [Human] Merge and record the release tag.** `.github/workflows/auto-tag.yaml` tags a patch release on every merge to `main` that touches Go files, and the tag publishes release images, so the merge is a release. Record the tag for Task 12:

```bash
git -C ~/go/src/github.com/getlantern/lantern-box fetch origin --tags
export LB_TAG=$(git -C ~/go/src/github.com/getlantern/lantern-box describe --tags --abbrev=0 origin/main)
echo "$LB_TAG"
```

Expected: a tag after `v0.0.170`.

---

### Task 12: radiance — dependency bumps and the ticket fetcher

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-rooms`

**Files:**
- Modify: `go.mod`, `go.sum` (lantern-box → `LB_TAG`, broflake → `UB_SHA`)
- Create: `helper/tickets.go`
- Create: `helper/tickets_test.go`

**Interfaces:**
- Consumes:
  - `POST /v1/helper/tickets` (plan 1), which returns `{"tickets":[…],"lifetime_seconds":900}` and a 404 when `helper_invites` is off for the client
  - `common.NewRequestWithHeaders`, which sets `X-Lantern-Device-Id`
  - `settings.FeatureOverridesKey`, following `peer/api.go` `API.do`
  - `helperticket.Peek` (Task 1)
- Produces:
  - `type Tickets struct { Inviter, Volunteer []string }`, `var ErrDisabled`, and `func Split([]string) Tickets`
  - `type Fetcher`, with `NewFetcher(*http.Client, baseURL string)` and `(*Fetcher).Fetch(ctx) (Tickets, error)`
  - `type Source`, with `NewSource(fetch func(context.Context) (Tickets, error))`, `Inviter()` and `Volunteer()`
  - `RefreshInterval`, `RetryInterval` and `DisabledInterval`

- [ ] **Step 1: Create the worktree and bump**

```bash
gh pr view 698 -R getlantern/radiance --json state
git -C ~/go/src/github.com/getlantern/radiance fetch origin
```

If the state is `MERGED`, the base is `origin/main`. Otherwise it is `origin/fisk/helper-direct-mode`, and this PR targets that branch:

```bash
BASE=origin/main   # or origin/fisk/helper-direct-mode while #698 is open
git -C ~/go/src/github.com/getlantern/radiance worktree add ~/go/src/github.com/getlantern/radiance-helper-rooms -b fisk/helper-rooms $BASE
cd ~/go/src/github.com/getlantern/radiance-helper-rooms
grep -n 'PeerServePoolKey' common/settings/settings.go
go get github.com/getlantern/lantern-box@$LB_TAG github.com/getlantern/broflake@$UB_SHA
go mod tidy
git diff --stat
```

Expected: `PeerServePoolKey` is defined; the diff touches only `go.mod` and `go.sum`; lantern-box moves to `LB_TAG`, and broflake moves from `v0.0.0-20261018030031-c6268bfc1b56` to the pseudo-version of `UB_SHA`.

```bash
go build ./... && git add go.mod go.sum && git commit -m "Bump lantern-box and broflake for helper pairing rooms"
```

- [ ] **Step 2: Write the failing tests**

`helper/tickets_test.go`:

```go
package helper

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getlantern/broflake/common/helperticket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/radiance/common"
	"github.com/getlantern/radiance/common/settings"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "radiance-helper-settings-*")
	if err != nil {
		panic(err)
	}
	if err := settings.InitSettings(dir); err != nil {
		os.RemoveAll(dir)
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testTickets returns an inviter and a volunteer ticket for one pairing.
func testTickets(t *testing.T) (inviter, volunteer string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	sign := func(role helperticket.Role) string {
		tk, err := helperticket.Sign(priv, helperticket.Ticket{
			PairingID: "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab", Role: role, Expires: time.Now().Add(15 * time.Minute)})
		require.NoError(t, err)
		return tk
	}
	return sign(helperticket.RoleInviter), sign(helperticket.RoleVolunteer)
}

func TestFetcherSplitsTicketsByRole(t *testing.T) {
	inv, vol := testTickets(t)
	require.NoError(t, settings.Set(settings.DeviceIDKey, "device-A"))
	var method, path, device string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, device = r.Method, r.URL.Path, r.Header.Get(common.DeviceIDHeader)
		json.NewEncoder(w).Encode(map[string]any{"tickets": []string{vol, inv, "junk"}, "lifetime_seconds": 900})
	}))
	defer srv.Close()

	got, err := NewFetcher(srv.Client(), srv.URL+"/v1").Fetch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/v1/helper/tickets", path)
	assert.Equal(t, "device-A", device)
	assert.Equal(t, Tickets{Inviter: []string{inv}, Volunteer: []string{vol}}, got)
}

func TestFetcherNotFoundMeansDisabled(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := NewFetcher(srv.Client(), srv.URL+"/v1").Fetch(context.Background())
	assert.ErrorIs(t, err, ErrDisabled)
}

// The response body is tickets, so an error never quotes it.
func TestFetcherErrorOmitsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("eyJzZWNyZXQi.c2ln"))
	}))
	defer srv.Close()
	_, err := NewFetcher(srv.Client(), srv.URL+"/v1").Fetch(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "eyJzZWNyZXQi")
}

// fakeFetch hands out results the test sends, and counts calls.
type fakeFetch struct {
	calls   atomic.Int32
	results chan fetchResult
}

type fetchResult struct {
	t   Tickets
	err error
}

func (f *fakeFetch) fetch(ctx context.Context) (Tickets, error) {
	f.calls.Add(1)
	r := <-f.results
	return r.t, r.err
}

// fakeClock is a settable now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestSourceRefreshesOnRead(t *testing.T) {
	inv, vol := testTickets(t)
	f := &fakeFetch{results: make(chan fetchResult)}
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	src := NewSource(f.fetch)
	src.now = clock.now

	assert.Empty(t, src.Inviter(), "the first read starts a fetch and does not wait for it")
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, time.Second, time.Millisecond)
	f.results <- fetchResult{t: Tickets{Inviter: []string{inv}, Volunteer: []string{vol}}}
	require.Eventually(t, func() bool { return len(src.Inviter()) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, []string{vol}, src.Volunteer())

	clock.add(RefreshInterval - time.Second)
	src.Inviter()
	assert.Never(t, func() bool { return f.calls.Load() > 1 }, 50*time.Millisecond, time.Millisecond, "fetched before the refresh was due")

	clock.add(time.Second)
	src.Volunteer()
	require.Eventually(t, func() bool { return f.calls.Load() == 2 }, time.Second, time.Millisecond)
	f.results <- fetchResult{err: errors.New("network down")}
	require.Eventually(t, func() bool { return !src.isFetching() }, time.Second, time.Millisecond)
	assert.Equal(t, []string{inv}, src.Inviter(), "a failed fetch keeps the last tickets")

	clock.add(RetryInterval)
	src.Inviter()
	require.Eventually(t, func() bool { return f.calls.Load() == 3 }, time.Second, time.Millisecond)
	f.results <- fetchResult{err: ErrDisabled}
	require.Eventually(t, func() bool { return len(src.Inviter()) == 0 }, time.Second, time.Millisecond)
	assert.Empty(t, src.Volunteer(), "helper_invites off clears the tickets")

	clock.add(DisabledInterval - time.Second)
	src.Inviter()
	assert.Never(t, func() bool { return f.calls.Load() > 3 }, 50*time.Millisecond, time.Millisecond, "fetched before the disabled backoff ended")
	clock.add(time.Second)
	src.Inviter()
	require.Eventually(t, func() bool { return f.calls.Load() == 4 }, time.Second, time.Millisecond)
	f.results <- fetchResult{}
}

func TestSourceReturnsCopies(t *testing.T) {
	inv, _ := testTickets(t)
	f := &fakeFetch{results: make(chan fetchResult, 1)}
	f.results <- fetchResult{t: Tickets{Inviter: []string{inv}}}
	src := NewSource(f.fetch)
	src.Inviter()
	require.Eventually(t, func() bool { return len(src.Inviter()) == 1 }, time.Second, time.Millisecond)
	got := src.Inviter()
	got[0] = "changed"
	assert.Equal(t, []string{inv}, src.Inviter())
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./helper/`
Expected: the build fails with `undefined: NewFetcher`, `undefined: NewSource` and `undefined: Tickets`.

- [ ] **Step 4: Implement**

`helper/tickets.go`:

```go
// Package helper fetches and renews this device's helper pairing tickets: the
// short-lived credentials lantern-cloud issues for each pairing the device is
// in, which put it in the pairing's room on freddie. A volunteer widget sends
// its volunteer tickets with every genesis message; an Unbounded consumer sends
// its inviter tickets when it subscribes.
//
// Tickets are credentials: nothing here logs them or a response body.
package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/broflake/common/helperticket"

	"github.com/getlantern/radiance/common"
	"github.com/getlantern/radiance/common/settings"
)

const (
	// RefreshInterval is how long after a successful fetch the next is due.
	// Tickets live 15 minutes, so readers always hold valid ones.
	RefreshInterval = 10 * time.Minute
	// RetryInterval is how long after a failed fetch the next is due. The
	// last tickets are kept meanwhile.
	RetryInterval = time.Minute
	// DisabledInterval is how long after a 404, which means helper_invites is
	// off for this client, the next fetch is due.
	DisabledInterval = time.Hour

	fetchTimeout = 30 * time.Second
	maxBody      = 1 << 20
)

// ErrDisabled is Fetch's error when lantern-cloud has helper invites off for
// this client.
var ErrDisabled = errors.New("helper invites are off for this client")

// Tickets are a device's pairing tickets, by role.
type Tickets struct {
	Inviter   []string
	Volunteer []string
}

// Split sorts tickets by the role each claims. It does not verify them:
// freddie does.
func Split(tickets []string) Tickets {
	var t Tickets
	for _, tk := range tickets {
		switch role, _ := helperticket.Peek(tk); role {
		case helperticket.RoleInviter:
			t.Inviter = append(t.Inviter, tk)
		case helperticket.RoleVolunteer:
			t.Volunteer = append(t.Volunteer, tk)
		}
	}
	return t
}

// Fetcher asks lantern-cloud for this device's tickets.
type Fetcher struct {
	httpClient *http.Client
	baseURL    string
}

// NewFetcher returns a Fetcher. baseURL includes the API version segment,
// as common.GetBaseURL() returns it.
func NewFetcher(httpClient *http.Client, baseURL string) *Fetcher {
	return &Fetcher{httpClient: httpClient, baseURL: baseURL}
}

// Fetch calls POST {baseURL}/helper/tickets, authenticated by the device ID
// header like the peer API, and splits the tickets by role.
func (f *Fetcher) Fetch(ctx context.Context) (Tickets, error) {
	r, err := common.NewRequestWithHeaders(ctx, http.MethodPost, f.baseURL+"/helper/tickets", strings.NewReader("{}"))
	if err != nil {
		return Tickets{}, fmt.Errorf("build ticket request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	if val := settings.GetString(settings.FeatureOverridesKey); val != "" {
		r.Header.Set("X-Lantern-Feature-Override", val)
	}
	resp, err := f.httpClient.Do(r)
	if err != nil {
		return Tickets{}, fmt.Errorf("send ticket request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Tickets{}, ErrDisabled
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Tickets{}, fmt.Errorf("ticket request: status %d", resp.StatusCode)
	}
	var body struct {
		Tickets []string `json:"tickets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return Tickets{}, errors.New("ticket response: malformed JSON")
	}
	return Split(body.Tickets), nil
}

// Source keeps a device's tickets fresh for code that reads them on its
// signaling path. A read never blocks: it returns what the last fetch got,
// and when a fetch is due it starts one in the background. So a reader that
// keeps reading always holds valid tickets, and one that paused for longer
// than a ticket's life sends stale ones once (freddie ignores them) and fresh
// ones from its next read after the fetch. Nothing is fetched until the first
// read, so a device that never reads never calls the API.
type Source struct {
	fetch func(context.Context) (Tickets, error)
	now   func() time.Time

	mu       sync.Mutex
	tickets  Tickets
	due      time.Time // zero: due at the first read
	fetching bool
}

// NewSource returns a Source that fetches with fetch, Fetcher.Fetch outside
// tests.
func NewSource(fetch func(context.Context) (Tickets, error)) *Source {
	return &Source{fetch: fetch, now: time.Now}
}

// Inviter returns the current inviter tickets.
func (s *Source) Inviter() []string {
	return s.read(func(t Tickets) []string { return t.Inviter })
}

// Volunteer returns the current volunteer tickets.
func (s *Source) Volunteer() []string {
	return s.read(func(t Tickets) []string { return t.Volunteer })
}

func (s *Source) read(pick func(Tickets) []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fetching && !s.now().Before(s.due) {
		s.fetching = true
		go s.refresh()
	}
	return slices.Clone(pick(s.tickets))
}

func (s *Source) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	t, err := s.fetch(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetching = false
	switch {
	case errors.Is(err, ErrDisabled):
		s.tickets = Tickets{}
		s.due = s.now().Add(DisabledInterval)
	case err != nil:
		slog.Debug("helper: fetching pairing tickets failed; keeping the last ones", "error", err)
		s.due = s.now().Add(RetryInterval)
	default:
		s.tickets = t
		s.due = s.now().Add(RefreshInterval)
	}
}

// isFetching reports whether a fetch is in flight. For tests.
func (s *Source) isFetching() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetching
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./helper/ -v`
Expected: PASS for `TestFetcherSplitsTicketsByRole`, `TestFetcherNotFoundMeansDisabled`, `TestFetcherErrorOmitsTheBody`, `TestSourceRefreshesOnRead` and `TestSourceReturnsCopies`, with no race reports.

- [ ] **Step 6: Commit**

```bash
git add helper/tickets.go helper/tickets_test.go
git commit -m "helper: fetch and renew helper pairing tickets"
```

---

### Task 13: radiance — volunteer tickets and serve-pool on the widget, and a restart when a pairing ends

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-rooms`

**Files:**
- Modify: `unbounded/unbounded.go`
- Create: `unbounded/rooms_test.go`
- Modify: `backend/radiance.go` (before `unbounded.InitSubscription(cachedCfg)`, line 336)

**Interfaces:**
- Consumes:
  - `WebRTCOptions.SetRoomTickets`, `SetServePool`, `CurrentRoomTickets` and `ServesPool` (Task 2)
  - `helperticket.Peek` (Task 1), `helper.Source` (Task 12), and `settings.PeerServePoolKey` (plan 2, PR #698)
  - the test helpers `resetManager`, `primeManager`, `fakeWidget` and `waitForCount`
- Produces:
  - `func SetTicketSource(src roomTickets)`, where `type roomTickets interface{ Volunteer() []string }`
  - `var roomSyncInterval = 30 * time.Second`
  - `applyRooms`, `(*unboundedManager).syncRooms`, `servePool` and `volunteerPairings`

**Why restart:** freddie stops new matches as soon as a pairing's tickets lapse, but an established WebRTC session outlives them. The widget cannot tell which of its sessions came through which room. When a pairing it volunteered for disappears from a fresh ticket set, it restarts, which ends every session, so the ended pairing's session ends within one ticket fetch of the end (≤10 minutes plus 30 s). Restarts happen only when a pairing ends, which is rare. Pool consumers reconnect at once.

- [ ] **Step 1: Write the failing tests**

`unbounded/rooms_test.go`:

```go
package unbounded

import (
	"crypto/ed25519"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common/helperticket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/radiance/common/settings"
)

// fakeTickets is a settable ticket source.
type fakeTickets struct {
	mu sync.Mutex
	v  []string
}

func (f *fakeTickets) Volunteer() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.v)
}

func (f *fakeTickets) set(v ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.v = v
}

func volunteerTicket(t *testing.T, priv ed25519.PrivateKey, pairing string) string {
	t.Helper()
	tk, err := helperticket.Sign(priv, helperticket.Ticket{PairingID: pairing, Role: helperticket.RoleVolunteer, Expires: time.Now().Add(15 * time.Minute)})
	require.NoError(t, err)
	return tk
}

// recordingWidgets resets the manager with a factory that records each
// widget and its WebRTC options, and stops the manager at cleanup.
func recordingWidgets(t *testing.T) (count *atomic.Int32, opts func(i int) *clientcore.WebRTCOptions, widgets func(i int) *fakeWidget) {
	t.Helper()
	var mu sync.Mutex
	var gotOpts []*clientcore.WebRTCOptions
	var gotWidgets []*fakeWidget
	count = &atomic.Int32{}
	resetManager(t, func(_ *clientcore.BroflakeOptions, rtc *clientcore.WebRTCOptions, _ *clientcore.EgressOptions) (widget, error) {
		w := &fakeWidget{}
		mu.Lock()
		gotOpts = append(gotOpts, rtc)
		gotWidgets = append(gotWidgets, w)
		mu.Unlock()
		count.Add(1)
		return w, nil
	})
	t.Cleanup(func() { manager.stop() })
	opts = func(i int) *clientcore.WebRTCOptions { mu.Lock(); defer mu.Unlock(); return gotOpts[i] }
	widgets = func(i int) *fakeWidget { mu.Lock(); defer mu.Unlock(); return gotWidgets[i] }
	return count, opts, widgets
}

func fastRoomSync(t *testing.T) {
	t.Helper()
	prev := roomSyncInterval
	roomSyncInterval = 10 * time.Millisecond
	t.Cleanup(func() { roomSyncInterval = prev })
}

const (
	pairingA = "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"
	pairingB = "0b6f3a52-2d4e-4f7a-8c1d-9e0f1a2b3c4d"
	pairingC = "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a"
)

func TestWidgetCarriesVolunteerTicketsAndServesThePoolByDefault(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	count, opts, _ := recordingWidgets(t)
	src := &fakeTickets{}
	src.set(volunteerTicket(t, priv, pairingA))
	SetTicketSource(src)
	primeManager(t, nil)

	manager.start()
	waitForCount(t, count, 1, 2*time.Second)
	assert.Equal(t, src.Volunteer(), opts(0).CurrentRoomTickets())
	assert.True(t, opts(0).ServesPool(), "help others too is on by default")
}

func TestWidgetWithoutTicketSourceJoinsNoRooms(t *testing.T) {
	count, opts, _ := recordingWidgets(t)
	primeManager(t, nil)
	manager.start()
	waitForCount(t, count, 1, 2*time.Second)
	assert.Empty(t, opts(0).CurrentRoomTickets())
	assert.True(t, opts(0).ServesPool())
}

func TestWidgetFollowsTheServePoolSetting(t *testing.T) {
	fastRoomSync(t)
	require.NoError(t, settings.Set(settings.PeerServePoolKey, false))
	t.Cleanup(func() { _ = settings.Set(settings.PeerServePoolKey, true) })
	count, opts, _ := recordingWidgets(t)
	SetTicketSource(&fakeTickets{})
	primeManager(t, nil)

	manager.start()
	waitForCount(t, count, 1, 2*time.Second)
	assert.False(t, opts(0).ServesPool(), "help others too off: rooms only")

	require.NoError(t, settings.Set(settings.PeerServePoolKey, true))
	require.Eventually(t, func() bool { return opts(0).ServesPool() }, 2*time.Second, 10*time.Millisecond,
		"a running widget picks the setting up without a restart")
	assert.EqualValues(t, 1, count.Load())
}

// Renewed tickets reach the running widget; a new pairing needs no restart;
// an ended pairing restarts the widget, ending its sessions.
func TestWidgetRestartsWhenAPairingEnds(t *testing.T) {
	fastRoomSync(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	count, opts, widgets := recordingWidgets(t)
	src := &fakeTickets{}
	a, b := volunteerTicket(t, priv, pairingA), volunteerTicket(t, priv, pairingB)
	src.set(a, b)
	SetTicketSource(src)
	primeManager(t, nil)
	manager.start()
	waitForCount(t, count, 1, 2*time.Second)

	renewedA, c := volunteerTicket(t, priv, pairingA), volunteerTicket(t, priv, pairingC)
	src.set(renewedA, b, c)
	require.Eventually(t, func() bool { return slices.Equal(opts(0).CurrentRoomTickets(), []string{renewedA, b, c}) },
		2*time.Second, 10*time.Millisecond, "renewed and added tickets reach the running widget")
	assert.Never(t, func() bool { return count.Load() > 1 }, 100*time.Millisecond, 10*time.Millisecond,
		"renewing or adding a pairing must not restart the widget")

	src.set(b, c)
	waitForCount(t, count, 2, 2*time.Second)
	assert.EqualValues(t, 1, widgets(0).stopCalled.Load(), "the old widget, and its sessions, stopped")
	assert.Equal(t, []string{b, c}, opts(1).CurrentRoomTickets())
	assert.Never(t, func() bool { return count.Load() > 2 }, 100*time.Millisecond, 10*time.Millisecond,
		"one ended pairing is one restart")
}

func TestVolunteerPairings(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	got := volunteerPairings([]string{volunteerTicket(t, priv, pairingA), volunteerTicket(t, priv, pairingA), "junk"})
	assert.Equal(t, map[string]bool{pairingA: true}, got)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./unbounded/ -run 'Widget|VolunteerPairings'`
Expected: the build fails with `undefined: SetTicketSource`, `undefined: roomSyncInterval` and `undefined: volunteerPairings`.

- [ ] **Step 3: Implement**

In `unbounded/unbounded.go`, add `"github.com/getlantern/broflake/common/helperticket"` to the imports. Add a field to `unboundedManager` after `lastFeatureOn bool`:

```go
	// tickets supplies the volunteer tickets for this device's helper
	// pairings. nil: the widget joins no rooms.
	tickets roomTickets
```

Add, after `donorSTUNBatch`:

```go
// roomTickets is where the widget gets its helper pairing tickets;
// helper.Source in production.
type roomTickets interface {
	Volunteer() []string
}

// SetTicketSource sets where the widget gets its volunteer tickets. A widget
// already running keeps its source until it restarts.
func SetTicketSource(src roomTickets) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.tickets = src
}

// roomSyncInterval is how often a running widget re-reads its tickets and the
// serve-pool setting. The source is a cache that renews tickets every 10
// minutes, so this costs nothing. A variable so tests can shorten it.
var roomSyncInterval = 30 * time.Second

// servePool is the "help others too" setting, shared with direct mode: unset
// means on.
func servePool() bool {
	return settings.Get(settings.PeerServePoolKey) == nil || settings.GetBool(settings.PeerServePoolKey)
}

// applyRooms gives a widget's options the current volunteer tickets and the
// serve-pool setting.
func applyRooms(rtcOpt *clientcore.WebRTCOptions, tickets roomTickets) {
	var vt []string
	if tickets != nil {
		vt = tickets.Volunteer()
	}
	rtcOpt.SetRoomTickets(vt)
	rtcOpt.SetServePool(servePool())
}

// volunteerPairings is the set of pairing IDs tickets claim. It is unverified
// and is used only to notice a pairing disappearing; it is never logged.
func volunteerPairings(tickets []string) map[string]bool {
	out := make(map[string]bool, len(tickets))
	for _, tk := range tickets {
		if _, id := helperticket.Peek(tk); id != "" {
			out[id] = true
		}
	}
	return out
}

// syncRooms keeps a running widget's room tickets and pool setting current
// until ctx ends. When a pairing it volunteers for is missing from the fresh
// set, the pairing has ended. freddie stops new matches by itself, but a
// WebRTC session already open outlives the tickets, and the widget cannot tell
// which session came through which room. So it restarts, ending every session.
func (m *unboundedManager) syncRooms(ctx context.Context, rtcOpt *clientcore.WebRTCOptions, tickets roomTickets) {
	prev := volunteerPairings(rtcOpt.CurrentRoomTickets())
	t := time.NewTicker(roomSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		applyRooms(rtcOpt, tickets)
		cur := volunteerPairings(rtcOpt.CurrentRoomTickets())
		for id := range prev {
			if !cur[id] {
				slog.Info("Unbounded: a helper pairing ended; restarting the widget to close its sessions")
				go func() {
					m.stop()
					m.start()
				}()
				return
			}
		}
		prev = cur
	}
}
```

In `start()`, read the source with the rest of the state, right after `ucfg := m.lastCfg`:

```go
	tickets := m.tickets
```

In the worker goroutine, after the `rtcOpt` block that sets `DiscoverySrv` and `Endpoint`, add:

```go
		// Helper pairing rooms: freddie offers this widget's genesis messages
		// to its paired consumers first, and with "help others too" off to
		// nobody else.
		applyRooms(rtcOpt, tickets)
```

After the success log `slog.Info("Unbounded: broflake widget proxy started")`, add:

```go
		go m.syncRooms(ctx, rtcOpt, tickets)
```

In `backend/radiance.go`, add `"github.com/getlantern/radiance/helper"` to the imports, and insert before `unbounded.InitSubscription(cachedCfg)`:

```go
	// The widget joins its helper pairings' rooms on freddie with the
	// volunteer tickets lantern-cloud issues this device, fetched on first use
	// and renewed in the background.
	unbounded.SetTicketSource(helper.NewSource(helper.NewFetcher(kindling.HTTPClient(), common.GetBaseURL()).Fetch))
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./unbounded/ ./backend/ -v -run 'Widget|VolunteerPairings|Unbounded' && go test -race ./unbounded/ ./backend/`
Expected: PASS for the five new tests and every existing `unbounded` and `backend` test, with no race reports.

- [ ] **Step 5: Commit**

```bash
git add unbounded/unbounded.go unbounded/rooms_test.go backend/radiance.go
git commit -m "unbounded: serve helper pairing rooms first and restart when a pairing ends"
```

---

### Task 14: radiance — inviter tickets for the tunnel's Unbounded consumer

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-rooms`

**Files:**
- Modify: `vpn/tunnel.go` (`tunnel.init`, after `lbA.ContextWithDirectTransport`, lines 200-203)
- Create: `vpn/room_tickets_test.go`

**Interfaces:**
- Consumes: `lbA.ContextWithRoomTicketSource` and `RoomTicketSourceFromContext` (Task 10); `helper.Source` and `Fetcher` (Task 12).
- Produces: `func withHelperRoomTickets(ctx context.Context, src *helper.Source) context.Context`. The tunnel's box context carries the inviter tickets, and the Unbounded outbound reads them at every subscribe. On mobile the tunnel runs in its own process, so it has its own `Source`, separate from the widget's. It fetches only once an Unbounded outbound subscribes, because the source is lazy, so a VPN user with no Unbounded outbound never calls the ticket API.

- [ ] **Step 1: Write the failing test**

`vpn/room_tickets_test.go`:

```go
package vpn

import (
	"context"
	"slices"
	"testing"
	"time"

	lbA "github.com/getlantern/lantern-box/adapter"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/radiance/helper"
)

func TestWithHelperRoomTicketsGivesTheConsumerInviterTickets(t *testing.T) {
	src := helper.NewSource(func(context.Context) (helper.Tickets, error) {
		return helper.Tickets{Inviter: []string{"i.1"}, Volunteer: []string{"v.1"}}, nil
	})
	get := lbA.RoomTicketSourceFromContext(withHelperRoomTickets(context.Background(), src))
	require.NotNil(t, get)
	require.Eventually(t, func() bool { return slices.Equal(get(), []string{"i.1"}) }, 2*time.Second, 10*time.Millisecond,
		"the consumer gets the inviter tickets, never the volunteer ones")
}
```

Run: `go test ./vpn/ -run WithHelperRoomTickets`
Expected: the build fails with `undefined: withHelperRoomTickets`.

- [ ] **Step 2: Implement**

In `vpn/tunnel.go`, add `"github.com/getlantern/radiance/helper"` to the imports. Insert after `boxCtx = lbA.ContextWithDirectTransport(boxCtx, ...)`:

```go
	// An Unbounded consumer joins its helper pairings' rooms on freddie with
	// the inviter tickets lantern-cloud issues this device. The outbound reads
	// them at every subscribe; the source fetches them on first use and renews
	// them in the background, outside the tunnel like the signaling itself.
	boxCtx = withHelperRoomTickets(boxCtx, helper.NewSource(helper.NewFetcher(kindling.HTTPClient(), common.GetBaseURL()).Fetch))
```

Append to `vpn/tunnel.go`:

```go
// withHelperRoomTickets gives the Unbounded outbound src's inviter tickets.
func withHelperRoomTickets(ctx context.Context, src *helper.Source) context.Context {
	return lbA.ContextWithRoomTicketSource(ctx, src.Inviter)
}
```

- [ ] **Step 3: Run the tests**

Run: `go test -race ./vpn/ -run 'WithHelperRoomTickets' -v && make test`
Expected: PASS, and `make test` passes for the whole module.

- [ ] **Step 4: Telemetry audit**

Run: `git diff $BASE -- . ':!go.sum' | grep -nE '^\+.*(slog\.|log\.)'`
Expected: only `"helper: fetching pairing tickets failed; keeping the last ones"` with `error` (whose text never includes a body, as `TestFetcherErrorOmitsTheBody` checks) and `"Unbounded: a helper pairing ended; restarting the widget to close its sessions"`. Neither carries a ticket or pairing ID.

- [ ] **Step 5: Commit and open the PR**

```bash
git add vpn/tunnel.go vpn/room_tickets_test.go
git commit -m "vpn: give the Unbounded consumer this device's inviter tickets"
```

Open the PR with `skills:create-pr`, with base `main`, or `fisk/helper-direct-mode` while #698 is open.

- [ ] **Step 6: [Human] Merge and release.**
  - Merge after the lantern-cloud PR (Task 9) is deployed and the lantern-box tag (Task 11) is published.
  - Until a client turns the `helper_invites` flag on, `/v1/helper/tickets` returns 404, the sources back off for an hour, and nothing changes on the wire.
  - Shipping radiance in a lantern app release (a radiance bump in `lantern`) is a production step.
  - Enabling `helper_invites` for the pilot is also a production step. The flag targets by IP and country, so it must cover volunteers abroad as well as inviters, because volunteers fetch tickets through the same gate.

---

## Self-review

### Spec coverage: Section 2, "Unbounded"

| Spec bullet | How this plan implements it | Tasks |
|---|---|---|
| A volunteer's widget announces into each of its pairing rooms, presenting a ticket for each | Every genesis POST carries the widget's volunteer tickets as repeated `room-ticket` fields, one per pairing and up to 20. radiance fetches the tickets, refreshes them every 10 minutes inside their 15-minute life, and re-applies them every 30 s with no broflake restart (`SetRoomTickets`). freddie verifies each ticket for role `volunteer`. | 2, 3, 5, 12, 13 |
| Sara's client subscribes to her pairing rooms (with tickets) as well as to the general pool | The consumer's GET carries its inviter tickets in `X-BF-Room-Tickets`, read from a ctx-injected source at every subscribe. freddie indexes it in `byRoom` under each verified pairing ID and, as before, in `Data` and `byCountry`, so it still receives pool genesis messages. | 2, 3, 4, 5, 10, 14 |
| Genesis messages addressed to a room go only to that room's consumers. A volunteer that helps others too also broadcasts as today | A room genesis goes only to consumers indexed in that room, marked `"Room":true`. If no room consumer takes it and `pool` is not `0`, the same genesis falls through to country targeting and broadcast, exactly as today. With `pool=0` it goes nowhere. "Also broadcasts" is per slot rather than simultaneous: each of the widget's slots (5 by default) runs its own genesis loop, so a pool-serving volunteer is broadcast whenever no room consumer is waiting. | 4, 5, 11 |
| The volunteer gives a waiting room consumer the next free slot before any pool consumer | `DeliverGenesis` tries the producer's rooms before country or broadcast, for every genesis, which is to say every free slot. A consumer that hears both room and pool genesis messages answers a room one (`pickGenesis`). Both are tested: in freddie (`TestHandleSignal_RoomPrecedence`) and in clientcore (`TestPickGenesisPrefersRooms`). | 3, 4, 5 |
| freddie verifies tickets with the public key and learns only opaque pairing IDs | `helperticket.Verify` checks the signature under the key the ticket names, the expiry, the role and the format, against lantern-cloud's golden vectors. cmd/freddie fetches the public key list over HTTPS and holds no secret. Tickets carry no device ID (plan 1, F1). Rooms are pairing IDs in memory only. `TestHandleSignal_RoomsLeaveNoTrace` checks logs, spans and metrics. | 1, 5, 6, 9 |

### Spec coverage: "Revocation" (Unbounded half) and "Pairing tickets"

| Spec statement | Implementation | Tasks |
|---|---|---|
| lantern-cloud stops issuing tickets for the pairing | This is unchanged from plan 1: `EndPairing` deletes the row and `IssueTickets` lists live pairings only (`TestHelperNoTicketsAfterEnd`). | — |
| In Unbounded, freddie drops the room membership | Membership is not stored past one request. freddie re-verifies every ticket at every subscribe and every genesis, and a consumer's membership lasts one GET (`consumerTTL`, 20 s). An expired ticket admits nobody (`TestHandleSignal_BadRoomTicketsIgnored`). | 4, 5 |
| Access ends within one ticket lifetime (15 minutes) at most | New matches stop within 15 minutes and 20 seconds: the last ticket's life plus the GET it opened. An established WebRTC session outlives tickets, so the volunteer's widget restarts when a pairing it serves disappears from its next ticket fetch, which ends the session within about 10.5 minutes of the end (`TestWidgetRestartsWhenAPairingEnds`). If the volunteer cannot reach lantern-cloud, the session lasts until its next successful fetch. See "Not fully covered" item 1. | 5, 13 |
| Within seconds where a revocation can be pushed | Not covered. freddie and the widget have no push channel from lantern-cloud. See "Deferred". | — |
| "freddie and the volunteer's proxy verify tickets with lantern-cloud's public key and need no database" | freddie verifies against the key list it fetches. There is no database. | 1, 9 |
| Threat model: "Ticket key compromise … rotate the key and reissue" | Tickets name their key (`"k"`). `/v1/helper/ticket-key` lists the current and previous keys. `previous_seed` and `generation` in Vault, plus the Task 9 runbook, rotate the key with or without an overlap. | 6, 7, 8, 9 |

### Deferred items from earlier plans

| Item | Where it is done |
|---|---|
| Plan 1 F7: ticket-key rotation (a key ID and a key list) | Task 6 adds `"k"` and `TicketKeyID` and regenerates the vectors. Task 7 adds the key list and `previous_seed`. Task 9 makes freddie the first verifier and adds the rotation runbook. |
| Plan 2: "the pairing short-ID revision should change when the key changes" | Task 8 makes the revision `version<<36 \| keygen<<20 \| epoch&0xFFFFF`, with the bit budget, positivity and plan-2 upgrade tested. A rotation also keeps the previous key's short IDs accepted until it ends, so inviters are not cut off between the volunteer's next heartbeat and their own next config fetch. |
| Plan 2: "Plan 3 must keep K_sid derived from the current seed" | `helperPeerSIDKey` is still `helperPeerSIDKeyFor(current)` (Task 7). The previous key only adds accepted IDs on the volunteer side. |

### Not fully covered in this plan

1. **Session teardown after revocation depends on the volunteer.** freddie bounds new matches to 15 minutes, but the open WebRTC session ends only when the widget restarts at its next successful ticket fetch. A volunteer that cannot reach lantern-cloud keeps the session open. A consumer-side teardown is not possible without trusting the consumer. A volunteer-side push needs the push channel deferred below.
2. **No pre-emption.** A room consumer gets a volunteer's next free slot, not a slot already serving a pool consumer. A pool-serving volunteer whose 5 slots are all busy makes its paired inviter wait for one to free. The same trade-off was made in direct mode (plan 2, "no hard reservation").
3. **Header size at the cap.** 20 inviter tickets make an `X-BF-Room-Tickets` of about 4.4 KB. That is within Go's and common CDN header limits, but the pilot should confirm that each fronted path to freddie (CloudFront, Akamai, Aliyun) passes it. An inviter has at most 20 pairings, and most have one or two.
4. **New pairings reach rooms within one refresh.** A newly approved pairing's tickets reach the tunnel and the widget at the next fetch (≤10 minutes), or up to an hour later if the device had a 404 because the flag was off. Plan 4's approval flow should invalidate the source.

### Deliberately deferred

- **Push revocation (seconds).** This needs a channel from lantern-cloud to freddie and to volunteer widgets, for example a revocation list freddie polls with its keys, or a server-sent event to radiance. It is a follow-up after the pilot measures actual revocation times (Section 4, "Production pilot checks").
- **Plan 4, browser volunteers:** the wasm widget on `lantern.io/help` has no device ID. It needs a ticket issued to the browser session, and `setRoomTickets` and `setServePool` exported through `clientcore/ui_wasm_impl.go`, beside `setTargetCountries`.
- **Plan 4, app:**
  - the "Help others too" toggle and its IPC exposure (`settings.PeerServePoolKey`)
  - dispatching a change to it through `PatchSettings` to `unbounded.Apply`. Until then a change reaches a running widget within 30 s through `syncRooms`.
  - invalidating the ticket source when a pairing is approved
- **Release:** a radiance bump in the lantern app, and turning on `helper_invites` for the pilot. Both are [Human] production steps after Task 14.

### Placeholder and consistency check

- No step says "TBD", "add error handling" or "similar to Task N". The values not known in advance, `UB_SHA` (Task 5 Step 8) and `LB_TAG` (Task 11 Step 6), come with the commands that produce them. Plan 2's branch head comes from Task 6 Step 1.
- Names are consistent across repos:
  - broflake: `helperticket.{Sign, Verify, Peek, RoomFunc, KeyID, NewKey}`, `common.{RoomTicketsHeader, RoomTicketField, PoolField, MaxRoomTickets, MaxRoomTicketLen, NormalizeRoomTickets, ParseRoomTicketsHeader, IsRoomGenesis}`, and `WebRTCOptions.{SetRoomTickets, SetServePool, CurrentRoomTickets, ServesPool, RoomTicketSource, RoomsOnly}`, all defined in Tasks 1–2.
  - `Freddie.RoomTicket` (Task 5) is set by cmd/freddie's `ticketKeys.Room` (Task 9) and by the e2e test's `helperticket.RoomFunc` (Task 11).
  - lantern-box's `ContextWithRoomTicketSource` (Task 10) is used by radiance's `withHelperRoomTickets` (Task 14).
  - radiance's `helper.{Source, Fetcher, Tickets}` (Task 12) are used in Tasks 13 and 14.
- The vectors: `common/helperticket/testdata/vectors.json` (Task 1) and lantern-cloud's regenerated `ticket` section (Task 6) are diffed in Task 6 Step 6, and cmd/freddie's tests verify lantern-cloud's own vectors with broflake's verifier (Task 9).
