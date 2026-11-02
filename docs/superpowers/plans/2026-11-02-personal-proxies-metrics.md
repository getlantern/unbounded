# Personal Proxies Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Count how personal proxies are used (the invite funnel, its timings, live pairings, and paired and residential traffic) as OpenTelemetry metrics from lantern-cloud, broken down by inviter and volunteer country, and show them on a SigNoz dashboard.

**Architecture:** lantern-cloud records invite and inviter countries on the invite and pairing rows and counts each helper handler's outcome. A sampler on each replica exports gauges for live pairings and online volunteers. Volunteer devices (radiance) keep running byte totals per pairing. Direct mode reports them, with total relayed bytes, on the existing peer heartbeat. Rooms mode reports them on the existing ticket refresh. lantern-cloud checks that the device owns each pairing, adds the bytes to counters and discards them. The lantern app picks the new radiance up through a version bump, and a SigNoz dashboard reads the metrics.

**Tech Stack:** Go, PostgreSQL migrations and sqlc (pgx/v5), protobuf with buf and protojson HTTP bindings, the OpenTelemetry Go metrics SDK through lantern-cloud's `metrics` package, SigNoz (MCP tools).

**Spec:** `docs/superpowers/specs/2026-11-02-personal-proxies-metrics-design.md` (in getlantern/unbounded). Prior context: `docs/superpowers/specs/2026-10-20-helper-invites-design.md`.

## Global Constraints

Copied from the spec:

- Labels: "Outcome labels, `inviter_cc` and `volunteer_cc` (ISO country, or `unknown`), and `mode` (`direct`, `rooms`) where it applies. Never a pairing ID, invite hash, device ID, name, IP or account."
- "Reported values are not stored, logged or traced. The pairing ID is used only to find the country, inside the request. That keeps the existing telemetry rule: no logs or traces tie a pairing ID to an IP address, a display name or an account."
- Trust: "A device can only report on its own pairings, and each value is capped per report: anything over 50 GB per 150-second heartbeat or per ticket refresh is dropped and counted as `helper.relayed.rejected`. The numbers measure usage. They are not billing-grade and don't back rewards."
- Missing countries: "Existing rows stay NULL and are reported as `unknown`."
- Pool: "Pool bytes are the total minus paired bytes, floored at 0."
- Gauges: "Gauges are sampled by every api replica. The dashboard therefore takes the max across instances, never the sum."
- Migrations: lantern-cloud's deploy does not apply them. An operator runs `bin/migrator migrate up --live` on staging and production before the lantern-cloud PR merges. Agents never run a migration or any SQL against staging or production. One table per migration, each with `SET lock_timeout = '5s'`.

Process and repository rules:

- **Never run `mise r codegen:sqlc` locally.** Its `__db_ready` dependency runs `DROP DATABASE IF EXISTS api WITH (FORCE)` against `localhost:5432`, which wipes the local api database. Regenerate `cmd/api/dal` only with the scratch-database procedure in Task 1, Step 4. Never hand-edit `cmd/api/dal/*.go`. If a generated name differs from what this plan says, change the callers to the generated name.
- lantern-cloud tests: `mise r tests:run -- --pkg ./cmd/api/ --run '<regex>'`. This runs through `bin/testsetup`, which gives each worktree its own Docker stack on dynamic ports and never touches `localhost:5432`.
- lantern-cloud lint, before each lantern-cloud commit: `golangci-lint run --new-from-rev=origin/main ./...` and `mise r checks:log-attr-keys`.
- lantern-cloud proto codegen: `mise r codegen:proto:api` (buf with remote plugins, no database). Commit every file it changes: `cmd/api/apipb/services/*.pb.go`, `openapiv2/services/*.swagger.json` and `cmd/backoffice/src/gen/services/*`.
- Metric recorders run on `trace.Detached(ctx)`, the same as `observeHelperResponse`, so that no exemplar ties a metric point to a request trace.
- Work in git worktrees made from `origin/main` (superpowers:using-git-worktrees), one per repo. Do not commit in the existing checkouts. After deleting a lantern-cloud worktree, run `bin/clean-orphan-containers`.
- Use opus for every implementer and reviewer subagent. This feature touches a privacy boundary and a client/server protocol.
- One PR per repo, each opened with the `skills:create-pr` skill (its Codex gate is required, and a hook blocks bare `gh pr create`). Pass `--body-file` a literal path such as `/tmp/pr-body.md`, never `$VAR`. Order: lantern-cloud merges and deploys first, then radiance, then the lantern bump, then the dashboard.
- radiance: every edit that adds or changes a comment needs a review by a separate comment-reviewer subagent before the task is done (radiance `AGENTS.md`, "Comment Verification"). Use the `skills:comment-analyzer` agent on the task's diff, fix what it reports, and run it again until it reports nothing. A self-review does not count.
- lantern: when a Go module pin changes, run `go mod tidy` and commit `go.mod` and `go.sum` together in the bump commit.
- Comments describe the code as it is: no "previously" or "the plan said" (lantern-cloud and radiance `AGENTS.md`).

## Rulings on the spec's open points

- **(a) Country.** The helper handlers already resolve `p.clientCountry` the same way `/v1/config-new` does. `helperRequest` (helper_handler.go:290-294) and `peerRequest` (peer_handler.go:249) call `util.ClientIPString` and `maxmind.LookupCountryASNState`. `clientService.config` (config.go:1777-1779) uses the same two functions. radiance sends every helper, peer and config call through `common.NewRequestWithHeaders`, which sets `X-Lantern-Config-Client-IP` (the client's detected public IP, which `ClientIPWithAddr` prefers) and the time-zone header. So a censored user's API calls through Lantern proxies resolve to the user's country, not an egress. Nothing new is needed. Labels normalize the value through `helperCC` (two capital letters, else `unknown`). Rows store it through `helperStoredCC` (two capital letters, else NULL), so a junk `X-Lantern-Client-Country` override can't break the CHECK.
- **(b) Claim timestamp.** None exists. Migration 000161 adds `helper_invites.claimed_at`, set by `ClaimHelperInvite`. `decide_seconds` falls back to `created_at` only for claims made before the migration. The volunteer's country also has to reach the pairing from the claim, so the same migration adds `helper_invites.volunteer_country`, set at claim and cleared at approval or rejection along with the other volunteer fields.
- **Column type.** The columns are `VARCHAR(2) CHECK (... ~ '^[A-Z]{2}$')`, not `char(2)`. That matches the repo's existing country columns (`peer_routes.country_code VARCHAR(2)`, migration 000085). It also maps to `sql.NullString` through the sqlc overrides, where `char(2)` would come out as `pgtype.Text`. Both hold the same two letters.
- **(c) Pairing identifier.** Both modes report the pairing ID (a UUID string). The direct-mode volunteer gets it from `pairing_short_id_sets` (`PairingShortIDSet.PairingID`, which `pairingByShortID` maps to). Rooms gets it from broflake's `ConsumerInfo.Pairing`. The server then needs a single indexed statement (`ListHelperPairingCountriesForVolunteer`: `volunteer_device_id = $1 AND pairing_id = ANY($2)`) that both checks ownership and returns `inviter_country`. A short ID would need K_sid derivation over every epoch and key first.
- **(d) Gauge sampling.** `runHelperInviteSweep` runs on every non-local api replica (main.go:567), so no single replica owns the sweep. Each replica runs its own sampler, and the dashboard takes the max. The sweep's `DELETE ... RETURNING` hands each expired row to exactly one replica, so the expired counts don't double.
- **(e) Result labels.**
  - Requests the gate refuses (flag off, missing device ID) or protovalidate refuses (400) are not counted here. `helper.requests` already counts them.
  - CreateInvite: source rate limit → `rate_limited`; 20 pending → `invite_cap` (checked first); 20 in 24 h → `daily_cap`; database error or a duplicate hash (409) → `error`; success → `ok`. `inviter_cc` is always the request's country.
  - ClaimInvite: source rate limit → `rate_limited` (`inviter_cc` unknown, to keep the limited path cheap); volunteer at 20 pairings → `pair_cap` (`inviter_cc` read from the live invite); claim matched nothing → `already_claimed` if an unexpired invite with that hash exists (any state, the self-claim included), else `not_found` with `inviter_cc` `unknown`; database error → `error`. The client's response doesn't change: it still can't tell these cases apart.
  - DecideInvite: no such invite (404), an invite not in `claimed`, or a lost race → `no_pending_claim`; bad signature (403), pair cap (429) and database errors → `error`. The spec's label set has no `pair_cap` for decisions, so a cap at approval reads as `error`. `decision` comes from `approve`. The countries come from the invite row, or from the request's country before the invite is loaded.
  - EndPairing: counted only when a row is deleted, and `by` is the caller's side of the pairing. A 404 isn't counted.
- **Traffic details.**
  - The `kind=paired` part of `peer.relayed.bytes` counts only paired bytes that pass the ownership check and the cap, so pool is the total minus those.
  - When the total is over the cap, that heartbeat records no `peer.relayed.bytes` at all. The pairing entries are still capped and counted one by one.
  - `helper.relayed.rejected` is labelled `mode`.
  - `helper.tickets.issued` gets `mode=direct` when the pairing's volunteer has a live verified peer route, the same test ListPairings' online status uses, and `rooms` otherwise.
  - `helper.volunteers.online` counts paired volunteer devices that are online by ListPairings' definition: a live peer route, or a ticket fetch in the last 15 minutes. Presence alone would miss direct-only volunteers.
  - Malformed report entries are dropped, not refused with a 400. A 400 on the heartbeat would make the peer stop serving.
  - Browser volunteers (help.lantern.io) send `{}` and so report no rooms bytes. Only radiance devices report traffic, as the spec scopes it.
- **lantern bump.** Needed: the app embeds radiance, and devices report nothing until the app ships the new radiance.

## File structure

lantern-cloud (`/Users/afisk/go/src/github.com/getlantern/lantern-cloud`, branch `feat/personal-proxies-metrics`):
- Create `cmd/api/migrations/infra/000161_helper_metrics.{up,down}.sql`: country and claim-time columns.
- Modify `cmd/api/queries/infra/helper.sql`; regenerate `cmd/api/dal/helper.sql.go` and `cmd/api/dal/models.go`.
- Modify `metrics/metrics.go`, `metrics/instruments.go`, `metrics/metrics_test.go`: `RecordWaitDistribution`.
- Create `cmd/api/helper_metrics.go`: metric names, label values, country normalization, recorders.
- Create `cmd/api/helper_gauges.go`: expiry sweep counting and the gauge sampler.
- Create `cmd/api/helper_traffic.go`: usage report validation and byte counters.
- Modify `cmd/api/helper_handler.go` (CreateInvite, ClaimInvite, DecideInvite, EndPairing, IssueTickets, runHelperInviteSweep), `cmd/api/peer_handler.go` (Heartbeat), `cmd/api/main.go` (start the sampler).
- Modify `cmd/api/apipb/proto/services/helper.proto` and `peer.proto`, and regenerate their outputs.
- Tests: `cmd/api/helper_metrics_store_db_test.go`, `cmd/api/helper_metrics_test.go`, `cmd/api/helper_metrics_db_test.go`, `cmd/api/helper_gauges_db_test.go`, `cmd/api/helper_traffic_db_test.go`.

radiance (`/Users/afisk/go/src/github.com/getlantern/radiance`, branch `feat/personal-proxies-metrics`):
- Create `helper/usage.go` and `helper/usage_test.go`: `PairedBytes` and `Usage`.
- Modify `helper/tickets.go` and `helper/tickets_test.go`: the ticket fetch carries rooms usage.
- Modify `unbounded/unbounded.go` and `unbounded/snapshot.go`, test in `unbounded/snapshot_test.go`: credit paired slot bytes.
- Modify `backend/radiance.go`: wiring.
- Modify `peer/snapshot.go` and `peer/snapshot_test.go`: direct-mode accumulators.
- Modify `peer/api.go`, `peer/peer.go`, `peer/peer_test.go` and `peer/pairing_test.go`: the heartbeat carries usage.

lantern (`/Users/afisk/go/src/github.com/getlantern/lantern`, branch `bump-radiance-personal-proxies-metrics`): `go.mod`, `go.sum`.

SigNoz: a dashboard named "Personal proxies".

---

## lantern-cloud

### Task 1: Schema and queries

**Files:**
- Create: `cmd/api/migrations/infra/000161_helper_metrics.up.sql`
- Create: `cmd/api/migrations/infra/000161_helper_metrics.down.sql`
- Modify: `cmd/api/queries/infra/helper.sql`
- Regenerate: `cmd/api/dal/helper.sql.go`, `cmd/api/dal/models.go`
- Test: `cmd/api/helper_metrics_store_db_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (sqlc-generated, package `dal`):
  - `HelperInvite` gains `InviterCountry sql.NullString`, `VolunteerCountry sql.NullString`, `ClaimedAt sql.NullTime`. `HelperPairing` gains `InviterCountry sql.NullString`, `VolunteerCountry sql.NullString`.
  - `InsertHelperInviteParams` gains `InviterCountry string`. `ClaimHelperInviteParams` gains `VolunteerCountry string`. `InsertHelperPairingParams` gains `InviterCountry string` and `VolunteerCountry string`. An empty string stores NULL.
  - `func (q *Queries) GetHelperInviteSummary(ctx context.Context, inviteHash []byte) (GetHelperInviteSummaryRow, error)`, with `GetHelperInviteSummaryRow{State string; InviterCountry string; ClaimSeconds float64}`.
  - `func (q *Queries) EndHelperPairingForDevice(ctx context.Context, arg EndHelperPairingForDeviceParams) (EndHelperPairingForDeviceRow, error)`, with params `{PairingID uuid.UUID; DeviceID string}` and row `{InviterDeviceID string; InviterCountry string; VolunteerCountry string}`.
  - `func (q *Queries) ListHelperPairingCountriesForVolunteer(ctx context.Context, arg ListHelperPairingCountriesForVolunteerParams) ([]ListHelperPairingCountriesForVolunteerRow, error)`, with params `{VolunteerDeviceID string; PairingIds []uuid.UUID}` and row `{PairingID uuid.UUID; InviterCountry string}`.
  - `func (q *Queries) ListHelperPairingsForTickets(ctx context.Context, deviceID string) ([]ListHelperPairingsForTicketsRow, error)`, with row `{PairingID uuid.UUID; VolunteerDeviceID string; Direct bool}`.
  - `func (q *Queries) CountHelperPairingsByCountry(ctx context.Context) ([]CountHelperPairingsByCountryRow, error)`, with row `{InviterCountry string; VolunteerCountry string; Pairings int64}`.
  - `func (q *Queries) CountHelperVolunteersOnline(ctx context.Context) (int64, error)`.
  - `func (q *Queries) DeleteExpiredHelperInvites(ctx context.Context) ([]DeleteExpiredHelperInvitesRow, error)`, with row `{State string; InviterCountry string}`. It used to return `int64`.
  - Country strings read through `COALESCE(...)::text` are `""` for NULL.

- [ ] **Step 1: Write the migration**

`cmd/api/migrations/infra/000161_helper_metrics.up.sql`:

```sql
-- Countries for personal-proxy metrics: the two-letter country each request
-- was seen from, as /v1/config-new resolves it, or NULL when it was unknown
-- or the row predates these columns. An invite records its inviter's
-- country when created and its volunteer's when claimed; approval copies
-- both to the pairing, and approval or rejection clears the volunteer's
-- from the invite with the rest of the claim. claimed_at times the inviter's
-- decision.
ALTER TABLE helper_invites
    ADD COLUMN inviter_country   VARCHAR(2) CHECK (inviter_country ~ '^[A-Z]{2}$'),
    ADD COLUMN volunteer_country VARCHAR(2) CHECK (volunteer_country ~ '^[A-Z]{2}$'),
    ADD COLUMN claimed_at        TIMESTAMPTZ;

ALTER TABLE helper_pairings
    ADD COLUMN inviter_country   VARCHAR(2) CHECK (inviter_country ~ '^[A-Z]{2}$'),
    ADD COLUMN volunteer_country VARCHAR(2) CHECK (volunteer_country ~ '^[A-Z]{2}$');
```

`cmd/api/migrations/infra/000161_helper_metrics.down.sql`:

```sql
ALTER TABLE helper_pairings
    DROP COLUMN IF EXISTS volunteer_country,
    DROP COLUMN IF EXISTS inviter_country;

ALTER TABLE helper_invites
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS volunteer_country,
    DROP COLUMN IF EXISTS inviter_country;
```

First confirm that 000161 is still the next free number: `git ls-tree --name-only origin/main cmd/api/migrations/infra | sort | tail -2` should end at `000160_*`. If a newer migration has landed, renumber.

- [ ] **Step 2: Change the queries in `cmd/api/queries/infra/helper.sql`**

Replace `InsertHelperInvite`:

```sql
-- name: InsertHelperInvite :exec
INSERT INTO helper_invites (invite_hash, inviter_device_id, approval_pubkey, expires_at, inviter_country)
VALUES (@invite_hash, @inviter_device_id, @approval_pubkey, @expires_at, NULLIF(@inviter_country::text, ''));
```

Replace the `ClaimHelperInvite` statement. Keep its comment lines above:

```sql
-- name: ClaimHelperInvite :execrows
UPDATE helper_invites
SET state = 'claimed', volunteer_device_id = @volunteer_device_id,
    volunteer_pubkey = @volunteer_pubkey, volunteer_blob = @volunteer_blob,
    volunteer_country = NULLIF(@volunteer_country::text, ''), claimed_at = now()
WHERE invite_hash = @invite_hash AND state = 'pending' AND expires_at > now()
  AND inviter_device_id <> @volunteer_device_id;
```

In `ConsumeClaimedHelperInvite`, change the SET line to:

```sql
SET state = 'approved', volunteer_device_id = NULL, volunteer_pubkey = NULL, volunteer_blob = NULL,
    volunteer_country = NULL, pairing_id = @pairing_id
```

In `MarkHelperInviteRejected`, change the SET line to:

```sql
SET state = 'rejected', volunteer_device_id = NULL, volunteer_pubkey = NULL, volunteer_blob = NULL,
    volunteer_country = NULL
```

Replace `InsertHelperPairing`:

```sql
-- name: InsertHelperPairing :exec
INSERT INTO helper_pairings (pairing_id, inviter_device_id, volunteer_device_id, inviter_blob,
                             inviter_country, volunteer_country)
VALUES (@pairing_id, @inviter_device_id, @volunteer_device_id, @inviter_blob,
        NULLIF(@inviter_country::text, ''), NULLIF(@volunteer_country::text, ''));
```

Replace `DeleteExpiredHelperInvites`:

```sql
-- Deletes expired invites and returns the state and inviter country each
-- was in, for helper.invites.expired. Every api replica sweeps; DELETE hands
-- each row to one of them, so none is counted twice.
-- name: DeleteExpiredHelperInvites :many
DELETE FROM helper_invites WHERE expires_at <= now()
RETURNING state, COALESCE(inviter_country, '')::text AS inviter_country;
```

Append these queries at the end of the file:

```sql
-- An unexpired invite's state, inviter country and, once claimed, seconds
-- from creation to claim, whoever owns it. ClaimInvite reads it for metrics
-- only: the claimant's response never depends on it.
-- name: GetHelperInviteSummary :one
SELECT state,
       COALESCE(inviter_country, '')::text AS inviter_country,
       COALESCE(EXTRACT(EPOCH FROM claimed_at - created_at), 0)::float8 AS claim_seconds
FROM helper_invites
WHERE invite_hash = @invite_hash AND expires_at > now();

-- DeleteHelperPairingForDevice that also returns whose side the inviter is
-- and both countries, for helper.pairings.ended.
-- name: EndHelperPairingForDevice :one
DELETE FROM helper_pairings
WHERE pairing_id = @pairing_id AND (inviter_device_id = @device_id OR volunteer_device_id = @device_id)
RETURNING inviter_device_id,
          COALESCE(inviter_country, '')::text AS inviter_country,
          COALESCE(volunteer_country, '')::text AS volunteer_country;

-- The inviter country of each of @pairing_ids whose volunteer is
-- @volunteer_device_id. A usage report counts only these: a pairing the
-- device does not volunteer in is never returned. Uses
-- helper_pairings_volunteer_idx.
-- name: ListHelperPairingCountriesForVolunteer :many
SELECT pairing_id, COALESCE(inviter_country, '')::text AS inviter_country
FROM helper_pairings
WHERE volunteer_device_id = @volunteer_device_id AND pairing_id = ANY(@pairing_ids::uuid[]);

-- A device's pairings, as IssueTickets signs them, with whether each
-- volunteer has a live peer route (direct mode) or not (rooms). The route
-- test is ListHelperVolunteersOnlineForInviter's.
-- name: ListHelperPairingsForTickets :many
SELECT hp.pairing_id,
       hp.volunteer_device_id,
       EXISTS (SELECT 1 FROM peer_routes pr
               WHERE pr.owner_device_id = hp.volunteer_device_id
                 AND pr.deprecated IS NULL
                 AND pr.callback_verified IS NOT NULL
                 AND pr.expiration > now())::boolean AS direct
FROM helper_pairings hp
WHERE hp.inviter_device_id = @device_id OR hp.volunteer_device_id = @device_id
ORDER BY hp.created_at;

-- Live pairings by country pair, for the helper.pairings.active gauge.
-- name: CountHelperPairingsByCountry :many
SELECT COALESCE(inviter_country, '')::text AS inviter_country,
       COALESCE(volunteer_country, '')::text AS volunteer_country,
       count(*) AS pairings
FROM helper_pairings
GROUP BY 1, 2;

-- Paired volunteer devices online by ListHelperVolunteersOnlineForInviter's
-- test: a live peer route, or a ticket fetch in the last 15 minutes. For the
-- helper.volunteers.online gauge.
-- name: CountHelperVolunteersOnline :one
SELECT count(DISTINCT hp.volunteer_device_id)
FROM helper_pairings hp
LEFT JOIN helper_volunteer_presence p ON p.volunteer_device_id = hp.volunteer_device_id
WHERE COALESCE(p.seen_at > now() - interval '15 minutes', false)
   OR EXISTS (SELECT 1 FROM peer_routes pr
              WHERE pr.owner_device_id = hp.volunteer_device_id
                AND pr.deprecated IS NULL
                AND pr.callback_verified IS NOT NULL
                AND pr.expiration > now());
```

- [ ] **Step 3: Write the failing tests**

`cmd/api/helper_metrics_store_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/testdb"
)

func helperMetricsColumns(t *testing.T, tdb *testdb.TestDB) []string {
	t.Helper()
	rows, err := tdb.GetPool().Query(context.Background(), `
		SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE (table_name = 'helper_invites' AND column_name IN ('inviter_country', 'volunteer_country', 'claimed_at'))
		   OR (table_name = 'helper_pairings' AND column_name IN ('inviter_country', 'volunteer_country'))`)
	require.NoError(t, err)
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return cols
}

func TestHelperMetricsMigrationRollsBackAndReapplies(t *testing.T) {
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	ctx := context.Background()
	all := []string{"helper_invites.inviter_country", "helper_invites.volunteer_country", "helper_invites.claimed_at",
		"helper_pairings.inviter_country", "helper_pairings.volunteer_country"}
	require.ElementsMatch(t, all, helperMetricsColumns(t, tdb))

	down, err := os.ReadFile("migrations/infra/000161_helper_metrics.down.sql")
	require.NoError(t, err)
	up, err := os.ReadFile("migrations/infra/000161_helper_metrics.up.sql")
	require.NoError(t, err)
	_, err = tdb.GetPool().Exec(ctx, string(down))
	require.NoError(t, err)
	assert.Empty(t, helperMetricsColumns(t, tdb))
	_, err = tdb.GetPool().Exec(ctx, string(up))
	require.NoError(t, err)
	assert.ElementsMatch(t, all, helperMetricsColumns(t, tdb))

	_, err = tdb.GetPool().Exec(ctx, `INSERT INTO helper_pairings (pairing_id, inviter_device_id, volunteer_device_id, inviter_country)
		VALUES (gen_random_uuid(), 'a', 'b', 'ir')`)
	assert.Error(t, err, "a country is two capital letters")
}

func TestHelperInviteCarriesCountriesAndClaimTime(t *testing.T) {
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	hash := bytes.Repeat([]byte{0x61}, 32)
	require.NoError(t, q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{InviteHash: hash, InviterDeviceID: "inviter",
		ApprovalPubkey: bytes.Repeat([]byte{2}, 32), ExpiresAt: time.Now().Add(time.Hour), InviterCountry: "IR"}))
	sum, err := q.GetHelperInviteSummary(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, dal.GetHelperInviteSummaryRow{State: "pending", InviterCountry: "IR"}, sum)

	volPub := bytes.Repeat([]byte{3}, 32)
	n, err := q.ClaimHelperInvite(ctx, dal.ClaimHelperInviteParams{InviteHash: hash,
		VolunteerDeviceID: sql.NullString{String: "vol", Valid: true}, VolunteerPubkey: volPub,
		VolunteerBlob: []byte("b"), VolunteerCountry: "DE"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	inv, err := q.GetHelperInviteForInviter(ctx, dal.GetHelperInviteForInviterParams{InviteHash: hash, InviterDeviceID: "inviter"})
	require.NoError(t, err)
	assert.Equal(t, sql.NullString{String: "IR", Valid: true}, inv.InviterCountry)
	assert.Equal(t, sql.NullString{String: "DE", Valid: true}, inv.VolunteerCountry)
	assert.True(t, inv.ClaimedAt.Valid)
	sum, err = q.GetHelperInviteSummary(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, "claimed", sum.State)
	assert.GreaterOrEqual(t, sum.ClaimSeconds, 0.0)

	n, err = q.ConsumeClaimedHelperInvite(ctx, dal.ConsumeClaimedHelperInviteParams{
		PairingID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, InviteHash: hash, InviterDeviceID: "inviter", VolunteerPubkey: volPub})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	inv, err = q.GetHelperInviteForInviter(ctx, dal.GetHelperInviteForInviterParams{InviteHash: hash, InviterDeviceID: "inviter"})
	require.NoError(t, err)
	assert.False(t, inv.VolunteerCountry.Valid, "approval drops the volunteer's country with the rest of the claim")
	assert.True(t, inv.InviterCountry.Valid)
	assert.True(t, inv.ClaimedAt.Valid)

	_, err = q.GetHelperInviteSummary(ctx, bytes.Repeat([]byte{0x62}, 32))
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestHelperPairingCountriesAreTheVolunteersOnly(t *testing.T) {
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	mine, legacy, theirs := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: mine,
		InviterDeviceID: "friend", VolunteerDeviceID: "vol", InviterCountry: "IR", VolunteerCountry: "DE"}))
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: legacy,
		InviterDeviceID: "friend", VolunteerDeviceID: "vol"}))
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: theirs,
		InviterDeviceID: "friend", VolunteerDeviceID: "other", InviterCountry: "RU"}))

	rows, err := q.ListHelperPairingCountriesForVolunteer(ctx, dal.ListHelperPairingCountriesForVolunteerParams{
		VolunteerDeviceID: "vol", PairingIds: []uuid.UUID{mine, legacy, theirs, uuid.New()}})
	require.NoError(t, err)
	got := map[uuid.UUID]string{}
	for _, r := range rows {
		got[r.PairingID] = r.InviterCountry
	}
	assert.Equal(t, map[uuid.UUID]string{mine: "IR", legacy: ""}, got)

	end, err := q.EndHelperPairingForDevice(ctx, dal.EndHelperPairingForDeviceParams{PairingID: mine, DeviceID: "vol"})
	require.NoError(t, err)
	assert.Equal(t, dal.EndHelperPairingForDeviceRow{InviterDeviceID: "friend", InviterCountry: "IR", VolunteerCountry: "DE"}, end)
	_, err = q.EndHelperPairingForDevice(ctx, dal.EndHelperPairingForDeviceParams{PairingID: mine, DeviceID: "vol"})
	assert.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestHelperPairingsForTicketsKnowDirectMode(t *testing.T) {
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	trackID := seedPeerTrack(t, tdb)
	direct, rooms := uuid.New(), uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: direct,
		InviterDeviceID: "inv", VolunteerDeviceID: "vol-direct"}))
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: rooms,
		InviterDeviceID: "inv", VolunteerDeviceID: "vol-rooms"}))
	insertPeerRoute(t, tdb, peerRow{device: "vol-direct", track: trackID, verified: true})
	insertPeerRoute(t, tdb, peerRow{device: "vol-rooms", track: trackID})

	rows, err := q.ListHelperPairingsForTickets(ctx, "inv")
	require.NoError(t, err)
	got := map[uuid.UUID]bool{}
	for _, r := range rows {
		got[r.PairingID] = r.Direct
	}
	assert.Equal(t, map[uuid.UUID]bool{direct: true, rooms: false}, got, "an unverified route is not direct mode")
}
```

- [ ] **Step 4: Regenerate the DAL against a scratch database**

Run from the lantern-cloud worktree root. This never touches `localhost:5432`:

```bash
docker run -d --rm --name lc-sqlc-scratch -e POSTGRES_USER=test -e POSTGRES_PASSWORD=1234 -e POSTGRES_DB=api \
  -p 127.0.0.1:55432:5432 postgres:14
until docker exec lc-sqlc-scratch psql -h 127.0.0.1 -U test -d api -c 'select 1' >/dev/null 2>&1; do sleep 1; done
docker exec lc-sqlc-scratch psql -h 127.0.0.1 -U test -d api -c 'CREATE SCHEMA IF NOT EXISTS pro_server'
export ENVIRONMENT=test LC_DOMAIN=test.lantr.net LC_PUBLIC_DOMAIN=test.iantem.io GCP_PROJECT_ID=test
DB_URL="postgres://test:1234@localhost:55432/api?sslmode=disable" \
  bin/migrator migrate up -dir ./cmd/api/migrations/infra
DB_URL="postgres://test:1234@localhost:55432/api?sslmode=disable&options=-c%20search_path=pro_server" \
  bin/migrator migrate up --dir ./cmd/api/migrations/pro-server
sed 's/localhost:5432/localhost:55432/g' cmd/api/sqlc.yaml > cmd/api/sqlc.scratch.yaml
(cd cmd/api && mise exec -- sqlc generate -f sqlc.scratch.yaml)
rm cmd/api/sqlc.scratch.yaml
docker stop lc-sqlc-scratch
```

`mise exec` runs the pinned sqlc binary without running any task or its dependencies. If `mise exec` is unavailable, use `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate -f sqlc.scratch.yaml`, which is the same version.

Verify:

```bash
git status --short cmd/api/dal cmd/api/pro_dal
head -4 cmd/api/dal/helper.sql.go
```

Expected: only `cmd/api/dal/helper.sql.go` and `cmd/api/dal/models.go` are modified, the header still reads `sqlc v1.30.0`, and `cmd/api/sqlc.scratch.yaml` doesn't exist. Check that the generated names match this task's Produces list.

- [ ] **Step 5: Build, then run the tests**

Run: `mise r build -- ./cmd/api/...`
Expected: success. `runHelperInviteSweep`'s `_, err :=` already accepts the new slice return.

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperMetricsMigration|TestHelperInviteCarriesCountries|TestHelperPairingCountriesAreTheVolunteers|TestHelperPairingsForTicketsKnowDirect|TestHelperInvite|TestHelperStore|TestPeerVolunteer'`
Expected: PASS. The existing invite, store and peer tests still pass, because the new parameters default to `""` and that stores NULL.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
git add cmd/api/migrations/infra/000161_helper_metrics.up.sql cmd/api/migrations/infra/000161_helper_metrics.down.sql \
  cmd/api/queries/infra/helper.sql cmd/api/dal/helper.sql.go cmd/api/dal/models.go cmd/api/helper_metrics_store_db_test.go
git commit -m "helper: record invite and pairing countries and claim time for metrics"
```

---

### Task 2: Metric vocabulary, wait histogram, CreateInvite and ClaimInvite

**Files:**
- Modify: `metrics/instruments.go`, `metrics/metrics.go`, `metrics/metrics_test.go`
- Create: `cmd/api/helper_metrics.go`
- Modify: `cmd/api/helper_handler.go` (`CreateInvite`, `ClaimInvite`)
- Test: `cmd/api/helper_metrics_test.go`, `cmd/api/helper_metrics_db_test.go`

**Interfaces:**
- Consumes: Task 1's `InsertHelperInviteParams.InviterCountry`, `ClaimHelperInviteParams.VolunteerCountry`, `GetHelperInviteSummary`.
- Produces:
  - `func metrics.RecordWaitDistribution[T constraints.Float](ctx context.Context, name string, v T, labels ...any)`, with buckets in seconds from 10 s to 48 h.
  - Package `main`, `cmd/api/helper_metrics.go`:
    - Metric-name constants: `metricHelperInvitesCreated`, `metricHelperInvitesClaimed`, `metricHelperInvitesDecided`, `metricHelperInvitesExpired`, `metricHelperClaimSeconds`, `metricHelperDecideSeconds`, `metricHelperPairingsEnded`, `metricHelperPairingsActive`, `metricHelperVolunteersOnline`, `metricHelperTicketsIssued`, `metricHelperRelayedBytes`, `metricHelperRelayedRejected`, `metricPeerRelayedBytes`.
    - Label-value constants: `helperResultOK`, `helperResultError`, `helperResultRateLimited`, `helperResultInviteCap`, `helperResultDailyCap`, `helperResultAlreadyClaimed`, `helperResultNotFound`, `helperResultPairCap`, `helperResultNoPendingClaim`, `helperDecisionApprove`, `helperDecisionReject`, `helperEndedByInviter`, `helperEndedByVolunteer`, `helperModeDirect`, `helperModeRooms`, `helperUnknownCountry`.
    - Country helpers: `func helperStoredCC(cc string) string` and `func helperCC(cc string) string`.
    - Recorders: `recordHelperInviteCreated(ctx, inviterCC, result string)`, `recordHelperInviteClaimed(ctx, inviterCC, volunteerCC, result string)`, `recordHelperInviteDecided(ctx, inviterCC, volunteerCC, decision, result string)`, `recordHelperInviteExpired(ctx, inviterCC, stage string)`, `recordHelperClaimSeconds(ctx, inviterCC string, seconds float64)`, `recordHelperDecideSeconds(ctx, inviterCC string, seconds float64)`, `recordHelperPairingEnded(ctx, inviterCC, volunteerCC, by string)`, `recordHelperTicketIssued(ctx, role, mode string)`. Each takes `ctx context.Context` first.
  - Test helpers in `cmd/api/helper_metrics_db_test.go`, used by Tasks 3 to 6: `helperPostAs(t, mux, ip, path, deviceID, cc string, body any) *httptest.ResponseRecorder`, `helperCounts(t, tester *metrics.MetricsTester, name string, keys ...string) map[string]float64`, `metricsTestHash(t) []byte` and `pairAcross(t, mux, inviter, inviterCC, volunteer, volunteerCC string) string`.

- [ ] **Step 1: Write the failing wait-histogram test**

Append to `metrics/metrics_test.go`:

```go
func TestRecordWaitDistributionUsesWaitBoundaries(t *testing.T) {
	tester := NewMetricsTester()
	defer tester.Close()

	RecordWaitDistribution(context.Background(), "test.wait_bounds_metric", 4000.0, "label", "value")

	bounds := tester.CollectFloat64HistogramBounds(t, "test.wait_bounds_metric")
	require.Equal(t, waitBoundaries, bounds)
	require.Equal(t, 172800.0, bounds[len(bounds)-1], "the top bucket is an invite's 48-hour life")
}
```

Run: `mise r tests:run -- --pkg ./metrics/ --run TestRecordWaitDistribution`
Expected: FAIL to compile, "undefined: RecordWaitDistribution".

- [ ] **Step 2: Implement `RecordWaitDistribution`**

In `metrics/instruments.go`, add `kindFloat64WaitHistogram` to the end of the `instrumentKind` const block:

```go
	kindFloat64UnitHistogram
	kindFloat64WaitHistogram
)
```

In `metrics/metrics.go`, after `RecordUnitDistribution`:

```go
// waitBoundaries are histogram buckets, in seconds, for how long a person
// took to act on something: from seconds to the 48-hour life of a helper
// invite, which is the longest such wait.
var waitBoundaries = []float64{
	10, 30, 60, 300, 900, 1800, 3600, 7200, 14400, 28800, 43200, 86400, 172800,
}

var waitHistogram metric.Float64HistogramOption = metric.WithExplicitBucketBoundaries(waitBoundaries...)

// RecordWaitDistribution records a wait in seconds that runs up to two days,
// past RecordFloatDistribution's top default boundary of 10000.
func RecordWaitDistribution[T constraints.Float](ctx context.Context, name string, v T, labels ...any) {
	instrument(kindFloat64WaitHistogram, name, metric.Meter.Float64Histogram, waitHistogram).Record(ctx, float64(v), attributeOption(labels))
}
```

Run: `mise r tests:run -- --pkg ./metrics/ --run TestRecordWaitDistribution`
Expected: PASS.

- [ ] **Step 3: Write the failing country-helper test**

`cmd/api/helper_metrics_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHelperCountryNormalization(t *testing.T) {
	for in, want := range map[string]string{"IR": "IR", "ir": "IR", " de ": "DE", "": "", "IRN": "", "1R": "", "Z": ""} {
		assert.Equal(t, want, helperStoredCC(in), "stored %q", in)
	}
	assert.Equal(t, "IR", helperCC("ir"))
	assert.Equal(t, helperUnknownCountry, helperCC(""))
	assert.Equal(t, helperUnknownCountry, helperCC("XYZ"))
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run TestHelperCountryNormalization`
Expected: FAIL to compile, "undefined: helperStoredCC".

- [ ] **Step 4: Create `cmd/api/helper_metrics.go`**

```go
package main

import (
	"context"
	"strings"

	"github.com/getlantern/lantern-cloud/metrics"
	"github.com/getlantern/lantern-cloud/trace"
)

// Personal-proxy metrics. Labels carry outcomes, countries and modes only:
// never a pairing ID, invite hash, device ID, name, IP or account. Every
// recorder runs on a context detached from the request's trace.
const (
	metricHelperInvitesCreated   = "helper.invites.created"
	metricHelperInvitesClaimed   = "helper.invites.claimed"
	metricHelperInvitesDecided   = "helper.invites.decided"
	metricHelperInvitesExpired   = "helper.invites.expired"
	metricHelperClaimSeconds     = "helper.invite.claim_seconds"
	metricHelperDecideSeconds    = "helper.invite.decide_seconds"
	metricHelperPairingsEnded    = "helper.pairings.ended"
	metricHelperPairingsActive   = "helper.pairings.active"
	metricHelperVolunteersOnline = "helper.volunteers.online"
	metricHelperTicketsIssued    = "helper.tickets.issued"
	metricHelperRelayedBytes     = "helper.relayed.bytes"
	metricHelperRelayedRejected  = "helper.relayed.rejected"
	metricPeerRelayedBytes       = "peer.relayed.bytes"
)

const (
	helperResultOK             = "ok"
	helperResultError          = "error"
	helperResultRateLimited    = "rate_limited"
	helperResultInviteCap      = "invite_cap"
	helperResultDailyCap       = "daily_cap"
	helperResultAlreadyClaimed = "already_claimed"
	helperResultNotFound       = "not_found"
	helperResultPairCap        = "pair_cap"
	helperResultNoPendingClaim = "no_pending_claim"

	helperDecisionApprove = "approve"
	helperDecisionReject  = "reject"

	helperEndedByInviter   = "inviter"
	helperEndedByVolunteer = "volunteer"

	helperModeDirect = "direct"
	helperModeRooms  = "rooms"

	helperUnknownCountry = "unknown"
)

// helperStoredCC is cc as an invite or pairing row stores it: two capital
// letters, or "" (stored as NULL) for anything else, so a malformed
// X-Lantern-Client-Country override never violates the column's CHECK.
func helperStoredCC(cc string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return cc
}

// helperCC is cc as a metric label: two capital letters, or "unknown".
func helperCC(cc string) string {
	if s := helperStoredCC(cc); s != "" {
		return s
	}
	return helperUnknownCountry
}

func recordHelperInviteCreated(ctx context.Context, inviterCC, result string) {
	metrics.Inc(trace.Detached(ctx), metricHelperInvitesCreated, "inviter_cc", helperCC(inviterCC), "result", result)
}

func recordHelperInviteClaimed(ctx context.Context, inviterCC, volunteerCC, result string) {
	metrics.Inc(trace.Detached(ctx), metricHelperInvitesClaimed,
		"inviter_cc", helperCC(inviterCC), "volunteer_cc", helperCC(volunteerCC), "result", result)
}

func recordHelperInviteDecided(ctx context.Context, inviterCC, volunteerCC, decision, result string) {
	metrics.Inc(trace.Detached(ctx), metricHelperInvitesDecided,
		"inviter_cc", helperCC(inviterCC), "volunteer_cc", helperCC(volunteerCC), "decision", decision, "result", result)
}

func recordHelperInviteExpired(ctx context.Context, inviterCC, stage string) {
	metrics.Inc(trace.Detached(ctx), metricHelperInvitesExpired, "inviter_cc", helperCC(inviterCC), "stage", stage)
}

func recordHelperClaimSeconds(ctx context.Context, inviterCC string, seconds float64) {
	metrics.RecordWaitDistribution(trace.Detached(ctx), metricHelperClaimSeconds, max(seconds, 0), "inviter_cc", helperCC(inviterCC))
}

func recordHelperDecideSeconds(ctx context.Context, inviterCC string, seconds float64) {
	metrics.RecordWaitDistribution(trace.Detached(ctx), metricHelperDecideSeconds, max(seconds, 0), "inviter_cc", helperCC(inviterCC))
}

func recordHelperPairingEnded(ctx context.Context, inviterCC, volunteerCC, by string) {
	metrics.Inc(trace.Detached(ctx), metricHelperPairingsEnded,
		"inviter_cc", helperCC(inviterCC), "volunteer_cc", helperCC(volunteerCC), "by", by)
}

func recordHelperTicketIssued(ctx context.Context, role, mode string) {
	metrics.Inc(trace.Detached(ctx), metricHelperTicketsIssued, "role", role, "mode", mode)
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run TestHelperCountryNormalization`
Expected: PASS.

- [ ] **Step 5: Write the failing handler-outcome tests**

`cmd/api/helper_metrics_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/metrics"
)

// helperPostAs sends body as JSON to path as deviceID from ip with the flag
// on, claiming country cc through the developer override when cc is set.
func helperPostAs(t *testing.T, mux http.Handler, ip, path, deviceID, cc string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("X-Lantern-Feature-Override", featureFlagHelperInvites)
	r.Header.Set("X-Lantern-Device-Id", deviceID)
	if cc != "" {
		r.Header.Set("X-Lantern-Client-Country", cc)
	}
	r.RemoteAddr = ip + ":443"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// helperCounts sums the tester's measurements of name by the values of keys,
// joined with "|".
func helperCounts(t *testing.T, tester *metrics.MetricsTester, name string, keys ...string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, m := range tester.Named(t, name) {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = m.Attributes[k]
		}
		out[strings.Join(parts, "|")] += m.Value
	}
	return out
}

func metricsTestHash(t *testing.T) []byte {
	t.Helper()
	h := make([]byte, 32)
	_, err := rand.Read(h)
	require.NoError(t, err)
	return h
}

// pairAcross takes an invite from inviter in inviterCC through a claim by
// volunteer in volunteerCC to approval, and returns the pairing ID.
func pairAcross(t *testing.T, mux http.Handler, inviter, inviterCC, volunteer, volunteerCC string) string {
	t.Helper()
	s, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", inviter, inviterCC, body).Code)
	vol := bytes.Repeat([]byte{7}, 32)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", volunteer, volunteerCC, claimBody(s, vol)).Code)
	w := helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites/decide", inviter, inviterCC, approveBody(s, vol))
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		PairingID string `json:"pairing_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.PairingID
}

func TestHelperCreateInviteOutcomes(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()

	_, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", "dev-inviter-7f3a", "IR", body).Code)
	assert.Equal(t, http.StatusConflict, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", "dev-inviter-7f3a", "IR", body).Code)

	for range helperMaxPendingInvites {
		require.NoError(t, q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{InviteHash: metricsTestHash(t),
			InviterDeviceID: "dev-capped", ApprovalPubkey: bytes.Repeat([]byte{2}, 32), ExpiresAt: time.Now().Add(time.Hour)}))
	}
	_, body = newTestInvite(t)
	assert.Equal(t, http.StatusTooManyRequests, helperPostAs(t, mux, "203.0.113.3", "/v1/helper/invites", "dev-capped", "IR", body).Code)

	for range helperMaxInvitesPerDay {
		require.NoError(t, q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{InviteHash: metricsTestHash(t),
			InviterDeviceID: "dev-daily", ApprovalPubkey: bytes.Repeat([]byte{2}, 32), ExpiresAt: time.Now().Add(time.Hour)}))
	}
	_, err := tdb.GetPool().Exec(ctx, `UPDATE helper_invites SET state = 'rejected' WHERE inviter_device_id = 'dev-daily'`)
	require.NoError(t, err)
	_, body = newTestInvite(t)
	assert.Equal(t, http.StatusTooManyRequests, helperPostAs(t, mux, "203.0.113.4", "/v1/helper/invites", "dev-daily", "RU", body).Code)

	// Creates and claims share one source's budget of helperIPBurst; the
	// extra ten outrun the bucket's refill while the loop runs.
	for range helperIPBurst + 10 {
		s, _ := newTestInvite(t)
		helperPostAs(t, mux, "198.51.100.77", "/v1/helper/invites/claim", "dev-noise", "", claimBody(s, bytes.Repeat([]byte{5}, 32)))
	}
	_, body = newTestInvite(t)
	assert.Equal(t, http.StatusTooManyRequests, helperPostAs(t, mux, "198.51.100.77", "/v1/helper/invites", "dev-limited", "CN", body).Code)

	assert.Equal(t, map[string]float64{
		"IR|ok": 1, "IR|error": 1, "IR|invite_cap": 1, "RU|daily_cap": 1, "CN|rate_limited": 1,
	}, helperCounts(t, tester, metricHelperInvitesCreated, "inviter_cc", "result"))
	tester.RequireAttributeKeys(t, metricHelperInvitesCreated, "inviter_cc", "result")

	var stored *string
	require.NoError(t, tdb.GetPool().QueryRow(ctx,
		`SELECT inviter_country FROM helper_invites WHERE inviter_device_id = 'dev-inviter-7f3a'`).Scan(&stored))
	require.NotNil(t, stored)
	assert.Equal(t, "IR", *stored)
}

func TestHelperClaimInviteOutcomes(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()

	s, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", "dev-inviter-7f3a", "IR", body).Code)
	vol := bytes.Repeat([]byte{5}, 32)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", "dev-volunteer-9c1e", "DE", claimBody(s, vol)).Code)
	assert.Equal(t, http.StatusConflict, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", "dev-late", "FR", claimBody(s, vol)).Code)
	unknown, _ := newTestInvite(t)
	assert.Equal(t, http.StatusConflict, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", "dev-late", "FR", claimBody(unknown, vol)).Code)

	for range helperMaxVolunteerPairings {
		require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: uuid.New(),
			InviterDeviceID: "someone", VolunteerDeviceID: "dev-full"}))
	}
	s2, body2 := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", "dev-inviter-7f3a", "IR", body2).Code)
	assert.Equal(t, http.StatusTooManyRequests, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", "dev-full", "US", claimBody(s2, vol)).Code)

	assert.Equal(t, map[string]float64{
		"IR|DE|ok": 1, "IR|FR|already_claimed": 1, "unknown|FR|not_found": 1, "IR|US|pair_cap": 1,
	}, helperCounts(t, tester, metricHelperInvitesClaimed, "inviter_cc", "volunteer_cc", "result"))
	claimed := tester.RequireOne(t, metricHelperClaimSeconds)
	assert.Equal(t, "IR", claimed.Attributes["inviter_cc"])
	assert.GreaterOrEqual(t, claimed.Value, 0.0)
	tester.RequireAttributeKeys(t, metricHelperInvitesClaimed, "inviter_cc", "volunteer_cc", "result")
	tester.RequireNoAttributeContains(t, "dev-inviter-7f3a", "dev-volunteer-9c1e", "203.0.113")
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperCreateInviteOutcomes|TestHelperClaimInviteOutcomes'`
Expected: FAIL: `helper.invites.created` and `helper.invites.claimed` are never recorded (`Named` returns nothing), so the map assertions fail.

- [ ] **Step 6: Instrument CreateInvite and ClaimInvite**

In `cmd/api/helper_handler.go`, replace `CreateInvite` and `ClaimInvite` with the code below, and add `liveInviteSummary` after `ClaimInvite`. Add `"github.com/getlantern/lantern-cloud/trace"` to the imports.

```go
func (s helperService) CreateInvite(ctx context.Context, req *services.HelperCreateInviteRequest) (*emptypb.Empty, error) {
	p, err := helperRequest(ctx, "helperCreateInvite")
	if err != nil {
		return nil, err
	}
	result := helperResultError
	defer func() { recordHelperInviteCreated(ctx, p.clientCountry, result) }()
	if !s.allowSource(p) {
		result = helperResultRateLimited
		return nil, errHelperTooManyInvites
	}
	q := dal.New(s.apiDB.GetPool())
	pending, err := q.CountPendingHelperInvites(ctx, p.deviceID)
	if err != nil {
		return nil, helperInternalError(ctx, "helperCreateInvite", err)
	}
	today, err := q.CountHelperInvitesSince(ctx, dal.CountHelperInvitesSinceParams{
		InviterDeviceID: p.deviceID,
		Since:           time.Now().Add(-24 * time.Hour),
	})
	if err != nil {
		return nil, helperInternalError(ctx, "helperCreateInvite", err)
	}
	if pending >= helperMaxPendingInvites {
		result = helperResultInviteCap
		return nil, errHelperTooManyInvites
	}
	if today >= helperMaxInvitesPerDay {
		result = helperResultDailyCap
		return nil, errHelperTooManyInvites
	}
	if err := q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{
		InviteHash:      req.InviteHash,
		InviterDeviceID: p.deviceID,
		ApprovalPubkey:  req.ApprovalPubkey,
		ExpiresAt:       time.Now().Add(helperInviteTTL),
		InviterCountry:  helperStoredCC(p.clientCountry),
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// A duplicate hash is a client bug or a replay; it never overwrites.
			return nil, &router.HTTPError{Status: http.StatusConflict, Msg: "invite exists"}
		}
		return nil, helperInternalError(ctx, "helperCreateInvite", err)
	}
	result = helperResultOK
	return &emptypb.Empty{}, nil
}

func (s helperService) ClaimInvite(ctx context.Context, req *services.HelperClaimInviteRequest) (*emptypb.Empty, error) {
	p, err := helperRequest(ctx, "helperClaimInvite")
	if err != nil {
		return nil, err
	}
	result, inviterCC := helperResultError, ""
	defer func() { recordHelperInviteClaimed(ctx, inviterCC, p.clientCountry, result) }()
	if !s.allowSource(p) {
		result = helperResultRateLimited
		return nil, errHelperTooManyInvites
	}
	q := dal.New(s.apiDB.GetPool())
	n, err := q.CountHelperPairingsForVolunteer(ctx, p.deviceID)
	if err != nil {
		return nil, helperInternalError(ctx, "helperClaimInvite", err)
	}
	if n >= helperMaxVolunteerPairings {
		result = helperResultPairCap
		inv, _ := s.liveInviteSummary(ctx, req.InviteHash)
		inviterCC = inv.InviterCountry
		return nil, &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many pairings"}
	}
	rows, err := q.ClaimHelperInvite(ctx, dal.ClaimHelperInviteParams{
		InviteHash:        req.InviteHash,
		VolunteerDeviceID: sql.NullString{String: p.deviceID, Valid: true},
		VolunteerPubkey:   req.VolunteerPubkey,
		VolunteerBlob:     req.VolunteerBlob,
		VolunteerCountry:  helperStoredCC(p.clientCountry),
	})
	if err != nil {
		return nil, helperInternalError(ctx, "helperClaimInvite", err)
	}
	inv, live := s.liveInviteSummary(ctx, req.InviteHash)
	inviterCC = inv.InviterCountry
	if rows == 0 {
		// Unknown, used, rejected and expired invites are indistinguishable,
		// so a claimant learns nothing about other people's invites. Only the
		// metric tells a live invite from none.
		result = helperResultNotFound
		if live {
			result = helperResultAlreadyClaimed
		}
		return nil, errHelperAlreadyClaimed
	}
	result = helperResultOK
	if live {
		recordHelperClaimSeconds(ctx, inv.InviterCountry, inv.ClaimSeconds)
	}
	return &emptypb.Empty{}, nil
}

// liveInviteSummary reads the unexpired invite with hash for metrics. A
// failed read reports no invite and never fails the request.
func (s helperService) liveInviteSummary(ctx context.Context, hash []byte) (dal.GetHelperInviteSummaryRow, bool) {
	inv, err := dal.New(s.apiDB.GetPool()).GetHelperInviteSummary(ctx, hash)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.WarnErr(trace.Detached(ctx), "helper invite lookup for metrics failed", err, nil)
		}
		return dal.GetHelperInviteSummaryRow{}, false
	}
	return inv, true
}
```

- [ ] **Step 7: Run the tests**

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperCreateInviteOutcomes|TestHelperClaimInviteOutcomes|TestHelperCountryNormalization|TestHelperCreate|TestHelperClaim|TestHelperLimit|TestHelperGate'`
Expected: PASS.

- [ ] **Step 8: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
mise r checks:log-attr-keys
git add metrics/instruments.go metrics/metrics.go metrics/metrics_test.go cmd/api/helper_metrics.go \
  cmd/api/helper_metrics_test.go cmd/api/helper_metrics_db_test.go cmd/api/helper_handler.go
git commit -m "helper: count invite creations and claims by outcome and country"
```

---

### Task 3: DecideInvite and EndPairing

**Files:**
- Modify: `cmd/api/helper_handler.go` (`DecideInvite`, `EndPairing`)
- Test: `cmd/api/helper_metrics_db_test.go` (append)

**Interfaces:**
- Consumes: Task 1's `HelperInvite.InviterCountry/VolunteerCountry/ClaimedAt`, `InsertHelperPairingParams.InviterCountry/VolunteerCountry` and `EndHelperPairingForDevice`. Task 2's recorders, constants, `helperPostAs`, `helperCounts` and `pairAcross`.
- Produces: `func helperDecideSeconds(inv dal.HelperInvite, now time.Time) float64`. Approved pairings now carry both countries, which Tasks 4 to 6 read.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/api/helper_metrics_db_test.go`:

```go
func TestHelperDecideInviteOutcomes(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	ctx := context.Background()

	id := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-volunteer-9c1e", "DE")
	var inviterCC, volunteerCC *string
	require.NoError(t, tdb.GetPool().QueryRow(ctx,
		`SELECT inviter_country, volunteer_country FROM helper_pairings WHERE pairing_id = $1`, id).Scan(&inviterCC, &volunteerCC))
	require.NotNil(t, inviterCC)
	require.NotNil(t, volunteerCC)
	assert.Equal(t, "IR", *inviterCC)
	assert.Equal(t, "DE", *volunteerCC)

	s, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites", "dev-inviter-7f3a", "IR", body).Code)
	vol := bytes.Repeat([]byte{5}, 32)
	reject := map[string]any{"invite_hash": body["invite_hash"], "approve": false}
	assert.Equal(t, http.StatusConflict, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites/decide", "dev-inviter-7f3a", "IR", reject).Code,
		"an unclaimed invite has nothing to decide")
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/invites/claim", "dev-v2", "FR", claimBody(s, vol)).Code)
	bad := approveBody(s, vol)
	bad["approval_sig"] = bytes.Repeat([]byte{1}, 64)
	assert.Equal(t, http.StatusForbidden, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites/decide", "dev-inviter-7f3a", "IR", bad).Code)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites/decide", "dev-inviter-7f3a", "IR", reject).Code)
	unknown, _ := newTestInvite(t)
	assert.Equal(t, http.StatusNotFound, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/invites/decide", "dev-inviter-7f3a", "IR",
		approveBody(unknown, vol)).Code)

	assert.Equal(t, map[string]float64{
		"IR|DE|approve|ok": 1, "IR|unknown|reject|no_pending_claim": 1, "IR|FR|approve|error": 1,
		"IR|FR|reject|ok": 1, "IR|unknown|approve|no_pending_claim": 1,
	}, helperCounts(t, tester, metricHelperInvitesDecided, "inviter_cc", "volunteer_cc", "decision", "result"))
	// Both successful decisions share one series; its value is their summed wait.
	decide := tester.RequireOne(t, metricHelperDecideSeconds)
	assert.Equal(t, "IR", decide.Attributes["inviter_cc"])
	assert.GreaterOrEqual(t, decide.Value, 0.0)
}
```

Also append:

```go
func TestHelperEndPairingCountsWhoEndedIt(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)

	a := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-volunteer-9c1e", "DE")
	b := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-v2", "FR")
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/pairings/end", "dev-volunteer-9c1e", "DE",
		map[string]any{"pairing_id": a}).Code)
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/pairings/end", "dev-inviter-7f3a", "IR",
		map[string]any{"pairing_id": b}).Code)
	assert.Equal(t, http.StatusNotFound, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/pairings/end", "dev-inviter-7f3a", "IR",
		map[string]any{"pairing_id": b}).Code)

	assert.Equal(t, map[string]float64{"IR|DE|volunteer": 1, "IR|FR|inviter": 1},
		helperCounts(t, tester, metricHelperPairingsEnded, "inviter_cc", "volunteer_cc", "by"))
	tester.RequireNoAttributeContains(t, a, b, "dev-inviter-7f3a", "dev-volunteer-9c1e")
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperDecideInviteOutcomes|TestHelperEndPairingCountsWhoEndedIt'`
Expected: FAIL: the decided and ended counters are empty, and the pairing's countries are NULL.

- [ ] **Step 2: Instrument DecideInvite**

Replace `DecideInvite` in `cmd/api/helper_handler.go`, and add `helperDecideSeconds` after it:

```go
func (s helperService) DecideInvite(ctx context.Context, req *services.HelperDecideInviteRequest) (*services.HelperDecideInviteResponse, error) {
	p, err := helperRequest(ctx, "helperDecideInvite")
	if err != nil {
		return nil, err
	}
	decision := helperDecisionReject
	if req.Approve {
		decision = helperDecisionApprove
	}
	result, inviterCC, volunteerCC := helperResultError, p.clientCountry, ""
	defer func() { recordHelperInviteDecided(ctx, inviterCC, volunteerCC, decision, result) }()
	inv, err := s.inviterInvite(ctx, "helperDecideInvite", p.deviceID, req.InviteHash)
	if errors.Is(err, errHelperNoInvite) {
		result = helperResultNoPendingClaim
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	inviterCC, volunteerCC = inv.InviterCountry.String, inv.VolunteerCountry.String
	// Approved, rejected and pending invites all have nothing to decide.
	if inv.State != "claimed" {
		result = helperResultNoPendingClaim
		return nil, errHelperNoPendingClaim
	}
	q := dal.New(s.apiDB.GetPool())
	if !req.Approve {
		n, err := q.MarkHelperInviteRejected(ctx, dal.MarkHelperInviteRejectedParams{
			InviteHash: inv.InviteHash, InviterDeviceID: p.deviceID, VolunteerPubkey: inv.VolunteerPubkey})
		if err != nil {
			return nil, helperInternalError(ctx, "helperDecideInvite", err)
		}
		if n == 0 {
			result = helperResultNoPendingClaim
			return nil, errHelperNoPendingClaim
		}
		result = helperResultOK
		recordHelperDecideSeconds(ctx, inviterCC, helperDecideSeconds(inv, time.Now()))
		return &services.HelperDecideInviteResponse{}, nil
	}
	if !pairing.VerifyApproval(ed25519.PublicKey(inv.ApprovalPubkey), inv.InviteHash, inv.VolunteerPubkey, req.ApprovalSig) {
		return nil, router.Forbidden("approval signature does not match")
	}
	inviterN, err := q.CountHelperPairingsForInviter(ctx, p.deviceID)
	if err != nil {
		return nil, helperInternalError(ctx, "helperDecideInvite", err)
	}
	// The volunteer may have filled up since the claim, so the cap is
	// checked again here.
	volunteerN, err := q.CountHelperPairingsForVolunteer(ctx, inv.VolunteerDeviceID.String)
	if err != nil {
		return nil, helperInternalError(ctx, "helperDecideInvite", err)
	}
	if inviterN >= helperMaxInviterPairings || volunteerN >= helperMaxVolunteerPairings {
		return nil, &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many pairings"}
	}
	id := uuid.New()
	// The invite is consumed first and only if it still holds the claim the
	// signature was verified against, so of two racing approvals one inserts
	// a pairing and the other rolls back, and a stale decision can't consume
	// a later claim on the same hash. The consume and the pairing commit
	// together.
	_, err = db.WithPGTx(ctx, s.apiDB, func(tx pgx.Tx) (struct{}, error) {
		txq := dal.New(s.apiDB.GetPool()).WithTx(tx)
		n, err := txq.ConsumeClaimedHelperInvite(ctx, dal.ConsumeClaimedHelperInviteParams{
			PairingID:  uuid.NullUUID{UUID: id, Valid: true},
			InviteHash: inv.InviteHash, InviterDeviceID: p.deviceID, VolunteerPubkey: inv.VolunteerPubkey})
		if err != nil {
			return struct{}{}, fmt.Errorf("consume helper invite: %w", err)
		}
		if n == 0 {
			return struct{}{}, errHelperNoPendingClaim
		}
		return struct{}{}, txq.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
			PairingID: id, InviterDeviceID: p.deviceID,
			VolunteerDeviceID: inv.VolunteerDeviceID.String, InviterBlob: req.InviterBlob,
			InviterCountry: inv.InviterCountry.String, VolunteerCountry: inv.VolunteerCountry.String,
		})
	})
	if errors.Is(err, errHelperNoPendingClaim) {
		result = helperResultNoPendingClaim
		return nil, errHelperNoPendingClaim
	}
	if err != nil {
		return nil, helperInternalError(ctx, "helperDecideInvite", err)
	}
	result = helperResultOK
	recordHelperDecideSeconds(ctx, inviterCC, helperDecideSeconds(inv, time.Now()))
	return &services.HelperDecideInviteResponse{PairingId: id.String()}, nil
}

// helperDecideSeconds is how long inv's claim waited for the inviter, timed
// from its creation for a claim made before claimed_at was recorded.
func helperDecideSeconds(inv dal.HelperInvite, now time.Time) float64 {
	from := inv.CreatedAt
	if inv.ClaimedAt.Valid {
		from = inv.ClaimedAt.Time
	}
	return now.Sub(from).Seconds()
}
```

- [ ] **Step 3: Instrument EndPairing**

In `EndPairing`, replace everything from `n, err := dal.New(s.apiDB.GetPool()).DeleteHelperPairingForDevice(` to the end of the function with:

```go
	row, err := dal.New(s.apiDB.GetPool()).EndHelperPairingForDevice(ctx, dal.EndHelperPairingForDeviceParams{
		PairingID: id, DeviceID: p.deviceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, router.NotFound("no such pairing")
	}
	if err != nil {
		return nil, helperInternalError(ctx, "helperEndPairing", err)
	}
	by := helperEndedByVolunteer
	if row.InviterDeviceID == p.deviceID {
		by = helperEndedByInviter
	}
	recordHelperPairingEnded(ctx, row.InviterCountry, row.VolunteerCountry, by)
	return &emptypb.Empty{}, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperDecideInviteOutcomes|TestHelperEndPairingCountsWhoEndedIt|TestHelperApprove|TestHelperReject|TestHelperDecide|TestHelperEndPairing|TestHelperGetInvite'`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
git add cmd/api/helper_handler.go cmd/api/helper_metrics_db_test.go
git commit -m "helper: count invite decisions and ended pairings, and copy countries to pairings"
```

---

### Task 4: Expired invites and gauges

**Files:**
- Create: `cmd/api/helper_gauges.go`
- Modify: `cmd/api/helper_handler.go` (`runHelperInviteSweep`), `cmd/api/main.go:566-567`
- Test: `cmd/api/helper_gauges_db_test.go`

**Interfaces:**
- Consumes: Task 1's `DeleteExpiredHelperInvites`, `CountHelperPairingsByCountry` and `CountHelperVolunteersOnline`. Task 2's constants, recorders and `helperCounts`.
- Produces: `func sweepHelperInvites(ctx context.Context, q *dal.Queries) error`, `type helperGauges`, `func newHelperGauges(now func() time.Time) *helperGauges`, `func (g *helperGauges) register(ctx context.Context)`, `func (g *helperGauges) sample(ctx context.Context, q *dal.Queries) error`, `func runHelperGauges(ctx context.Context, q *dal.Queries, every time.Duration)` and `const helperGaugeInterval = 5 * time.Minute`.

- [ ] **Step 1: Write the failing tests**

`cmd/api/helper_gauges_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/metrics"
	"github.com/getlantern/lantern-cloud/testdb"
)

func insertInviteExpiringIn(t *testing.T, tdb *testdb.TestDB, state, cc string, in time.Duration) {
	t.Helper()
	var volDev, volPub any
	if state == "claimed" {
		volDev, volPub = "vol", bytes.Repeat([]byte{3}, 32)
	}
	_, err := tdb.GetPool().Exec(context.Background(), `
		INSERT INTO helper_invites (invite_hash, inviter_device_id, approval_pubkey, state, expires_at,
		                            inviter_country, volunteer_device_id, volunteer_pubkey)
		VALUES ($1, 'inviter', $2, $3, now() + make_interval(secs => $4), NULLIF($5, ''), $6, $7)`,
		metricsTestHash(t), bytes.Repeat([]byte{2}, 32), state, in.Seconds(), cc, volDev, volPub)
	require.NoError(t, err)
}

func TestHelperInviteSweepCountsExpiredByStage(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	insertInviteExpiringIn(t, tdb, "pending", "IR", -time.Minute)
	insertInviteExpiringIn(t, tdb, "pending", "", -time.Minute)
	insertInviteExpiringIn(t, tdb, "claimed", "IR", -time.Minute)
	insertInviteExpiringIn(t, tdb, "approved", "IR", -time.Minute)
	insertInviteExpiringIn(t, tdb, "rejected", "IR", -time.Minute)
	insertInviteExpiringIn(t, tdb, "pending", "DE", time.Hour)

	require.NoError(t, sweepHelperInvites(ctx, q))
	assert.Equal(t, map[string]float64{"IR|pending": 1, "unknown|pending": 1, "IR|claimed": 1},
		helperCounts(t, tester, metricHelperInvitesExpired, "inviter_cc", "stage"), "decided tombstones are not counted")
	var left int
	require.NoError(t, tdb.GetPool().QueryRow(ctx, `SELECT count(*) FROM helper_invites`).Scan(&left))
	assert.Equal(t, 1, left)
}

func TestHelperGaugesSampleTheDatabase(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	for _, p := range []dal.InsertHelperPairingParams{
		{InviterDeviceID: "i1", VolunteerDeviceID: "v1", InviterCountry: "IR", VolunteerCountry: "DE"},
		{InviterDeviceID: "i2", VolunteerDeviceID: "v1", InviterCountry: "IR", VolunteerCountry: "DE"},
		{InviterDeviceID: "i3", VolunteerDeviceID: "v2", InviterCountry: "RU"},
		{InviterDeviceID: "i4", VolunteerDeviceID: "v3"},
	} {
		p.PairingID = uuid.New()
		require.NoError(t, q.InsertHelperPairing(ctx, p))
	}
	require.NoError(t, q.TouchHelperVolunteerPresence(ctx, "v1"))
	require.NoError(t, q.TouchHelperVolunteerPresence(ctx, "v2"))
	_, err := tdb.GetPool().Exec(ctx, `UPDATE helper_volunteer_presence SET seen_at = now() - interval '1 hour' WHERE volunteer_device_id = 'v2'`)
	require.NoError(t, err)

	now := time.Now()
	g := newHelperGauges(func() time.Time { return now })
	g.register(ctx)
	assert.Empty(t, tester.Named(t, metricHelperPairingsActive), "nothing is exported before the first sample")
	require.NoError(t, g.sample(ctx, q))

	assert.Equal(t, map[string]float64{"IR|DE": 2, "RU|unknown": 1, "unknown|unknown": 1},
		helperCounts(t, tester, metricHelperPairingsActive, "inviter_cc", "volunteer_cc"))
	assert.Equal(t, 1.0, tester.RequireOne(t, metricHelperVolunteersOnline).Value, "v1 only: v2 was seen an hour ago")

	now = now.Add(helperGaugeStale + time.Second)
	assert.Empty(t, tester.Named(t, metricHelperPairingsActive), "a stale sample is not exported")
	assert.Empty(t, tester.Named(t, metricHelperVolunteersOnline))
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperInviteSweepCountsExpiredByStage|TestHelperGaugesSampleTheDatabase'`
Expected: FAIL to compile, "undefined: sweepHelperInvites".

- [ ] **Step 2: Create `cmd/api/helper_gauges.go`**

```go
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/log"
	"github.com/getlantern/lantern-cloud/metrics"
)

// sweepHelperInvites deletes expired invites and counts those that expired
// unclaimed or undecided. Approved and rejected invites expire too, but they
// were decided, so they are not counted.
func sweepHelperInvites(ctx context.Context, q *dal.Queries) error {
	rows, err := q.DeleteExpiredHelperInvites(ctx)
	if err != nil {
		return fmt.Errorf("delete expired helper invites: %w", err)
	}
	for _, r := range rows {
		switch r.State {
		case "pending", "claimed":
			recordHelperInviteExpired(ctx, r.InviterCountry, r.State)
		}
	}
	return nil
}

const (
	// helperGaugeInterval is how often each api replica samples the
	// pairing gauges. Every replica samples, so dashboards take the max
	// across instances, never the sum.
	helperGaugeInterval = 5 * time.Minute
	// helperGaugeStale is how old a sample may be and still be exported. A
	// replica that cannot reach the database stops reporting rather than
	// repeat old counts.
	helperGaugeStale = 3 * helperGaugeInterval
)

// helperGauges holds the last sample of helper.pairings.active and
// helper.volunteers.online for their observable gauges.
type helperGauges struct {
	now    func() time.Time
	mu     sync.Mutex
	at     time.Time
	active []dal.CountHelperPairingsByCountryRow
	online int64
}

func newHelperGauges(now func() time.Time) *helperGauges {
	return &helperGauges{now: now}
}

// register exports g's last sample on the current meter provider.
func (g *helperGauges) register(ctx context.Context) {
	metrics.InitObservableGauge(ctx, metricHelperPairingsActive, func(_ context.Context, o metric.Int64Observer) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.freshLocked() {
			return nil
		}
		for _, r := range g.active {
			o.Observe(r.Pairings, metric.WithAttributes(
				attribute.String("inviter_cc", helperCC(r.InviterCountry)),
				attribute.String("volunteer_cc", helperCC(r.VolunteerCountry))))
		}
		return nil
	})
	metrics.InitObservableGauge(ctx, metricHelperVolunteersOnline, func(_ context.Context, o metric.Int64Observer) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.freshLocked() {
			o.Observe(g.online)
		}
		return nil
	})
}

func (g *helperGauges) freshLocked() bool {
	return !g.at.IsZero() && g.now().Sub(g.at) <= helperGaugeStale
}

func (g *helperGauges) sample(ctx context.Context, q *dal.Queries) error {
	active, err := q.CountHelperPairingsByCountry(ctx)
	if err != nil {
		return fmt.Errorf("count helper pairings by country: %w", err)
	}
	online, err := q.CountHelperVolunteersOnline(ctx)
	if err != nil {
		return fmt.Errorf("count online helper volunteers: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active, g.online, g.at = active, online, g.now()
	return nil
}

// runHelperGauges registers the pairing gauges and samples them every
// interval until ctx ends.
func runHelperGauges(ctx context.Context, q *dal.Queries, every time.Duration) {
	g := newHelperGauges(time.Now)
	g.register(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := g.sample(ctx, q); err != nil {
			log.Error(ctx, "helper gauge sample failed", err, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 3: Use the counting sweep and start the sampler**

In `runHelperInviteSweep` (`cmd/api/helper_handler.go`), replace:

```go
				if _, err := q.DeleteExpiredHelperInvites(ctx); err != nil {
```

with:

```go
				if err := sweepHelperInvites(ctx, q); err != nil {
```

In `cmd/api/main.go`, after `go runHelperInviteSweep(ctx, dal.New(ls.apiDB.GetPool()), time.Hour)`, add:

```go
		// Personal-proxy gauges, sampled on every replica.
		go runHelperGauges(ctx, dal.New(ls.apiDB.GetPool()), helperGaugeInterval)
```

- [ ] **Step 4: Run the tests**

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperInviteSweepCountsExpiredByStage|TestHelperGaugesSampleTheDatabase|TestHelperPresence|TestHelperSweep'`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
mise r checks:log-attr-keys
git add cmd/api/helper_gauges.go cmd/api/helper_gauges_db_test.go cmd/api/helper_handler.go cmd/api/main.go
git commit -m "helper: count expired invites by stage and sample pairing gauges"
```

---

### Task 5: Usage protos, IssueTickets traffic and tickets issued

**Files:**
- Modify: `cmd/api/apipb/proto/services/helper.proto`, `cmd/api/apipb/proto/services/peer.proto`
- Regenerate: `cmd/api/apipb/services/{helper,peer}.pb.go`, `helper_grpc.pb.go`, `openapiv2/services/*.swagger.json`, `cmd/backoffice/src/gen/services/*`
- Create: `cmd/api/helper_traffic.go`
- Modify: `cmd/api/helper_handler.go` (`IssueTickets`)
- Test: `cmd/api/helper_traffic_db_test.go`

**Interfaces:**
- Consumes: Task 1's `ListHelperPairingCountriesForVolunteer` and `ListHelperPairingsForTickets`. Task 2's recorders, constants, `helperPostAs`, `helperCounts` and `pairAcross`. Task 3's countries on pairings.
- Produces:
  - Proto messages `HelperPairedBytes{string pairing_id = 1; uint64 bytes = 2}` and `HelperIssueTicketsRequest{repeated HelperPairedBytes paired_bytes = 1}`. `PeerLifecycleRequest` gains `uint64 relayed_bytes = 3` and `repeated HelperPairedBytes paired_bytes = 4`. Go getters: `GetPairingId() string`, `GetBytes() uint64`, `GetPairedBytes() []*services.HelperPairedBytes` and `GetRelayedBytes() uint64`.
  - The HTTP JSON bodies radiance sends: `POST /v1/helper/tickets` takes `{"paired_bytes":[{"pairing_id":"<uuid>","bytes":N}]}`, and `{}` is still valid. `POST /v1/peer/heartbeat` adds `"relayed_bytes":N` and `"paired_bytes":[...]` beside `route_id` and `active_clients`.
  - `const helperMaxReportedBytes uint64 = 50_000_000_000` and `const helperMaxReportedPairings = 64`.
  - `func recordHelperPairedBytes(ctx context.Context, q *dal.Queries, deviceID, volunteerCC, mode string, reported []*services.HelperPairedBytes) uint64`, which returns the bytes it counted.

- [ ] **Step 1: Change the protos**

In `helper.proto`, change the `IssueTickets` rpc to:

```proto
  // IssueTickets also takes the bytes a volunteer running Unbounded rooms
  // relayed for each of its pairings since its last refresh that succeeded.
  rpc IssueTickets(HelperIssueTicketsRequest) returns (HelperTickets) {
    option (google.api.http) = { post: "/v1/helper/tickets" body: "*" };
  }
```

Then add, after `HelperPairingRef`:

```proto
// HelperPairedBytes is the bytes a volunteer relayed for one of its
// pairings since its last report that succeeded. The server only counts
// them: it never stores, logs or traces a report. An entry with a malformed
// pairing_id, a pairing the caller does not volunteer in, or more than 50 GB
// is dropped, never refused, so a report cannot fail the call carrying it.
message HelperPairedBytes {
  string pairing_id = 1;
  uint64 bytes = 2;
}

message HelperIssueTicketsRequest {
  // Rooms-mode usage. Inviters, and clients that predate it, send none.
  repeated HelperPairedBytes paired_bytes = 1;
}
```

In `peer.proto`, add `import "services/helper.proto";` after `import "rules.proto";`, and extend `PeerLifecycleRequest`:

```proto
message PeerLifecycleRequest {
  string route_id = 1 [(buf.validate.field).string.(uuid_or) = "invalid route_id"];
  // The number of distinct client devices the peer is serving, sent on
  // heartbeat only. Self-reported, so it bounds load only for honest peers.
  int32 active_clients = 2;
  // Heartbeat only: every byte relayed, pool and paired, since the last
  // heartbeat that succeeded. Counted, never stored; over 50 GB it is
  // dropped.
  uint64 relayed_bytes = 3;
  // Heartbeat only: the part of relayed_bytes that came through each helper
  // pairing this device volunteers in. See HelperPairedBytes.
  repeated HelperPairedBytes paired_bytes = 4;
}
```

Run: `mise r codegen:proto:api`
Expected: `cmd/api/apipb/services/helper.pb.go`, `helper_grpc.pb.go`, `peer.pb.go`, the swagger JSON and the backoffice TS change. `mise r build -- ./cmd/api/...` now fails with `helperService does not implement ... IssueTickets` (wrong argument type). Step 4 fixes that.

- [ ] **Step 2: Write the failing tests**

`cmd/api/helper_traffic_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/metrics"
)

func TestHelperIssueTicketsCountsRoomsTraffic(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	mine := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-volunteer-9c1e", "DE")
	theirs := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-other", "FR")

	report := map[string]any{"paired_bytes": []map[string]any{
		{"pairing_id": mine, "bytes": 4096},
		{"pairing_id": mine, "bytes": 1024},
		{"pairing_id": theirs, "bytes": 777},
		{"pairing_id": "not-a-uuid", "bytes": 1},
		{"pairing_id": uuid.NewString(), "bytes": 9},
		{"pairing_id": mine, "bytes": 0},
	}}
	w := helperPostAs(t, mux, "203.0.113.2", "/v1/helper/tickets", "dev-volunteer-9c1e", "NL", report)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	assert.Equal(t, map[string]float64{"rooms|IR|NL": 5120},
		helperCounts(t, tester, metricHelperRelayedBytes, "mode", "inviter_cc", "volunteer_cc"),
		"the reporting request's country is the volunteer's; a pairing it does not volunteer in is dropped")
	assert.Equal(t, map[string]float64{"volunteer|rooms": 1},
		helperCounts(t, tester, metricHelperTicketsIssued, "role", "mode"))
	tester.RequireNoAttributeContains(t, mine, theirs, "dev-volunteer-9c1e", "203.0.113")
}

func TestHelperIssueTicketsDropsOversizedValues(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	mine := pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-volunteer-9c1e", "DE")

	report := map[string]any{"paired_bytes": []map[string]any{{"pairing_id": mine, "bytes": helperMaxReportedBytes + 1}}}
	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.2", "/v1/helper/tickets", "dev-volunteer-9c1e", "DE", report).Code)
	assert.Empty(t, tester.Named(t, metricHelperRelayedBytes))
	assert.Equal(t, map[string]float64{"rooms": 1}, helperCounts(t, tester, metricHelperRelayedRejected, "mode"))
}

func TestHelperTicketsIssuedKnowDirectMode(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-direct", "DE")
	pairAcross(t, mux, "dev-inviter-7f3a", "IR", "dev-rooms", "DE")
	insertPeerRoute(t, tdb, peerRow{device: "dev-direct", track: seedPeerTrack(t, tdb), verified: true})

	require.Equal(t, http.StatusOK, helperPostAs(t, mux, "203.0.113.1", "/v1/helper/tickets", "dev-inviter-7f3a", "IR", map[string]any{}).Code)
	assert.Equal(t, map[string]float64{"inviter|direct": 1, "inviter|rooms": 1},
		helperCounts(t, tester, metricHelperTicketsIssued, "role", "mode"))
}
```

- [ ] **Step 3: Create `cmd/api/helper_traffic.go`**

```go
package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb/services"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/log"
	"github.com/getlantern/lantern-cloud/metrics"
	"github.com/getlantern/lantern-cloud/trace"
)

const (
	// helperMaxReportedBytes is the most one value of a usage report may
	// carry, per 150-second heartbeat or per ticket refresh. A larger value
	// is dropped and counted as helper.relayed.rejected: the counts are
	// self-reported, so the cap bounds what one device can skew.
	helperMaxReportedBytes uint64 = 50_000_000_000
	// helperMaxReportedPairings bounds the entries read from one report. A
	// device volunteers in at most helperMaxVolunteerPairings pairings.
	helperMaxReportedPairings = 64
)

// recordHelperPairedBytes counts the bytes deviceID reports relaying for its
// pairings, in mode, and returns the total it counted. Entries for a pairing
// deviceID does not volunteer in, malformed or zero entries, and values over
// helperMaxReportedBytes are dropped. The pairing IDs are used only to look up
// each pairing's inviter country; nothing about the report is logged or
// traced.
func recordHelperPairedBytes(ctx context.Context, q *dal.Queries, deviceID, volunteerCC, mode string, reported []*services.HelperPairedBytes) uint64 {
	ctx = trace.Detached(ctx)
	byID := make(map[uuid.UUID]uint64)
	for i, e := range reported {
		if i >= helperMaxReportedPairings {
			break
		}
		id, err := uuid.Parse(e.GetPairingId())
		if err != nil || e.GetBytes() == 0 {
			continue
		}
		// Clamped so that summing duplicate entries cannot overflow.
		byID[id] += min(e.GetBytes(), helperMaxReportedBytes+1)
	}
	ids := make([]uuid.UUID, 0, len(byID))
	for id, n := range byID {
		if n > helperMaxReportedBytes {
			metrics.Inc(ctx, metricHelperRelayedRejected, "mode", mode)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return 0
	}
	rows, err := q.ListHelperPairingCountriesForVolunteer(ctx, dal.ListHelperPairingCountriesForVolunteerParams{
		VolunteerDeviceID: deviceID, PairingIds: ids})
	if err != nil {
		log.Error(ctx, "helper usage: pairing lookup failed", err, nil)
		return 0
	}
	vcc := helperCC(volunteerCC)
	var counted uint64
	for _, r := range rows {
		n := byID[r.PairingID]
		metrics.Add(ctx, metricHelperRelayedBytes, n, "mode", mode, "inviter_cc", helperCC(r.InviterCountry), "volunteer_cc", vcc)
		counted += n
	}
	return counted
}
```

- [ ] **Step 4: Update IssueTickets**

Replace `IssueTickets` in `cmd/api/helper_handler.go`:

```go
func (s helperService) IssueTickets(ctx context.Context, req *services.HelperIssueTicketsRequest) (*services.HelperTickets, error) {
	p, err := helperRequest(ctx, "helperIssueTickets")
	if err != nil {
		return nil, err
	}
	if err := s.volunteerOrNotFound(p, "helperIssueTickets"); err != nil {
		return nil, err
	}
	if !s.allowTickets(p.deviceID, time.Now()) {
		return nil, errHelperTooManyTicketRequests
	}
	if s.helperTicketKey == nil {
		return nil, &router.HTTPError{Status: http.StatusServiceUnavailable, Msg: "tickets unavailable"}
	}
	q := dal.New(s.apiDB.GetPool())
	rows, err := q.ListHelperPairingsForTickets(ctx, p.deviceID)
	if err != nil {
		return nil, helperInternalError(ctx, "helperIssueTickets", err)
	}
	exp := time.Now().Add(pairing.TicketLifetime)
	out := &services.HelperTickets{LifetimeSeconds: int32(pairing.TicketLifetime.Seconds())}
	volunteer := false
	modes := make([]string, 0, len(rows))
	roles := make([]pairing.Role, 0, len(rows))
	for _, r := range rows {
		role := pairing.RoleInviter
		if r.VolunteerDeviceID == p.deviceID {
			role = pairing.RoleVolunteer
			volunteer = true
		}
		tok, err := pairing.SignTicket(s.helperTicketKey, pairing.Ticket{
			Version: 1, PairingID: r.PairingID, Role: role, Expires: exp})
		if err != nil {
			return nil, helperInternalError(ctx, "helperIssueTickets", err)
		}
		out.Tickets = append(out.Tickets, tok)
		tag := ""
		if role == pairing.RoleInviter {
			tag = s.helperOutboundTag(r.PairingID)
		}
		out.InviterTags = append(out.InviterTags, tag)
		mode := helperModeRooms
		if r.Direct {
			mode = helperModeDirect
		}
		roles, modes = append(roles, role), append(modes, mode)
	}
	for i := range roles {
		recordHelperTicketIssued(ctx, string(roles[i]), modes[i])
	}
	// A volunteer that fetches tickets is running: its inviters see it
	// online for 15 minutes. A failure or timeout costs only that status.
	if volunteer {
		pctx, cancel := context.WithTimeout(ctx, helperPresenceTimeout)
		err := q.TouchHelperVolunteerPresence(pctx, p.deviceID)
		cancel()
		if err != nil {
			log.Error(ctx, "helperIssueTickets: presence failed", err, nil)
		}
	}
	// Counted only once the call has succeeded, so a client that retries a
	// failed refresh never has its bytes counted twice.
	recordHelperPairedBytes(ctx, q, p.deviceID, p.clientCountry, helperModeRooms, req.GetPairedBytes())
	return out, nil
}
```

- [ ] **Step 5: Build and run the tests**

Run: `mise r build -- ./cmd/api/...`
Expected: success.

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestHelperIssueTickets|TestHelperTicketsIssuedKnowDirectMode|TestHelperPresence|TestHelperGate|TestHelperCORS'`
Run: `mise r tests:run -- --pkg ./cmd/api/apipb/services/`
Expected: PASS. `TestValidationRulesCompile` passes because no rule was added.

- [ ] **Step 6: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
mise r checks:log-attr-keys
git add cmd/api/apipb cmd/api/helper_traffic.go cmd/api/helper_traffic_db_test.go cmd/api/helper_handler.go \
  openapiv2/services cmd/backoffice/src/gen/services
git commit -m "helper: count rooms traffic reported on ticket refresh, and tickets issued by mode"
```

---

### Task 6: Heartbeat traffic, then the lantern-cloud PR

**Files:**
- Modify: `cmd/api/peer_handler.go` (`Heartbeat`), `cmd/api/helper_traffic.go`
- Test: `cmd/api/helper_traffic_db_test.go` (append)

**Interfaces:**
- Consumes: Task 5's proto getters, `recordHelperPairedBytes` and `helperMaxReportedBytes`. Test helpers `newPeerTestService`, `newPeerTestRouter`, `registerAs`, `fakePeerVerifier` and `seedPeerTrack`.
- Produces: `func recordPeerRelayedBytes(ctx context.Context, volunteerCC string, total, paired uint64)`. lantern-cloud then accepts both report shapes, deployed.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/api/helper_traffic_db_test.go`, and add `"bytes"`, `"context"`, `"encoding/json"`, `"net/http/httptest"`, `"github.com/getlantern/common"` and `"github.com/getlantern/lantern-cloud/cmd/api/dal"` to its imports:

```go
func heartbeatWithUsage(t *testing.T, mux http.Handler, deviceID, cc string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/peer/heartbeat", bytes.NewReader(b))
	r.Header.Set("X-Lantern-Feature-Override", common.UNBOUNDED)
	r.Header.Set("X-Lantern-Device-Id", deviceID)
	r.Header.Set("X-Lantern-Client-Country", cc)
	r.RemoteAddr = "203.0.113.1:443"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestPeerHeartbeatCountsRelayedTraffic(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	mine, theirs := uuid.New(), uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: mine,
		InviterDeviceID: "friend", VolunteerDeviceID: "device-A", InviterCountry: "IR", VolunteerCountry: "DE"}))
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{PairingID: theirs,
		InviterDeviceID: "friend", VolunteerDeviceID: "device-B", InviterCountry: "RU"}))
	mux := newPeerTestRouter(t, ls)
	reg := registerAs(t, mux, "device-A")

	w := heartbeatWithUsage(t, mux, "device-A", "DE", map[string]any{"route_id": reg.RouteID, "active_clients": 1,
		"relayed_bytes": 10000, "paired_bytes": []map[string]any{
			{"pairing_id": mine.String(), "bytes": 3000},
			{"pairing_id": theirs.String(), "bytes": 500},
		}})
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	assert.Equal(t, map[string]float64{"direct|IR|DE": 3000},
		helperCounts(t, tester, metricHelperRelayedBytes, "mode", "inviter_cc", "volunteer_cc"))
	assert.Equal(t, map[string]float64{"paired|DE": 3000, "pool|DE": 7000},
		helperCounts(t, tester, metricPeerRelayedBytes, "kind", "volunteer_cc"),
		"a foreign pairing's bytes are not paired, so they count as pool")
	tester.RequireNoAttributeContains(t, mine.String(), theirs.String(), "device-A", "203.0.113")
}

func TestPeerHeartbeatPoolFloorsAtZeroAndCapsTotal(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	q := dal.New(tdb.GetPool())
	mine := uuid.New()
	require.NoError(t, q.InsertHelperPairing(context.Background(), dal.InsertHelperPairingParams{PairingID: mine,
		InviterDeviceID: "friend", VolunteerDeviceID: "device-A", InviterCountry: "IR"}))
	mux := newPeerTestRouter(t, ls)
	reg := registerAs(t, mux, "device-A")

	require.Equal(t, http.StatusOK, heartbeatWithUsage(t, mux, "device-A", "DE", map[string]any{"route_id": reg.RouteID,
		"relayed_bytes": 100, "paired_bytes": []map[string]any{{"pairing_id": mine.String(), "bytes": 300}}}).Code)
	assert.Equal(t, map[string]float64{"paired": 300}, helperCounts(t, tester, metricPeerRelayedBytes, "kind"),
		"pool is floored at 0")

	require.Equal(t, http.StatusOK, heartbeatWithUsage(t, mux, "device-A", "DE", map[string]any{"route_id": reg.RouteID,
		"relayed_bytes": helperMaxReportedBytes + 1}).Code)
	assert.Equal(t, map[string]float64{"direct": 1}, helperCounts(t, tester, metricHelperRelayedRejected, "mode"))
	assert.Equal(t, map[string]float64{"paired": 300}, helperCounts(t, tester, metricPeerRelayedBytes, "kind"),
		"an oversized total records nothing")
}

func TestPeerHeartbeatWithoutUsageCountsNothing(t *testing.T) {
	tester := metrics.NewMetricsTester()
	defer tester.Close()
	ls, _ := newPeerTestService(t, &fakePeerVerifier{})
	mux := newPeerTestRouter(t, ls)
	reg := registerAs(t, mux, "device-A")
	require.Equal(t, http.StatusOK, heartbeatWithUsage(t, mux, "device-A", "DE", map[string]any{"route_id": reg.RouteID}).Code)
	assert.Empty(t, tester.Named(t, metricPeerRelayedBytes))
	assert.Empty(t, tester.Named(t, metricHelperRelayedBytes))
}
```

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestPeerHeartbeatCountsRelayedTraffic|TestPeerHeartbeatPoolFloors|TestPeerHeartbeatWithoutUsage'`
Expected: FAIL. The first two find no `helper.relayed.bytes` or `peer.relayed.bytes`. The third passes already, and that's expected: it guards against a regression.

- [ ] **Step 2: Implement `recordPeerRelayedBytes` and call both recorders from Heartbeat**

Append to `cmd/api/helper_traffic.go`:

```go
// recordPeerRelayedBytes counts a direct-mode peer's reported total as
// paired bytes and pool bytes, pool being total minus paired, floored at 0.
// paired is what recordHelperPairedBytes counted for the same report. A
// total over helperMaxReportedBytes is dropped and counted as rejected.
func recordPeerRelayedBytes(ctx context.Context, volunteerCC string, total, paired uint64) {
	ctx = trace.Detached(ctx)
	if total > helperMaxReportedBytes {
		metrics.Inc(ctx, metricHelperRelayedRejected, "mode", helperModeDirect)
		return
	}
	var pool uint64
	if total > paired {
		pool = total - paired
	}
	cc := helperCC(volunteerCC)
	metrics.Add(ctx, metricPeerRelayedBytes, paired, "volunteer_cc", cc, "kind", "paired")
	metrics.Add(ctx, metricPeerRelayedBytes, pool, "volunteer_cc", cc, "kind", "pool")
}
```

In `Heartbeat` (`cmd/api/peer_handler.go`), between the `if rows == 0 { ... }` block and `return &services.PeerHeartbeatResponse{`, insert:

```go
	// Counted only for a heartbeat that succeeds, since a failed one is
	// retried with the same usage.
	paired := recordHelperPairedBytes(ctx, dal.New(ls.apiDB.GetPool()), deviceID, p.clientCountry, helperModeDirect, req.GetPairedBytes())
	recordPeerRelayedBytes(ctx, p.clientCountry, req.GetRelayedBytes(), paired)
```

- [ ] **Step 3: Run the full helper and peer suites**

Run: `mise r tests:run -- --pkg ./cmd/api/ --run 'TestPeer|TestHelper|TestDirectMode'`
Run: `mise r tests:run -- --pkg ./metrics/`
Expected: PASS.

- [ ] **Step 4: Lint and commit**

```bash
golangci-lint run --new-from-rev=origin/main ./...
mise r checks:log-attr-keys
git add cmd/api/peer_handler.go cmd/api/helper_traffic.go cmd/api/helper_traffic_db_test.go
git commit -m "peer: count relayed traffic reported on heartbeat, pool and paired"
```

- [ ] **Step 5: Check deploy path filters**

Run: `go list -deps ./cmd/api | grep '^github.com/getlantern/lantern-cloud/' | sed 's#github.com/getlantern/lantern-cloud/##' | sort -u`
Expected: `metrics` and `cmd/api/...` are already listed under `on.push.paths` in `.github/workflows/deploy-api.yml`. No new top-level package was imported.

- [ ] **Step 6: Open the PR with `skills:create-pr`**

Invoke `skills:create-pr`. Write the body to a literal path (`/tmp/lc-pp-metrics-pr.md`). Title: `Personal proxies metrics: funnel, gauges and reported traffic`. The body covers:
- the metrics added, with their labels;
- migration 000161 (additive nullable columns, applied by the normal deploy);
- the label privacy rule, and that report values are not logged, traced or stored;
- the 50 GB per-value cap;
- that radiance's reports are optional fields, so old clients are unaffected;
- the rulings from this plan's "Rulings" section;
- a link to the spec.

Let the skill run its Codex gate and hand off to `review-pr`. After merge, confirm the deploy finished before starting Task 10's PR.

---

## radiance

### Task 7: Rooms usage on the ticket fetch

**Files:**
- Create: `helper/usage.go`, `helper/usage_test.go`
- Modify: `helper/tickets.go` (`Fetcher`, `Fetch`), `helper/tickets_test.go`

**Interfaces:**
- Consumes: the lantern-cloud `/v1/helper/tickets` body from Task 5.
- Produces:
  - `type helper.PairedBytes struct { PairingID string \`json:"pairing_id"\`; Bytes uint64 \`json:"bytes"\` }`.
  - `type helper.Usage struct` (the zero value is ready, and every method is safe on a nil `*Usage`): `func (u *Usage) Add(pairing string, n uint64)`, `func (u *Usage) SetFlush(f func())`, `func (u *Usage) Take() []PairedBytes` (sorted by pairing ID, nil when empty) and `func (u *Usage) Restore(ps []PairedBytes)`.
  - `func (f *Fetcher) WithUsage(u *Usage) *Fetcher`.

- [ ] **Step 1: Write the failing tests**

`helper/usage_test.go`:

```go
package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	usagePairingA = "11111111-1111-4111-8111-111111111111"
	usagePairingB = "22222222-2222-4222-8222-222222222222"
)

func TestUsageAddsUpTakesAndRestores(t *testing.T) {
	var u Usage
	u.Add(usagePairingB, 30)
	u.Add(usagePairingA, 50)
	u.Add(usagePairingA, 20)
	u.Add(usagePairingA, 0)
	u.Add("", 99)
	got := u.Take()
	assert.Equal(t, []PairedBytes{{PairingID: usagePairingA, Bytes: 70}, {PairingID: usagePairingB, Bytes: 30}}, got)
	assert.Nil(t, u.Take(), "a take clears what it returned")

	u.Add(usagePairingA, 5)
	u.Restore(got)
	assert.Equal(t, []PairedBytes{{PairingID: usagePairingA, Bytes: 75}, {PairingID: usagePairingB, Bytes: 30}}, u.Take())
}

func TestUsageTakeFlushesFirst(t *testing.T) {
	var u Usage
	u.SetFlush(func() { u.Add(usagePairingA, 8) })
	assert.Equal(t, []PairedBytes{{PairingID: usagePairingA, Bytes: 8}}, u.Take())
}

func TestNilUsageIsInert(t *testing.T) {
	var u *Usage
	u.Add(usagePairingA, 1)
	u.SetFlush(func() {})
	u.Restore([]PairedBytes{{PairingID: usagePairingA, Bytes: 1}})
	assert.Nil(t, u.Take())
}
```

Append to `helper/tickets_test.go`, and add `"io"` to its imports:

```go
func TestFetcherReportsPairedBytesUntilDelivered(t *testing.T) {
	var fail atomic.Bool
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"tickets":[]}`))
	}))
	defer srv.Close()
	u := &Usage{}
	f := NewFetcher(srv.Client(), srv.URL+"/v1").WithUsage(u)
	u.Add(usagePairingB, 30)
	u.Add(usagePairingA, 70)

	fail.Store(true)
	_, err := f.Fetch(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
	fail.Store(false)
	_, err = f.Fetch(context.Background())
	require.NoError(t, err)
	_, err = f.Fetch(context.Background())
	require.NoError(t, err)

	want := `{"paired_bytes":[{"pairing_id":"` + usagePairingA + `","bytes":70},{"pairing_id":"` + usagePairingB + `","bytes":30}]}`
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 3)
	assert.JSONEq(t, want, bodies[0])
	assert.JSONEq(t, want, bodies[1], "a refused report is sent again")
	assert.JSONEq(t, `{}`, bodies[2], "a delivered report is not, and no zero entry goes out")
}

func TestFetcherWithoutUsageSendsAnEmptyObject(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"tickets":[]}`))
	}))
	defer srv.Close()
	_, err := NewFetcher(srv.Client(), srv.URL+"/v1").Fetch(context.Background())
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, body)
}
```

Run: `go test ./helper/ -run 'TestUsage|TestNilUsage|TestFetcherReportsPairedBytes|TestFetcherWithoutUsage'`
Expected: FAIL to compile, "undefined: Usage".

- [ ] **Step 2: Create `helper/usage.go`**

```go
package helper

import (
	"sort"
	"sync"
)

// PairedBytes is the bytes relayed for one helper pairing in a usage report.
// PairingID identifies a friend relationship and must never be logged.
type PairedBytes struct {
	PairingID string `json:"pairing_id"`
	Bytes     uint64 `json:"bytes"`
}

// Usage accumulates bytes relayed per helper pairing until a report takes
// them. It is in memory only, so a restart loses what was not yet reported.
// The zero value is ready to use, methods on a nil Usage do nothing, and it is
// safe for concurrent use.
type Usage struct {
	mu      sync.Mutex
	pending map[string]uint64
	flush   func()
}

// Add counts n bytes relayed for pairing. An empty pairing or a zero n is
// ignored.
func (u *Usage) Add(pairing string, n uint64) {
	if u == nil || pairing == "" || n == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.pending == nil {
		u.pending = make(map[string]uint64)
	}
	u.pending[pairing] += n
}

// SetFlush sets f to run at the start of every Take, without u's lock held,
// so a source that batches bytes can Add them first.
func (u *Usage) SetFlush(f func()) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.flush = f
}

// Take returns the bytes added since the last Take, sorted by pairing, and
// forgets them; nil when there are none.
func (u *Usage) Take() []PairedBytes {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	flush := u.flush
	u.mu.Unlock()
	if flush != nil {
		flush()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) == 0 {
		return nil
	}
	out := make([]PairedBytes, 0, len(u.pending))
	for id, n := range u.pending {
		out = append(out, PairedBytes{PairingID: id, Bytes: n})
	}
	clear(u.pending)
	sort.Slice(out, func(i, j int) bool { return out[i].PairingID < out[j].PairingID })
	return out
}

// Restore adds back what a Take returned, for a report that was not
// delivered.
func (u *Usage) Restore(ps []PairedBytes) {
	for _, p := range ps {
		u.Add(p.PairingID, p.Bytes)
	}
}
```

- [ ] **Step 3: Send usage on the ticket fetch**

In `helper/tickets.go`, add a `usage *Usage` field to `Fetcher`, and add `WithUsage` after `NewFetcher`:

```go
// Fetcher asks lantern-cloud for this device's tickets.
type Fetcher struct {
	httpClient *http.Client
	baseURL    string
	usage      *Usage
}
```

```go
// WithUsage makes every fetch report the rooms-mode bytes u has accumulated.
// A fetch lantern-cloud does not answer with a 2xx puts them back for the
// next.
func (f *Fetcher) WithUsage(u *Usage) *Fetcher {
	f.usage = u
	return f
}

// ticketRequest is the body of POST /helper/tickets.
type ticketRequest struct {
	PairedBytes []PairedBytes `json:"paired_bytes,omitempty"`
}
```

Replace the beginning of `Fetch`, from its signature through the `resp.Body.Close()` defer, with:

```go
func (f *Fetcher) Fetch(ctx context.Context) (Tickets, error) {
	paired := f.usage.Take()
	delivered := false
	defer func() {
		if !delivered {
			f.usage.Restore(paired)
		}
	}()
	body, err := json.Marshal(ticketRequest{PairedBytes: paired})
	if err != nil {
		return Tickets{}, fmt.Errorf("encode ticket request: %w", err)
	}
	// Untraced also keeps a redirect off /helper/ out of traces.
	r, err := common.NewRequestWithHeaders(traces.Untraced(ctx), http.MethodPost, f.baseURL+"/helper/tickets", bytes.NewReader(body))
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
	delivered = resp.StatusCode >= 200 && resp.StatusCode < 300
```

Leave the rest of `Fetch` (the status `switch` onward) unchanged. Add `"bytes"` to the imports, and remove `"strings"` if `go vet ./helper/` reports it unused.

- [ ] **Step 4: Run the tests**

Run: `go test ./helper/`
Expected: PASS.

- [ ] **Step 5: Review comments, then commit**

Spawn the `skills:comment-analyzer` agent on `git diff` for `helper/`, fix what it reports, and repeat until it reports nothing. Then:

```bash
go vet ./helper/
git add helper/usage.go helper/usage_test.go helper/tickets.go helper/tickets_test.go
git commit -m "helper: report rooms bytes per pairing on the ticket fetch"
```

---

### Task 8: Credit Unbounded slot bytes to pairings, and wire the usage

**Files:**
- Modify: `unbounded/unbounded.go` (manager field, `SetPairedUsage`), `unbounded/snapshot.go` (`addSlotBytes`, `foldPending`)
- Modify: `backend/radiance.go` (field, wiring)
- Test: `unbounded/snapshot_test.go` (append)

**Interfaces:**
- Consumes: Task 7's `helper.Usage`, `helper.PairedBytes` and `(*helper.Fetcher).WithUsage`.
- Produces: `func unbounded.SetPairedUsage(u *helper.Usage)` and `func (m *unboundedManager) foldPending()`. The backend field is `helperUsage *helper.Usage`.

- [ ] **Step 1: Write the failing test**

Append to `unbounded/snapshot_test.go`, and add `"github.com/getlantern/radiance/helper"` to its imports:

```go
func TestPairedSlotBytesAccrueToUsage(t *testing.T) {
	now := time.Unix(1000, 0)
	m, slots := newBytesManager(&now)
	m.live = helper.NewLiveTracker(nil)
	u := &helper.Usage{}
	m.usage = u
	u.SetFlush(m.foldPending)
	ctx := context.Background()
	down, up := chunkCounter(slots, true), chunkCounter(slots, false)

	m.recordConnection(ctx, 1, 0, "198.51.100.7")
	m.recordPairing(ctx, slots, 1, 0, testPairing)
	m.recordConnection(ctx, 1, 1, "198.51.100.8")
	m.recordPairing(ctx, slots, 1, 1, "")
	down(100, 0)
	up(20, 0)
	down(999, 1)

	require.Equal(t, []helper.PairedBytes{{PairingID: testPairing, Bytes: 120}}, u.Take(),
		"a take folds the slots first; pool bytes are not paired")
	require.Nil(t, u.Take())

	down(5, 0)
	_ = m.snapshot()
	require.Equal(t, []helper.PairedBytes{{PairingID: testPairing, Bytes: 5}}, u.Take(), "a snapshot's fold accrues too")
	require.Equal(t, uint64(1104), m.snapshot().BytesDown, "the widget's own totals are unchanged")
}
```

Run: `go test ./unbounded/ -run TestPairedSlotBytesAccrueToUsage`
Expected: FAIL to compile, "m.usage undefined".

- [ ] **Step 2: Implement**

In `unbounded/unbounded.go`, add a field to `unboundedManager` after `live *helper.LiveTracker`:

```go
	// usage receives the bytes relayed for each helper pairing, which the
	// ticket fetch reports. Nil counts nothing.
	usage *helper.Usage
```

Add after `SetTicketSource`:

```go
// SetPairedUsage sets where the widget counts the bytes it relays for each
// helper pairing. A take from u first folds the widget's pending slot bytes.
func SetPairedUsage(u *helper.Usage) {
	manager.mu.Lock()
	manager.usage = u
	manager.mu.Unlock()
	u.SetFlush(manager.foldPending)
}
```

In `unbounded/snapshot.go`, change `addSlotBytes`'s tally branch to:

```go
	if t != nil {
		t.down += down
		t.up += up
		if t.pairing != "" {
			m.usage.Add(t.pairing, down+up)
		}
	}
```

Add after `foldAllSlots`:

```go
// foldPending moves every slot's pending bytes into the totals, crediting
// paired bytes to m.usage.
func (m *unboundedManager) foldPending() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.foldAllSlots()
	m.foldRetiredRuns()
}
```

Lock order: `foldPending` takes `m.mu` and then `Usage.mu` (in `Add`). `Usage.Take` runs the flush without holding `Usage.mu`, so there is no cycle.

- [ ] **Step 3: Wire it in the backend**

In `backend/radiance.go`, add after the `helperTickets` field:

```go
	// helperUsage is the rooms-mode bytes relayed per helper pairing, which
	// the widget adds to and the ticket fetch reports.
	helperUsage *helper.Usage
```

Replace `r.helperTickets = helper.NewSource(ctx, helper.NewFetcher(kindling.HTTPClient(), common.GetBaseURL()).Fetch)` with:

```go
	r.helperUsage = &helper.Usage{}
	r.helperTickets = helper.NewSource(ctx, helper.NewFetcher(kindling.HTTPClient(), common.GetBaseURL()).WithUsage(r.helperUsage).Fetch)
```

Change the `if r.helperTickets != nil { unbounded.SetTicketSource(r.helperTickets) }` block to:

```go
	if r.helperTickets != nil {
		unbounded.SetTicketSource(r.helperTickets)
		unbounded.SetPairedUsage(r.helperUsage)
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./unbounded/ ./helper/ && go build ./... && go vet ./unbounded/ ./backend/`
Expected: PASS, and a clean build.

- [ ] **Step 5: Review comments, then commit**

Run the `skills:comment-analyzer` loop on the task's diff. Then:

```bash
git add unbounded/unbounded.go unbounded/snapshot.go unbounded/snapshot_test.go backend/radiance.go
git commit -m "unbounded: count rooms bytes per helper pairing for the ticket fetch"
```

---

### Task 9: Direct-mode relay usage accumulators

**Files:**
- Modify: `peer/snapshot.go` (`relayConn`, `relayStats`, `foldLocked`, `pruneLocked`, `recordPairingFor`, `setPairing`, and new `accrueLocked`, `takeUsage`, `restoreUsage`)
- Test: `peer/snapshot_test.go` (append)

**Interfaces:**
- Consumes: Task 7's `helper.Usage` and `helper.PairedBytes`.
- Produces: `type peer.RelayUsage struct { RelayedBytes uint64; PairedBytes []helper.PairedBytes }`, `func (r *relayStats) takeUsage() RelayUsage` and `func (r *relayStats) restoreUsage(u RelayUsage)`.

- [ ] **Step 1: Write the failing tests**

Append to `peer/snapshot_test.go`, and add `"github.com/getlantern/radiance/helper"` to its imports:

```go
const (
	usagePairingA = "11111111-1111-4111-8111-111111111111"
	usagePairingB = "22222222-2222-4222-8222-222222222222"
)

func TestRelayStats_UsageAddsUpPairedAndTotal(t *testing.T) {
	var r relayStats
	gen := r.begin()
	pool, a, b := counter(100, 10), counter(50, 5), counter(7, 3)
	r.recordPairingFor(gen, 1, "203.0.113.7:1000", "", pool)
	r.recordPairingFor(gen, 1, "203.0.113.8:1000", usagePairingA, a)
	r.recordPairingFor(gen, 1, "203.0.113.9:1000", usagePairingB, b)

	assert.Equal(t, RelayUsage{RelayedBytes: 175, PairedBytes: []helper.PairedBytes{
		{PairingID: usagePairingA, Bytes: 55}, {PairingID: usagePairingB, Bytes: 10}}}, r.takeUsage())

	a.AddDown(45)
	r.recordPairingFor(gen, -1, "203.0.113.8:1000", usagePairingA, a)
	a.AddDown(1000) // a closed connection is no longer read
	pool.AddUp(1)
	assert.Equal(t, RelayUsage{RelayedBytes: 46, PairedBytes: []helper.PairedBytes{
		{PairingID: usagePairingA, Bytes: 45}}}, r.takeUsage())
	assert.Equal(t, RelayUsage{}, r.takeUsage(), "nothing new, nothing to report")
	assert.Equal(t, uint64(1158), r.snapshot(true).BytesDown, "taking usage leaves the snapshot's totals alone")
}

func TestRelayStats_RestoredUsageIsReportedAgain(t *testing.T) {
	var r relayStats
	gen := r.begin()
	a := counter(1000, 24)
	r.recordPairingFor(gen, 1, "203.0.113.8:1000", usagePairingA, a)
	failed := r.takeUsage()
	a.AddUp(1)
	r.restoreUsage(failed)
	assert.Equal(t, RelayUsage{RelayedBytes: 1025, PairedBytes: []helper.PairedBytes{
		{PairingID: usagePairingA, Bytes: 1025}}}, r.takeUsage())
}

func TestRelayStats_LateAttributionCreditsThePairing(t *testing.T) {
	var r relayStats
	gen := r.begin()
	a := counter(300, 0)
	r.recordPairingFor(gen, 1, "203.0.113.8:1000", "", a)
	r.setPairing(a, usagePairingA)
	assert.Equal(t, RelayUsage{RelayedBytes: 300, PairedBytes: []helper.PairedBytes{
		{PairingID: usagePairingA, Bytes: 300}}}, r.takeUsage(),
		"bytes not yet reported go to the pairing the connection turned out to belong to")
}

func TestRelayStats_UsageSurvivesSessionEnd(t *testing.T) {
	now := time.Unix(1000, 0)
	r := relayStats{now: func() time.Time { return now }}
	gen := r.begin()
	a := counter(10, 0)
	r.recordPairingFor(gen, 1, "203.0.113.8:1000", usagePairingA, a)
	r.end()
	now = now.Add(relayRetention + time.Second)
	_ = r.snapshot(false) // prunes the ended session's connections
	assert.Equal(t, RelayUsage{RelayedBytes: 10, PairedBytes: []helper.PairedBytes{
		{PairingID: usagePairingA, Bytes: 10}}}, r.takeUsage())
}
```

Run: `go test ./peer/ -run 'TestRelayStats_Usage|TestRelayStats_Restored|TestRelayStats_LateAttribution'`
Expected: FAIL to compile, "undefined: RelayUsage".

- [ ] **Step 2: Implement in `peer/snapshot.go`**

Add `"github.com/getlantern/radiance/helper"` to the imports. Replace `relayConn`:

```go
// relayConn is an open connection's source and, once a later session has
// begun, when its own session was retired. A retired connection may still be
// draining in a box that has not finished closing, so its counter is read
// live for relayRetention before being folded into the totals.
type relayConn struct {
	source    string
	retiredAt time.Time
	// pairing is the helper pairing the connection came through, "" for the
	// pool or not yet attributed.
	pairing string
	// reported is how many of the counter's bytes are already in the
	// pending usage.
	reported uint64
}
```

Add to `relayStats`, after `gen uint64`:

```go
	// pendingTotal and pendingPaired are the bytes relayed since the last
	// heartbeat that reported them. Only takeUsage clears them: they carry
	// across sessions, unlike the snapshot's retention.
	pendingTotal  uint64
	pendingPaired helper.Usage
```

Add `RelayUsage` and the accrual methods after `relayStats.clock`:

```go
// RelayUsage is the traffic a heartbeat reports: every byte relayed since the
// last heartbeat that succeeded, and the part of it that came through each
// helper pairing.
type RelayUsage struct {
	RelayedBytes uint64
	PairedBytes  []helper.PairedBytes
}

// accrueLocked adds c's bytes since its last accrual to the pending usage and
// returns rc marked with them. Caller must hold r.mu.
func (r *relayStats) accrueLocked(c *peerconn.Counter, rc relayConn) relayConn {
	cur := c.Down() + c.Up()
	if cur <= rc.reported {
		return rc
	}
	n := cur - rc.reported
	rc.reported = cur
	r.pendingTotal += n
	r.pendingPaired.Add(rc.pairing, n)
	return rc
}

// takeUsage returns the bytes relayed since the last take and forgets them.
func (r *relayStats) takeUsage() RelayUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c, rc := range r.conns {
		r.conns[c] = r.accrueLocked(c, rc)
	}
	u := RelayUsage{RelayedBytes: r.pendingTotal, PairedBytes: r.pendingPaired.Take()}
	r.pendingTotal = 0
	return u
}

// restoreUsage puts back a takeUsage result whose heartbeat failed.
func (r *relayStats) restoreUsage(u RelayUsage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingTotal += u.RelayedBytes
	r.pendingPaired.Restore(u.PairedBytes)
}
```

Change `foldLocked` to take the `relayConn` and accrue first:

```go
// foldLocked moves a finished connection's count into the totals, its
// client's tally and the pending usage. Caller must hold r.mu and remove c
// from r.conns.
func (r *relayStats) foldLocked(c *peerconn.Counter, rc relayConn) {
	r.accrueLocked(c, rc)
	down, up := c.Down(), c.Up()
	r.closedDown += down
	r.closedUp += up
	if t := r.ips[ipOf(rc.source)]; t != nil {
		t.closedDown += down
		t.closedUp += up
	}
}
```

In `pruneLocked`, change `r.foldLocked(c, rc.source)` to `r.foldLocked(c, rc)`. In `recordPairingFor`'s close branch, change `r.foldLocked(bytes, rc.source)` to `r.foldLocked(bytes, rc)`. In its accept branch, replace `r.conns[bytes] = relayConn{source: source}` with:

```go
		// A counter seen again keeps what it has already reported.
		r.conns[bytes] = relayConn{source: source, pairing: pairing, reported: r.conns[bytes].reported}
```

In `setPairing`, after the `if !ok || !rc.retiredAt.IsZero() { return }` check, add:

```go
	rc.pairing = pairing
	r.conns[bytes] = rc
```

- [ ] **Step 3: Run the tests**

Run: `go test ./peer/ -run 'TestRelayStats|TestClient_'`
Expected: PASS.

- [ ] **Step 4: Review comments, then commit**

Run the `skills:comment-analyzer` loop on the task's diff. Then:

```bash
go vet ./peer/
git add peer/snapshot.go peer/snapshot_test.go
git commit -m "peer: accumulate relayed bytes, total and per pairing, until a heartbeat reports them"
```

---

### Task 10: Heartbeat reports usage, then the radiance PR

**Files:**
- Modify: `peer/api.go` (`HeartbeatRequest`, `Heartbeat`), `peer/peer.go` (`heartbeatLoop`, new `sendHeartbeat`)
- Modify (call sites): `peer/pairing_test.go:68` and `peer/peer_test.go:1216,1463,1470,1514`
- Test: `peer/peer_test.go` (append)

**Interfaces:**
- Consumes: Task 9's `RelayUsage`, `takeUsage` and `restoreUsage`. Task 5's heartbeat JSON fields.
- Produces: `func (a *API) Heartbeat(ctx context.Context, routeID string, activeClients int, usage RelayUsage) (*HeartbeatResponse, error)` and `func (c *Client) sendHeartbeat(ctx context.Context, routeID string) (*HeartbeatResponse, error)`.

- [ ] **Step 1: Write the failing test**

Append to `peer/peer_test.go`, and add `"sync/atomic"` and `"io"` to its imports if they aren't there:

```go
func TestHeartbeatReportsUsageUntilDelivered(t *testing.T) {
	var fail atomic.Bool
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		require.NoError(t, json.Unmarshal(b, &m))
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Client{cfg: Config{API: NewAPI(srv.Client(), srv.URL, "dev")}}
	gen := c.relay.begin()
	c.relay.recordPairingFor(gen, 1, "203.0.113.8:1000", usagePairingA, counter(1000, 24))
	c.relay.recordPairingFor(gen, 1, "203.0.113.9:1000", "", counter(76, 0))
	ctx := context.Background()

	fail.Store(true)
	_, err := c.sendHeartbeat(ctx, "route")
	require.Error(t, err)
	fail.Store(false)
	_, err = c.sendHeartbeat(ctx, "route")
	require.NoError(t, err)
	_, err = c.sendHeartbeat(ctx, "route")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 3)
	want := map[string]any{"route_id": "route", "active_clients": 0.0, "relayed_bytes": 1100.0,
		"paired_bytes": []any{map[string]any{"pairing_id": usagePairingA, "bytes": 1024.0}}}
	assert.Equal(t, want, bodies[0], "only the pairing ID and bytes are sent")
	assert.Equal(t, want, bodies[1], "a failed heartbeat's usage goes out again")
	assert.Equal(t, map[string]any{"route_id": "route", "active_clients": 0.0}, bodies[2],
		"a delivered report is cleared, and zero usage sends no fields")
}
```

Run: `go test ./peer/ -run TestHeartbeatReportsUsageUntilDelivered`
Expected: FAIL to compile, "c.sendHeartbeat undefined".

- [ ] **Step 2: Implement**

In `peer/api.go`, add `"github.com/getlantern/radiance/helper"` to the imports. Extend `HeartbeatRequest` after `ActiveClients`:

```go
	// RelayedBytes is every byte relayed since the last heartbeat that
	// succeeded; omitted when zero.
	RelayedBytes uint64 `json:"relayed_bytes,omitempty"`
	// PairedBytes is the part of RelayedBytes that came through each helper
	// pairing.
	PairedBytes []helper.PairedBytes `json:"paired_bytes,omitempty"`
```

Change `Heartbeat`'s signature and request construction. Add one sentence to its doc comment: "usage is the traffic to report; a heartbeat that returns an error was not recorded, so the caller reports its usage again."

```go
func (a *API) Heartbeat(ctx context.Context, routeID string, activeClients int, usage RelayUsage) (*HeartbeatResponse, error) {
	req := HeartbeatRequest{RouteID: routeID, ActiveClients: activeClients,
		RelayedBytes: usage.RelayedBytes, PairedBytes: usage.PairedBytes}
```

The rest of the body is unchanged.

In `peer/peer.go`, add before `heartbeatLoop`:

```go
// sendHeartbeat sends one heartbeat carrying the traffic relayed since the
// last one that succeeded. A heartbeat that fails keeps that traffic for the
// next.
func (c *Client) sendHeartbeat(ctx context.Context, routeID string) (*HeartbeatResponse, error) {
	usage := c.relay.takeUsage()
	resp, err := c.cfg.API.Heartbeat(ctx, routeID, c.ActiveClients(), usage)
	if err != nil {
		c.relay.restoreUsage(usage)
	}
	return resp, err
}
```

In `heartbeatLoop`, replace `resp, err := c.cfg.API.Heartbeat(hbCtx, routeID, c.ActiveClients())` with `resp, err := c.sendHeartbeat(hbCtx, routeID)`.

Update the existing call sites so they pass a zero `RelayUsage{}`: `peer/pairing_test.go:68` (`api.Heartbeat(context.Background(), "r", 0, RelayUsage{})`), and `peer/peer_test.go:1216`, `:1463`, `:1470` and `:1514` the same way. Run `git grep -n '\.Heartbeat(' origin/main -- 'peer/*_test.go'` first to confirm the line numbers.

- [ ] **Step 3: Run the tests**

Run: `go test ./peer/ ./helper/ ./unbounded/ ./backend/ && go build ./... && go vet ./peer/`
Expected: PASS.

- [ ] **Step 4: Review comments, then commit**

Run the `skills:comment-analyzer` loop on the task's diff. Then:

```bash
git add peer/api.go peer/peer.go peer/peer_test.go peer/pairing_test.go
git commit -m "peer: report relayed bytes, total and per pairing, on each heartbeat"
```

- [ ] **Step 5: Open the PR with `skills:create-pr`**

Do this only after the lantern-cloud PR (Task 6) is merged and deployed. Body at `/tmp/radiance-pp-metrics-pr.md`. Title: `Report personal-proxy traffic to lantern-cloud`. The body covers:
- direct mode reports `relayed_bytes` and `paired_bytes` on the heartbeat;
- rooms reports `paired_bytes` on the ticket refresh;
- the deltas are held until a 2xx, are in memory only, and contain nothing but pairing IDs and byte counts;
- the review-focus items checked: no new logging of pairing IDs (class 9), the lock order `m.mu` → `Usage.mu` (class 2), and no new network call on a start path (class 3);
- a link to the spec and to the lantern-cloud PR.

---

## lantern

### Task 11: Bump radiance

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: the merge commit of the radiance PR (Task 10).
- Produces: a lantern build that reports personal-proxy traffic.

- [ ] **Step 1: Bump and tidy**

```bash
cd <lantern worktree on branch bump-radiance-personal-proxies-metrics from origin/main>
go get github.com/getlantern/radiance@<radiance merge commit SHA>
go mod tidy
git diff --stat
```

Expected: only `go.mod` and `go.sum` changed, and the `github.com/getlantern/radiance` line names the new pseudo-version.

- [ ] **Step 2: Build and test the Go core**

Run: `go build ./lantern-core/... && go test ./lantern-core/...`
Expected: PASS. lantern calls none of the radiance APIs whose signatures changed: it imports `radiance/peer` and `radiance/unbounded` only for snapshots.

- [ ] **Step 3: Commit and open the PR**

```bash
git add go.mod go.sum
git commit -m "Bump radiance to <short sha>: report personal-proxy traffic"
```

Invoke `skills:create-pr` with the body at `/tmp/lantern-radiance-bump-pr.md`. It links the radiance PR and says the bump makes volunteer devices report traffic for the "Personal proxies" dashboard.

---

## SigNoz

### Task 12: "Personal proxies" dashboard and end-to-end check

**Files:** none. SigNoz, through MCP tools.

**Interfaces:**
- Consumes: every metric name from Task 2's constants, and radiance's existing `downlink_bytes` and `uplink_bytes`.
- Produces: a SigNoz dashboard titled "Personal proxies".

- [ ] **Step 1: Confirm the exported metric names**

After the lantern-cloud deploy, and once one invite has been created anywhere, call `signoz_list_metrics` with search text `helper` and then `peer.relayed`. OTel names may arrive with dots or with underscores (`helper_invites_created`). Use whichever spelling SigNoz lists in every query below. Call `signoz_get_field_keys` with `fieldContext=resource` for the metrics signal to find the resource attribute that tells prod apart (expect `service.name = 'api'`, plus a `deployment.environment` key or similar). Filter every query to production with it.

- [ ] **Step 2: Copy the widget schema from an existing dashboard**

Call `signoz_list_dashboards`, then `signoz_get_dashboard` on any dashboard that has time-series and table widgets on a builder metrics query. Reuse its widget JSON shape (`panelTypes`, `query.builder.queryData[]` with `aggregateAttribute`, `timeAggregation`, `spaceAggregation`, `groupBy`, `filters`, `legend`) for each widget below.

- [ ] **Step 3: Create the dashboard with `signoz_create_dashboard`**

Title: `Personal proxies`. Description: `Helper invites funnel, timings, live pairings and relayed traffic. Gauges use max across api replicas.` Every query filters `service.name = 'api'` and production, unless noted otherwise. Counters are delta sums, so use `timeAggregation: increase` (or `rate` where noted) with `spaceAggregation: sum`. Gauges use `timeAggregation: max` and `spaceAggregation: max`, never sum. Histograms use `spaceAggregation: p50` or `p90`.

| # | Widget | Type | Query (metric · filter · time agg · space agg · group by) |
|---|---|---|---|
| 1 | Invite funnel by inviter country | table (reduce: sum over range) | A `helper.invites.created` · `result = 'ok'` · increase · sum · `inviter_cc`; B `helper.invites.claimed` · `result = 'ok'` · increase · sum · `inviter_cc`; C `helper.invites.decided` · `decision = 'approve' AND result = 'ok'` · increase · sum · `inviter_cc`; D `helper.invites.expired` · `stage = 'pending'` · increase · sum · `inviter_cc`; E `helper.invites.expired` · `stage = 'claimed'` · increase · sum · `inviter_cc`. Legends: Created, Claimed, Approved, Expired unclaimed, Expired undecided |
| 2 | Funnel over time | time series (bars, 1 d step) | A, B, C, D and E from widget 1, with no group by |
| 3 | Invite outcomes | time series | A `helper.invites.created` · increase · sum · `result`; B `helper.invites.claimed` · increase · sum · `result`; C `helper.invites.decided` · increase · sum · `decision`, `result` |
| 4 | Time to claim | time series | A `helper.invite.claim_seconds` · p50; B same · p90. Unit: seconds |
| 5 | Time to decide | time series | A `helper.invite.decide_seconds` · p50; B same · p90. Unit: seconds |
| 6 | Active pairings | time series (stacked) | `helper.pairings.active` · max · max · `inviter_cc`, `volunteer_cc` |
| 7 | Online volunteers | time series | `helper.volunteers.online` · max · max · no group by |
| 8 | Pairings ended | time series | `helper.pairings.ended` · increase · sum · `by` |
| 9 | Pairings in use (tickets) | time series | `helper.tickets.issued` · rate · sum · `role`, `mode` |
| 10 | Paired traffic by mode | time series (bytes) | `helper.relayed.bytes` · increase · sum · `mode` |
| 11 | Paired traffic, inviter → volunteer country | table (reduce: sum) | `helper.relayed.bytes` · increase · sum · `inviter_cc`, `volunteer_cc` |
| 12 | Residential traffic, pool vs paired | time series (stacked, bytes) | `peer.relayed.bytes` · increase · sum · `kind` |
| 13 | Residential traffic by volunteer country | table (reduce: sum) | `peer.relayed.bytes` · increase · sum · `volunteer_cc`, `kind` |
| 14 | Rejected reports | time series | `helper.relayed.rejected` · increase · sum · `mode` |
| 15 | Inviter-side cross-check | time series (bytes) | A `downlink_bytes` · `outbound_name = 'helper'` · increase · sum; B `uplink_bytes` · same; C `helper.relayed.bytes` · increase · sum. Drop the `service.name = 'api'` filter on A and B: these come from radiance client telemetry. Find their service with `signoz_get_field_values` on `service.name` for `downlink_bytes` |

Layout: widgets 1 and 2 on the top row, 3 to 5 next, 6 to 9, then 10 to 15. Afterwards, call `signoz_get_dashboard` on the new dashboard, check that every widget has data or shows "no data" only where nothing has happened yet, and keep the returned `webUrl` for the report.

- [ ] **Step 4: End-to-end check (spec "Testing")**

On a dev or staging stack with the new lantern-cloud, and lantern builds carrying the bumped radiance:
1. Device A (inviter, with `X-Lantern-Client-Country` overridden to `IR` through the feature-override settings, or physically in a test region) invites device B (volunteer). B claims and A approves. Do this once with B serving direct mode (share my connection on) and once with B in rooms mode only (Unbounded widget on, direct off).
2. Move at least 10 MB through each pairing.
3. Within 15 minutes, `signoz_query_metrics` shows:
   - `helper.invites.created`, `claimed` and `decided` with `inviter_cc=IR` and B's country;
   - `helper.relayed.bytes` with `mode=direct` and `mode=rooms`, and `inviter_cc=IR`;
   - `peer.relayed.bytes` with `kind=paired` for B's country;
   - `helper.pairings.active` of at least 2, and `helper.volunteers.online` of at least 1.
4. Search logs and traces for the pairing IDs (`signoz_search_logs` and `signoz_search_traces` with body or attribute CONTAINS the pairing UUID). Expect no results.

Report the dashboard URL and the results of the check.

---

## Self-review

- **Spec coverage.**
  - Inviter country stored and copied: Tasks 1-3.
  - Volunteer country at approval: Tasks 1 and 3.
  - Every funnel metric and label: Tasks 2-4.
  - The histograms: Tasks 2-3, with buckets that reach 48 h.
  - Gauges sampled every 5 minutes, max on the dashboard: Tasks 4 and 12.
  - `helper.tickets.issued`: Task 5.
  - Rooms bytes on IssueTickets, with the request changed from `Empty`: Tasks 5, 7 and 8.
  - Direct bytes and residential totals on the heartbeat: Tasks 6, 9 and 10.
  - Ownership check, `inviter_cc` from the row, `volunteer_cc` from the request: Tasks 5-6.
  - 50 GB cap and `helper.relayed.rejected`: Tasks 5-6.
  - Pool floored at 0: Task 6.
  - Nothing stored, logged or traced: recorders on detached contexts, no new log line carries a value or ID, the `RequireNoAttributeContains` checks, and Task 12 step 4.
  - Migration up and down: Task 1.
  - The sweep's per-stage counts: Task 4.
  - radiance accumulators, failed report kept, success cleared, no zero entries, only ID and bytes sent: Tasks 7, 9 and 10.
  - Inviter-side cross-check: Task 12, widget 15.
  - Dashboard panels: Task 12.
  - Dev-stack end-to-end check: Task 12, step 4.
- **Placeholder scan.** No TBDs or "similar to" steps. `<radiance merge commit SHA>` and `<short sha>` in Task 11, and the lantern worktree path, are values that only exist at execution time.
- **Type consistency.** `helperStoredCC` and `helperCC` return `string`. Every `dal.*Params` country field is a `string`. `HelperInvite.InviterCountry`, `VolunteerCountry` and `ClaimedAt` are `sql.NullString`, `sql.NullString` and `sql.NullTime`, read with `.String` and `.Time`. `recordHelperPairedBytes` returns the `uint64` that `recordPeerRelayedBytes` takes as `paired`. `helperMaxReportedBytes` is typed `uint64`, so the tests' `helperMaxReportedBytes + 1` is a `uint64` that JSON encodes as a number. radiance's `RelayUsage.PairedBytes` and `HeartbeatRequest.PairedBytes` are both `[]helper.PairedBytes`. `Fetcher.WithUsage` returns `*Fetcher` for chaining in `backend/radiance.go`.
