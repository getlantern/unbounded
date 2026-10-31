# Helper Invites, Plan 1: Pairing Core (lantern-cloud) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give lantern-cloud a blind pairing service: inviters create single-use invites, volunteers claim them, inviters approve or reject after comparing a matching code, either side ends a pairing, and paired devices get short-lived signed tickets.

**Architecture:** A pure Go package `cmd/api/pairing` holds the invite cryptography and the ticket format, with published test vectors for the Dart, freddie and radiance implementations in later plans. A new `HelperService` gRPC service (HTTP under `/v1/helper/…`, same router bindings as the peer API) stores invites and pairings in two new tables through sqlc. The server only ever sees `H(s)`, an approval public key derived from `s`, opaque encrypted name blobs, device IDs and random pairing IDs.

**Tech Stack:** Go, PostgreSQL via sqlc (pgx/v5), protobuf/gRPC with buf, `golang.org/x/crypto/hkdf`, `crypto/ed25519`, `crypto/hmac`, testify, lantern-cloud `testdb`.

**Spec:** `docs/superpowers/specs/2026-10-20-helper-invites-design.md` (unbounded repo, branch `docs/helper-invites-spec`)

**Plan series:** 1 Pairing core (this plan) · 2 Direct-mode routing · 3 Unbounded rooms · 4 App · 5 Landing page and rewards. Later plans consume this plan's wire formats and test vectors.

## Global Constraints

- The server never receives the invite secret `s`. It stores `invite_hash = SHA-256(s)` and the approval public key only.
- No names, Telegram identifiers or account data are stored with pairings. Encrypted name blobs are opaque to the server, at most 512 bytes, and deleted once delivered.
- No log line, span attribute or metric may contain a pairing ID together with an IP address, device ID, display name or account. Spans record outcomes and timings only.
- Invite expiry: 48 hours.
- Inviter limits: 20 pending invites, 20 active pairings, 20 new invites per device per day. Volunteer limit: 20 pairings.
- Pairings are device-bound in this plan. Account binding needs authenticated user identity and is deferred to Plan 4.
- Every endpoint except `GET /v1/helper/ticket-key` requires `X-Lantern-Device-Id` and the `helper_invites` feature flag (overridable in tests with `X-Lantern-Feature-Override: helper_invites`). Plan 4 later exempted the volunteer endpoints (`ClaimInvite`, `ListPairings`, `EndPairing`, `IssueTickets`) from the flag; that is what shipped.
- Tickets live 15 minutes.
- Repo conventions: run `make sqlc` after query changes and `make proto` after proto changes, and commit the generated files with the change. Tests run with `bin/testsetup go test -tags=test ./cmd/api/...`.

## Wire formats (normative; later plans implement against these)

| Name | Definition |
|---|---|
| `s` | 32 random bytes. In links: base64url without padding, in the URL fragment |
| `invite_hash` | `SHA-256(s)` (32 bytes) |
| `K` | `HKDF-SHA256(ikm=s, salt=empty, info="lantern-helper-invite-v1/key")`, 32 bytes |
| Approval keypair | Ed25519 from seed `HKDF-SHA256(ikm=s, salt=empty, info="lantern-helper-invite-v1/approve")`, 32 bytes |
| Name-blob keys | `K_v = HKDF(s, "lantern-helper-invite-v1/volunteer-name")`, `K_i = HKDF(s, "lantern-helper-invite-v1/inviter-name")`; ChaCha20-Poly1305, blob = 12-byte random nonce ‖ ciphertext. Encrypted on the devices only; the server stores bytes |
| Matching code | `HMAC-SHA256(K, "code" ‖ volunteer_pubkey)`, first 4 bytes as big-endian uint32, mod 10000, zero-padded to 4 digits |
| Approval signature | Ed25519 signature by the approval key over `"approve" ‖ invite_hash ‖ volunteer_pubkey` |
| Ticket | `base64url(payload) + "." + base64url(ed25519 signature over the payload bytes)`. Payload is compact JSON `{"v":1,"p":"<pairing uuid>","r":"inviter"\|"volunteer","e":<unix seconds>}`. No device ID (final-review ruling F1: verifiers must not learn the API credential; the Task 2 code below predates this and the branch drops `DeviceID`) |

## File Structure

| File | Responsibility |
|---|---|
| `cmd/api/pairing/invite.go` | Key derivation, matching code, approval signature verification |
| `cmd/api/pairing/ticket.go` | Ticket signing and verification |
| `cmd/api/pairing/invite_test.go`, `ticket_test.go` | Unit tests and vector generation |
| `cmd/api/pairing/testdata/vectors.json` | Published test vectors for Plans 2–4 |
| `cmd/api/migrations/infra/000145_helper_invites.{up,down}.sql` | `helper_invites`, `helper_pairings` tables |
| `cmd/api/queries/infra/helper.sql` | sqlc queries |
| `cmd/api/apipb/proto/services/helper.proto` | `HelperService` API |
| `cmd/api/helper_handler.go` | Request preamble, limits, the service methods |
| `cmd/api/helper_handler_db_test.go` | DB-backed handler tests |
| `cmd/api/grpc_services.go`, `cmd/api/main.go` | Registration, ticket key loading |

---

### Task 1: Invite cryptography and test vectors

**Files:**
- Create: `cmd/api/pairing/invite.go`
- Create: `cmd/api/pairing/invite_test.go`
- Create: `cmd/api/pairing/testdata/vectors.json` (generated in Step 5)

**Interfaces:**
- Produces:
  - `func InviteHash(s []byte) []byte`
  - `func DeriveKey(s []byte) []byte`
  - `func ApprovalKey(s []byte) ed25519.PrivateKey`
  - `func MatchingCode(k, volunteerPub []byte) string`
  - `func ApprovalMessage(inviteHash, volunteerPub []byte) []byte`
  - `func VerifyApproval(approvalPub ed25519.PublicKey, inviteHash, volunteerPub, sig []byte) bool`
  - `const SecretLen = 32`

- [ ] **Step 1: Write the failing tests**

```go
package pairing

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedSecret() []byte { return bytes.Repeat([]byte{0x42}, SecretLen) }

func TestDerivationsAreDeterministicAndDistinct(t *testing.T) {
	s := fixedSecret()
	assert.Len(t, InviteHash(s), 32)
	assert.Equal(t, InviteHash(s), InviteHash(s))
	assert.Equal(t, DeriveKey(s), DeriveKey(s))
	assert.NotEqual(t, InviteHash(s), DeriveKey(s), "hash and key must differ")
	assert.Equal(t, ApprovalKey(s), ApprovalKey(s))
}

func TestMatchingCodeFormatAndBinding(t *testing.T) {
	k := DeriveKey(fixedSecret())
	pubA := bytes.Repeat([]byte{1}, ed25519.PublicKeySize)
	pubB := bytes.Repeat([]byte{2}, ed25519.PublicKeySize)
	code := MatchingCode(k, pubA)
	assert.Regexp(t, `^[0-9]{4}$`, code)
	assert.NotEqual(t, code, MatchingCode(k, pubB), "code is bound to the volunteer key")
	other := DeriveKey(bytes.Repeat([]byte{0x43}, SecretLen))
	assert.NotEqual(t, code, MatchingCode(other, pubA), "code is bound to the invite secret")
}

func TestApprovalSignatureVerifies(t *testing.T) {
	s := fixedSecret()
	priv := ApprovalKey(s)
	pub := priv.Public().(ed25519.PublicKey)
	vol := bytes.Repeat([]byte{7}, ed25519.PublicKeySize)
	sig := ed25519.Sign(priv, ApprovalMessage(InviteHash(s), vol))

	assert.True(t, VerifyApproval(pub, InviteHash(s), vol, sig))
	other := bytes.Repeat([]byte{8}, ed25519.PublicKeySize)
	assert.False(t, VerifyApproval(pub, InviteHash(s), other, sig), "signature is bound to the volunteer key")
	require.False(t, VerifyApproval(pub[:5], InviteHash(s), vol, sig), "a malformed key never verifies")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/api && go test ./pairing/ -run 'Derivations|MatchingCode|Approval' -v`
Expected: FAIL to compile with `undefined: SecretLen` and the other missing functions.

- [ ] **Step 3: Write the implementation**

```go
// Package pairing holds the cryptography of helper invites and the pairing
// ticket format. The server side never sees the invite secret s; it works
// with InviteHash(s) and the approval public key derived from s. Devices
// derive everything else. The wire formats here are shared with the app,
// freddie and radiance through testdata/vectors.json.
package pairing

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// SecretLen is the length of an invite secret in bytes.
const SecretLen = 32

const (
	infoKey     = "lantern-helper-invite-v1/key"
	infoApprove = "lantern-helper-invite-v1/approve"
)

// InviteHash is what the server stores in place of the secret.
func InviteHash(s []byte) []byte {
	h := sha256.Sum256(s)
	return h[:]
}

func expand(s []byte, info string) []byte {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, s, nil, []byte(info)), out); err != nil {
		panic(err) // HKDF-SHA256 cannot fail for 32 bytes of output
	}
	return out
}

// DeriveKey is K, the key both devices use for the matching code.
func DeriveKey(s []byte) []byte { return expand(s, infoKey) }

// ApprovalKey is the key the inviter signs an approval with. Its public half
// is registered when the invite is created.
func ApprovalKey(s []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(expand(s, infoApprove))
}

// MatchingCode is the four-digit code both people compare.
func MatchingCode(k, volunteerPub []byte) string {
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte("code"))
	mac.Write(volunteerPub)
	return fmt.Sprintf("%04d", binary.BigEndian.Uint32(mac.Sum(nil)[:4])%10000)
}

// ApprovalMessage is what an approval signature covers.
func ApprovalMessage(inviteHash, volunteerPub []byte) []byte {
	msg := make([]byte, 0, len("approve")+len(inviteHash)+len(volunteerPub))
	msg = append(msg, "approve"...)
	msg = append(msg, inviteHash...)
	return append(msg, volunteerPub...)
}

// VerifyApproval reports whether sig is the inviter's approval of volunteerPub
// for the invite. A malformed key never verifies.
func VerifyApproval(approvalPub ed25519.PublicKey, inviteHash, volunteerPub, sig []byte) bool {
	if len(approvalPub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(approvalPub, ApprovalMessage(inviteHash, volunteerPub), sig)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/api && go test ./pairing/ -run 'Derivations|MatchingCode|Approval' -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Add the vector generator and generate vectors**

Append to `invite_test.go`:

```go
// TestVectors pins the wire formats. Run with -update to regenerate
// testdata/vectors.json, which the app, freddie and radiance test against.
var update = flag.Bool("update", false, "regenerate testdata/vectors.json")

type inviteVector struct {
	Secret         string `json:"secret_b64url"`
	InviteHash     string `json:"invite_hash_hex"`
	Key            string `json:"key_hex"`
	ApprovalPub    string `json:"approval_pub_hex"`
	VolunteerPub   string `json:"volunteer_pub_hex"`
	MatchingCode   string `json:"matching_code"`
	ApprovalSigHex string `json:"approval_sig_hex"`
}

func TestVectors(t *testing.T) {
	var vs []inviteVector
	for i, fill := range []byte{0x00, 0x42, 0xff} {
		s := bytes.Repeat([]byte{fill}, SecretLen)
		vol := bytes.Repeat([]byte{byte(i + 1)}, ed25519.PublicKeySize)
		priv := ApprovalKey(s)
		vs = append(vs, inviteVector{
			Secret:         base64.RawURLEncoding.EncodeToString(s),
			InviteHash:     hex.EncodeToString(InviteHash(s)),
			Key:            hex.EncodeToString(DeriveKey(s)),
			ApprovalPub:    hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
			VolunteerPub:   hex.EncodeToString(vol),
			MatchingCode:   MatchingCode(DeriveKey(s), vol),
			ApprovalSigHex: hex.EncodeToString(ed25519.Sign(priv, ApprovalMessage(InviteHash(s), vol))),
		})
	}
	got, err := json.MarshalIndent(map[string]any{"invites": vs}, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("testdata", "vectors.json")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./pairing/ -run TestVectors -update")
	assert.JSONEq(t, string(want), string(got))
}
```

Add the imports `encoding/base64`, `encoding/hex`, `encoding/json`, `flag`, `os`, `path/filepath`.

Run: `cd cmd/api && go test ./pairing/ -run TestVectors -update && go test ./pairing/ -v`
Expected: `testdata/vectors.json` is created with three entries, and the whole package passes. Ed25519 signatures are deterministic, so the file is stable across runs.

- [ ] **Step 6: Commit**

```bash
git add cmd/api/pairing/invite.go cmd/api/pairing/invite_test.go cmd/api/pairing/testdata/vectors.json
git commit -m "pairing: helper-invite key derivation, matching code and approval signatures"
```

---

### Task 2: Pairing tickets

**Files:**
- Create: `cmd/api/pairing/ticket.go`
- Create: `cmd/api/pairing/ticket_test.go`
- Modify: `cmd/api/pairing/invite_test.go` (add ticket vectors to `TestVectors`)
- Modify: `cmd/api/pairing/testdata/vectors.json` (regenerated)

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `type Ticket struct { Version int; PairingID uuid.UUID; DeviceID string; Role Role; Expires time.Time }`
  - `type Role string` with `RoleInviter = "inviter"`, `RoleVolunteer = "volunteer"`
  - `const TicketLifetime = 15 * time.Minute`
  - `func SignTicket(priv ed25519.PrivateKey, t Ticket) (string, error)`
  - `func VerifyTicket(pub ed25519.PublicKey, token string, now time.Time) (Ticket, error)`
  - `var ErrTicketInvalid, ErrTicketExpired error`

- [ ] **Step 1: Write the failing tests**

```go
package pairing

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testTicketKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
}

func TestTicketRoundTrip(t *testing.T) {
	priv := testTicketKey()
	now := time.Unix(1_800_000_000, 0)
	in := Ticket{Version: 1, PairingID: uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"),
		DeviceID: "device-A", Role: RoleInviter, Expires: now.Add(TicketLifetime)}
	tok, err := SignTicket(priv, in)
	require.NoError(t, err)

	out, err := VerifyTicket(priv.Public().(ed25519.PublicKey), tok, now)
	require.NoError(t, err)
	assert.Equal(t, in.PairingID, out.PairingID)
	assert.Equal(t, "device-A", out.DeviceID)
	assert.Equal(t, RoleInviter, out.Role)
	assert.Equal(t, in.Expires.Unix(), out.Expires.Unix())
}

func TestTicketRejections(t *testing.T) {
	priv := testTicketKey()
	pub := priv.Public().(ed25519.PublicKey)
	now := time.Unix(1_800_000_000, 0)
	tok, err := SignTicket(priv, Ticket{Version: 1, PairingID: uuid.New(), DeviceID: "d",
		Role: RoleVolunteer, Expires: now.Add(time.Minute)})
	require.NoError(t, err)

	_, err = VerifyTicket(pub, tok, now.Add(2*time.Minute))
	assert.ErrorIs(t, err, ErrTicketExpired)

	payload, sig, _ := strings.Cut(tok, ".")
	tampered := payload[:len(payload)-2] + "AA." + sig
	_, err = VerifyTicket(pub, tampered, now)
	assert.ErrorIs(t, err, ErrTicketInvalid)

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	_, err = VerifyTicket(other.Public().(ed25519.PublicKey), tok, now)
	assert.ErrorIs(t, err, ErrTicketInvalid)

	_, err = VerifyTicket(pub, "not-a-ticket", now)
	assert.ErrorIs(t, err, ErrTicketInvalid)
}

func TestSignTicketRejectsBadRole(t *testing.T) {
	_, err := SignTicket(testTicketKey(), Ticket{Version: 1, PairingID: uuid.New(), DeviceID: "d", Role: "admin"})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd cmd/api && go test ./pairing/ -run Ticket -v`
Expected: FAIL to compile with `undefined: Ticket`.

- [ ] **Step 3: Write the implementation**

```go
package pairing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TicketLifetime bounds how long a revoked pairing can still be used.
const TicketLifetime = 15 * time.Minute

// Role says which side of a pairing a ticket was issued to.
type Role string

const (
	RoleInviter   Role = "inviter"
	RoleVolunteer Role = "volunteer"
)

var (
	ErrTicketInvalid = errors.New("pairing ticket invalid")
	ErrTicketExpired = errors.New("pairing ticket expired")
)

// Ticket says DeviceID may use PairingID, in Role, until Expires.
type Ticket struct {
	Version   int
	PairingID uuid.UUID
	DeviceID  string
	Role      Role
	Expires   time.Time
}

type ticketWire struct {
	V int    `json:"v"`
	P string `json:"p"`
	D string `json:"d"`
	R Role   `json:"r"`
	E int64  `json:"e"`
}

var b64 = base64.RawURLEncoding

// SignTicket encodes and signs t.
func SignTicket(priv ed25519.PrivateKey, t Ticket) (string, error) {
	if t.Role != RoleInviter && t.Role != RoleVolunteer {
		return "", fmt.Errorf("pairing: unknown ticket role %q", t.Role)
	}
	payload, err := json.Marshal(ticketWire{V: t.Version, P: t.PairingID.String(), D: t.DeviceID, R: t.Role, E: t.Expires.Unix()})
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// VerifyTicket checks token's signature and expiry at now.
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
	if err := json.Unmarshal(payload, &w); err != nil || w.V != 1 {
		return Ticket{}, ErrTicketInvalid
	}
	id, err := uuid.Parse(w.P)
	if err != nil || (w.R != RoleInviter && w.R != RoleVolunteer) || w.D == "" {
		return Ticket{}, ErrTicketInvalid
	}
	t := Ticket{Version: w.V, PairingID: id, DeviceID: w.D, Role: w.R, Expires: time.Unix(w.E, 0)}
	if !now.Before(t.Expires) {
		return t, ErrTicketExpired
	}
	return t, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd cmd/api && go test ./pairing/ -run Ticket -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Add ticket vectors**

In `TestVectors`, after building the invite vectors, add:

```go
	tpriv := testTicketKey()
	tok, err := SignTicket(tpriv, Ticket{Version: 1,
		PairingID: uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"),
		DeviceID:  "device-A", Role: RoleVolunteer, Expires: time.Unix(1_800_000_900, 0)})
	require.NoError(t, err)
	ticketVec := map[string]string{
		"public_key_hex": hex.EncodeToString(tpriv.Public().(ed25519.PublicKey)),
		"token":          tok,
		"verify_at_unix": "1800000000",
		"expired_at_unix": "1800000900",
	}
```

and change the marshalled object to `map[string]any{"invites": vs, "ticket": ticketVec}`. Add the imports `time` and `github.com/google/uuid`.

Run: `cd cmd/api && go test ./pairing/ -run TestVectors -update && go test ./pairing/ -v`
Expected: `vectors.json` gains a `ticket` object; the whole package passes.

- [ ] **Step 6: Commit**

```bash
git add cmd/api/pairing/
git commit -m "pairing: signed pairing tickets"
```

---

### Task 3: Storage

**Files:**
- Create: `cmd/api/migrations/infra/000145_helper_invites.up.sql`
- Create: `cmd/api/migrations/infra/000145_helper_invites.down.sql`
- Create: `cmd/api/queries/infra/helper.sql`
- Generated: `cmd/api/dal/helper.sql.go`, `cmd/api/dal/models.go`

**Interfaces:**
- Produces (sqlc-generated in `dal`, used by Tasks 5–8):
  - `InsertHelperInvite(ctx, InsertHelperInviteParams{InviteHash []byte; InviterDeviceID string; ApprovalPubkey []byte; ExpiresAt pgtype.Timestamptz}) error`
  - `CountPendingHelperInvites(ctx, inviterDeviceID string) (int64, error)`
  - `CountHelperInvitesSince(ctx, CountHelperInvitesSinceParams{InviterDeviceID string; Since pgtype.Timestamptz}) (int64, error)`
  - `ClaimHelperInvite(ctx, ClaimHelperInviteParams{InviteHash []byte; VolunteerDeviceID string; VolunteerPubkey []byte; VolunteerBlob []byte}) (int64, error)` (rows affected)
  - `GetHelperInviteForInviter(ctx, GetHelperInviteForInviterParams{InviteHash []byte; InviterDeviceID string}) (HelperInvite, error)`
  - `DeleteHelperInvite(ctx, inviteHash []byte) error`
  - `MarkHelperInviteRejected(ctx, inviteHash []byte) error`
  - `InsertHelperPairing(ctx, InsertHelperPairingParams{PairingID uuid.UUID; InviterDeviceID string; VolunteerDeviceID string; InviterBlob []byte}) error`
  - `CountHelperPairingsForInviter(ctx, inviterDeviceID string) (int64, error)`
  - `CountHelperPairingsForVolunteer(ctx, volunteerDeviceID string) (int64, error)`
  - `ListHelperPairingsForDevice(ctx, deviceID string) ([]HelperPairing, error)`
  - `TakeHelperPairingBlob(ctx, TakeHelperPairingBlobParams{PairingID uuid.UUID; VolunteerDeviceID string}) ([]byte, error)`
  - `DeleteHelperPairingForDevice(ctx, DeleteHelperPairingForDeviceParams{PairingID uuid.UUID; DeviceID string}) (int64, error)`
  - `GetHelperPairingForDevice(ctx, GetHelperPairingForDeviceParams{PairingID uuid.UUID; DeviceID string}) (HelperPairing, error)`
  - `DeleteExpiredHelperInvites(ctx) (int64, error)`

- [ ] **Step 1: Write the migration**

`000145_helper_invites.up.sql`:

```sql
-- Helper invites: a user in a censored country invites someone abroad to be a
-- dedicated proxy volunteer. The server never sees the invite secret; it keys
-- invites by SHA-256 of it and verifies approvals with a public key derived
-- from it. Name blobs are encrypted on the devices and opaque here.
CREATE TABLE helper_invites (
    invite_hash          BYTEA PRIMARY KEY CHECK (length(invite_hash) = 32),
    inviter_device_id    TEXT NOT NULL,
    approval_pubkey      BYTEA NOT NULL CHECK (length(approval_pubkey) = 32),
    state                TEXT NOT NULL DEFAULT 'pending'
                         CHECK (state IN ('pending', 'claimed', 'rejected')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ NOT NULL,
    volunteer_device_id  TEXT,
    volunteer_pubkey     BYTEA CHECK (volunteer_pubkey IS NULL OR length(volunteer_pubkey) = 32),
    volunteer_blob       BYTEA CHECK (volunteer_blob IS NULL OR length(volunteer_blob) <= 512)
);
CREATE INDEX helper_invites_inviter_idx ON helper_invites (inviter_device_id, created_at);

-- An approved pairing. No names, no invite secret, no account.
CREATE TABLE helper_pairings (
    pairing_id           UUID PRIMARY KEY,
    inviter_device_id    TEXT NOT NULL,
    volunteer_device_id  TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The inviter's encrypted display name, delivered once to the volunteer
    -- and then cleared.
    inviter_blob         BYTEA CHECK (inviter_blob IS NULL OR length(inviter_blob) <= 512)
);
CREATE INDEX helper_pairings_inviter_idx ON helper_pairings (inviter_device_id);
CREATE INDEX helper_pairings_volunteer_idx ON helper_pairings (volunteer_device_id);
```

`000145_helper_invites.down.sql`:

```sql
DROP TABLE helper_pairings;
DROP TABLE helper_invites;
```

- [ ] **Step 2: Write the queries**

`cmd/api/queries/infra/helper.sql`:

```sql
-- name: InsertHelperInvite :exec
INSERT INTO helper_invites (invite_hash, inviter_device_id, approval_pubkey, expires_at)
VALUES (@invite_hash, @inviter_device_id, @approval_pubkey, @expires_at);

-- name: CountPendingHelperInvites :one
SELECT count(*) FROM helper_invites
WHERE inviter_device_id = @inviter_device_id AND state IN ('pending', 'claimed') AND expires_at > now();

-- name: CountHelperInvitesSince :one
SELECT count(*) FROM helper_invites
WHERE inviter_device_id = @inviter_device_id AND created_at >= @since;

-- Single use: only a pending, unexpired invite can be claimed, and only once.
-- name: ClaimHelperInvite :execrows
UPDATE helper_invites
SET state = 'claimed', volunteer_device_id = @volunteer_device_id,
    volunteer_pubkey = @volunteer_pubkey, volunteer_blob = @volunteer_blob
WHERE invite_hash = @invite_hash AND state = 'pending' AND expires_at > now();

-- name: GetHelperInviteForInviter :one
SELECT * FROM helper_invites
WHERE invite_hash = @invite_hash AND inviter_device_id = @inviter_device_id AND expires_at > now();

-- name: DeleteHelperInvite :exec
DELETE FROM helper_invites WHERE invite_hash = @invite_hash;

-- A rejected invite stays until expiry so a second claim still reads
-- "already claimed" rather than succeeding.
-- name: MarkHelperInviteRejected :exec
UPDATE helper_invites
SET state = 'rejected', volunteer_pubkey = NULL, volunteer_blob = NULL
WHERE invite_hash = @invite_hash;

-- name: InsertHelperPairing :exec
INSERT INTO helper_pairings (pairing_id, inviter_device_id, volunteer_device_id, inviter_blob)
VALUES (@pairing_id, @inviter_device_id, @volunteer_device_id, @inviter_blob);

-- name: CountHelperPairingsForInviter :one
SELECT count(*) FROM helper_pairings WHERE inviter_device_id = @inviter_device_id;

-- name: CountHelperPairingsForVolunteer :one
SELECT count(*) FROM helper_pairings WHERE volunteer_device_id = @volunteer_device_id;

-- name: ListHelperPairingsForDevice :many
SELECT * FROM helper_pairings
WHERE inviter_device_id = @device_id OR volunteer_device_id = @device_id
ORDER BY created_at;

-- Delivers the inviter's encrypted name to the volunteer once.
-- name: TakeHelperPairingBlob :one
UPDATE helper_pairings p SET inviter_blob = NULL
FROM (SELECT pairing_id, inviter_blob FROM helper_pairings
      WHERE pairing_id = @pairing_id AND volunteer_device_id = @volunteer_device_id
      FOR UPDATE) old
WHERE p.pairing_id = old.pairing_id
RETURNING old.inviter_blob;

-- name: GetHelperPairingForDevice :one
SELECT * FROM helper_pairings
WHERE pairing_id = @pairing_id AND (inviter_device_id = @device_id OR volunteer_device_id = @device_id);

-- name: DeleteHelperPairingForDevice :execrows
DELETE FROM helper_pairings
WHERE pairing_id = @pairing_id AND (inviter_device_id = @device_id OR volunteer_device_id = @device_id);

-- name: DeleteExpiredHelperInvites :execrows
DELETE FROM helper_invites WHERE expires_at <= now();
```

- [ ] **Step 3: Generate and build**

Run: `make sqlc && go build ./cmd/api/...`
Expected: `cmd/api/dal/helper.sql.go` exists with the functions listed under Interfaces, and the build succeeds.

- [ ] **Step 4: Write a migration smoke test**

Create `cmd/api/helper_store_db_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/testdb"
)

func TestHelperInviteClaimIsSingleUse(t *testing.T) {
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	q := dal.New(tdb.GetPool())
	ctx := context.Background()
	hash := bytes.Repeat([]byte{1}, 32)
	require.NoError(t, q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{
		InviteHash: hash, InviterDeviceID: "inviter", ApprovalPubkey: bytes.Repeat([]byte{2}, 32),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}))
	claim := dal.ClaimHelperInviteParams{InviteHash: hash, VolunteerDeviceID: "vol",
		VolunteerPubkey: bytes.Repeat([]byte{3}, 32), VolunteerBlob: []byte("blob")}

	n, err := q.ClaimHelperInvite(ctx, claim)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.ClaimHelperInvite(ctx, claim)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "a second claim must not succeed")
}
```

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run TestHelperInviteClaimIsSingleUse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/api/migrations/infra/000145_helper_invites.up.sql cmd/api/migrations/infra/000145_helper_invites.down.sql cmd/api/queries/infra/helper.sql cmd/api/dal/ cmd/api/helper_store_db_test.go
git commit -m "api: helper invite and pairing storage"
```

---

### Task 4: HelperService API skeleton and gate

**Files:**
- Create: `cmd/api/apipb/proto/services/helper.proto`
- Generated: `cmd/api/apipb/services/helper.pb.go`, `helper_grpc.pb.go`, openapi and TS outputs
- Create: `cmd/api/helper_handler.go`
- Create: `cmd/api/helper_handler_db_test.go`
- Modify: `cmd/api/grpc_services.go` (register the service)

**Interfaces:**
- Consumes: `peerBindings`, `featureOverrides`, `featureOverrideEnabled`, `router.*`, `util.ClientIPString`, `maxmind.LookupCountryASNState` (existing).
- Produces:
  - `type helperService struct{ *lanternService }` implementing `services.HelperServiceServer`
  - `func helperRequest(ctx context.Context, handler string) (helperRequestContext, error)` returning `{ctx, deviceID, clientIP, clientCountry}`
  - `const featureFlagHelperInvites = "helper_invites"`
  - Test helpers `newHelperTestService(t) (*lanternService, *testdb.TestDB)`, `newHelperTestRouter(t, ls) http.Handler`, `helperPost(t, mux, path, deviceID string, body any) *httptest.ResponseRecorder`

- [ ] **Step 1: Write the proto**

```proto
syntax = "proto3";
package lantern_cloud.api;

import "buf/validate/validate.proto";
import "google/api/annotations.proto";
import "google/protobuf/empty.proto";

option go_package = "github.com/getlantern/lantern-cloud/cmd/api/apipb/services";

// HelperService pairs a user in a censored country with a volunteer abroad.
// The server never sees the invite secret: invites are keyed by its SHA-256,
// and approvals are verified with a public key derived from it. Every call
// except TicketKey carries X-Lantern-Device-Id and is gated by the
// helper_invites feature flag; a client the flag is off for gets NotFound.
service HelperService {
  rpc CreateInvite(HelperCreateInviteRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/helper/invites" body: "*" };
  }
  rpc ClaimInvite(HelperClaimInviteRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/helper/invites/claim" body: "*" };
  }
  // GetInvite returns the pending claim, if any, to the inviter.
  rpc GetInvite(HelperInviteRef) returns (HelperInviteStatus) {
    option (google.api.http) = { post: "/v1/helper/invites/get" body: "*" };
  }
  rpc DecideInvite(HelperDecideInviteRequest) returns (HelperDecideInviteResponse) {
    option (google.api.http) = { post: "/v1/helper/invites/decide" body: "*" };
  }
  rpc ListPairings(google.protobuf.Empty) returns (HelperPairingList) {
    option (google.api.http) = { post: "/v1/helper/pairings" body: "*" };
  }
  rpc EndPairing(HelperPairingRef) returns (google.protobuf.Empty) {
    option (google.api.http) = { post: "/v1/helper/pairings/end" body: "*" };
  }
  rpc IssueTickets(google.protobuf.Empty) returns (HelperTickets) {
    option (google.api.http) = { post: "/v1/helper/tickets" body: "*" };
  }
  // TicketKey is public: freddie and volunteers verify tickets with it.
  rpc TicketKey(google.protobuf.Empty) returns (HelperTicketKey) {
    option (google.api.http) = { get: "/v1/helper/ticket-key" };
  }
}

message HelperCreateInviteRequest {
  bytes invite_hash = 1 [(buf.validate.field).bytes.len = 32];
  bytes approval_pubkey = 2 [(buf.validate.field).bytes.len = 32];
}

message HelperClaimInviteRequest {
  bytes invite_hash = 1 [(buf.validate.field).bytes.len = 32];
  bytes volunteer_pubkey = 2 [(buf.validate.field).bytes.len = 32];
  bytes volunteer_blob = 3 [(buf.validate.field).bytes.max_len = 512];
}

message HelperInviteRef {
  bytes invite_hash = 1 [(buf.validate.field).bytes.len = 32];
}

message HelperInviteStatus {
  // "pending" (unclaimed), "claimed" (awaiting the inviter) or "rejected".
  string state = 1;
  bytes volunteer_pubkey = 2;
  bytes volunteer_blob = 3;
}

message HelperDecideInviteRequest {
  bytes invite_hash = 1 [(buf.validate.field).bytes.len = 32];
  bool approve = 2;
  // Required when approve is true: the approval signature.
  bytes approval_sig = 3 [(buf.validate.field).bytes.max_len = 64];
  // The inviter's encrypted display name, delivered once to the volunteer.
  bytes inviter_blob = 4 [(buf.validate.field).bytes.max_len = 512];
}

message HelperDecideInviteResponse {
  string pairing_id = 1;
}

message HelperPairing {
  string pairing_id = 1;
  // "inviter" or "volunteer": this device's side of the pairing.
  string role = 2;
  // For a volunteer, the inviter's encrypted name the first time it is listed.
  bytes inviter_blob = 3;
}

message HelperPairingList {
  repeated HelperPairing pairings = 1;
}

message HelperPairingRef {
  string pairing_id = 1;
}

message HelperTickets {
  repeated string tickets = 1;
  int32 lifetime_seconds = 2;
}

message HelperTicketKey {
  bytes public_key = 1;
}
```

Run: `make proto && go build ./cmd/api/...`
Expected: generation succeeds and the build passes (nothing references `HelperService` yet).

- [ ] **Step 2: Write the failing gate test**

`cmd/api/helper_handler_db_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb/services"
	"github.com/getlantern/lantern-cloud/maxmind"
	"github.com/getlantern/lantern-cloud/router"
	"github.com/getlantern/lantern-cloud/settings"
	"github.com/getlantern/lantern-cloud/testdb"
)

func newHelperTestService(t *testing.T) (*lanternService, *testdb.TestDB) {
	t.Helper()
	srv := maxmind.SetupMaxmindTestServer(t)
	t.Cleanup(srv.Close)
	maxmind.WaitForTestMaxmindServer(t)
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	return &lanternService{
		apiDB:            tdb,
		settings:         settings.MakeMockManager(),
		helperTicketKey:  ed25519.NewKeyFromSeed(seed),
	}, tdb
}

func newHelperTestRouter(t testing.TB, ls *lanternService) http.Handler {
	t.Helper()
	root := router.New()
	newGRPCServers(context.Background(), "helper-test", newTestJWTManager(t)).mount(apiService{
		desc: &services.HelperService_ServiceDesc, srv: helperService{ls},
		router: root.NewGroup("/v1"), opts: peerBindings})
	return root
}

// helperPost sends body as JSON to path as deviceID, with the flag on unless
// deviceID is empty.
func helperPost(t *testing.T, mux http.Handler, path, deviceID string, body any, flagOn bool) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	if flagOn {
		r.Header.Set("X-Lantern-Feature-Override", featureFlagHelperInvites)
	}
	if deviceID != "" {
		r.Header.Set("X-Lantern-Device-Id", deviceID)
	}
	r.RemoteAddr = "203.0.113.1:443"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestHelperGate(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	w := helperPost(t, mux, "/v1/helper/pairings", "device-A", map[string]any{}, false)
	assert.Equal(t, http.StatusNotFound, w.Code, "flag off reads as not found")
	w = helperPost(t, mux, "/v1/helper/pairings", "", map[string]any{}, true)
	assert.Equal(t, http.StatusBadRequest, w.Code, "device id required")
	w = helperPost(t, mux, "/v1/helper/pairings", "device-A", map[string]any{}, true)
	assert.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
}
```

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run TestHelperGate -v`
Expected: FAIL to compile with `undefined: helperService` and `unknown field helperTicketKey`.

- [ ] **Step 3: Write the skeleton**

Add the field to `lanternService` in `cmd/api/main.go`, next to `peerVerifier`:

```go
	// helperTicketKey signs helper pairing tickets; nil disables ticket issuance.
	helperTicketKey ed25519.PrivateKey
```

`cmd/api/helper_handler.go`:

```go
package main

import (
	"context"
	"net"
	"net/http"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb/services"
	"github.com/getlantern/lantern-cloud/flags"
	"github.com/getlantern/lantern-cloud/maxmind"
	"github.com/getlantern/lantern-cloud/router"
	"github.com/getlantern/lantern-cloud/util"
)

// featureFlagHelperInvites gates the helper invite API.
const featureFlagHelperInvites = "helper_invites"

// helperService serves HelperService. It never logs pairing IDs, device IDs
// or IPs together: spans and logs carry outcomes only.
type helperService struct{ *lanternService }

type helperRequestContext struct {
	ctx           context.Context
	deviceID      string
	clientIP      string
	clientCountry string
}

// helperRequest is the preamble every authenticated helper method shares.
func helperRequest(ctx context.Context, handler string) (helperRequestContext, error) {
	header := router.RequestHeaders(ctx)
	p := helperRequestContext{ctx: ctx, deviceID: router.HeaderGet(header, "X-Lantern-Device-Id")}
	p.clientIP = util.ClientIPString(ctx, &header, router.RequestAddr(ctx))
	p.clientCountry, _, _, _ = maxmind.LookupCountryASNState(ctx, &header, net.ParseIP(p.clientIP), handler)
	if !helperInvitesEnabled(header, p.clientIP, p.clientCountry) {
		return p, router.NotFound("404 page not found")
	}
	if p.deviceID == "" {
		return p, router.BadRequest("missing X-Lantern-Device-Id")
	}
	return p, nil
}

func helperInvitesEnabled(header http.Header, clientIP, country string) bool {
	if featureOverrideEnabled(featureOverrides(header), featureFlagHelperInvites) {
		return true
	}
	return flags.IsFeatureEnabled(featureFlagHelperInvites, "", clientIP, map[string]string{"countryCode": country})
}

func (s helperService) ListPairings(ctx context.Context, _ *emptypb.Empty) (*services.HelperPairingList, error) {
	if _, err := helperRequest(ctx, "helperListPairings"); err != nil {
		return nil, err
	}
	return &services.HelperPairingList{}, nil
}
```

Register it in `grpc_services.go` directly after the `PeerService` entry:

```go
		{desc: &services.HelperService_ServiceDesc, srv: helperService{ls}, router: public, opts: peerBindings},
```

The generated server interface has `require_unimplemented_servers=false`, so the methods not yet written return Unimplemented until Tasks 5–8 add them.

Check the `flags` import path matches `peer_handler.go`'s and adjust if it differs.

- [ ] **Step 4: Run the gate test**

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run TestHelperGate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/api/apipb/ cmd/api/helper_handler.go cmd/api/helper_handler_db_test.go cmd/api/grpc_services.go cmd/api/main.go openapiv2 backoffice/src/gen dashboard/src/gen
git commit -m "api: HelperService skeleton behind the helper_invites flag"
```

---

### Task 5: Create and claim invites

**Files:**
- Modify: `cmd/api/helper_handler.go`
- Modify: `cmd/api/helper_handler_db_test.go`

**Interfaces:**
- Consumes: `dal.InsertHelperInvite`, `dal.CountPendingHelperInvites`, `dal.CountHelperInvitesSince`, `dal.ClaimHelperInvite` (Task 3); `pairing.InviteHash`, `pairing.ApprovalKey` (Task 1, tests only).
- Produces: `CreateInvite`, `ClaimInvite`; constants `helperInviteTTL = 48 * time.Hour`, `helperMaxPendingInvites = 20`, `helperMaxInvitesPerDay = 20`, `helperMaxVolunteerPairings = 20`; test helper `newTestInvite(t) (s []byte, createBody map[string]any)`.

- [ ] **Step 1: Write the failing tests**

Append to `helper_handler_db_test.go` (add imports `crypto/rand` and `github.com/getlantern/lantern-cloud/cmd/api/pairing`):

```go
// newTestInvite returns a fresh secret and the CreateInvite body for it.
func newTestInvite(t *testing.T) ([]byte, map[string]any) {
	t.Helper()
	s := make([]byte, pairing.SecretLen)
	_, err := rand.Read(s)
	require.NoError(t, err)
	return s, map[string]any{
		"invite_hash":     pairing.InviteHash(s),
		"approval_pubkey": []byte(pairing.ApprovalKey(s).Public().(ed25519.PublicKey)),
	}
}

func claimBody(s, volunteerPub []byte) map[string]any {
	return map[string]any{"invite_hash": pairing.InviteHash(s), "volunteer_pubkey": volunteerPub,
		"volunteer_blob": []byte("opaque-name")}
}

func TestHelperCreateAndClaim(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, body := newTestInvite(t)

	w := helperPost(t, mux, "/v1/helper/invites", "inviter", body, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	vol := bytes.Repeat([]byte{5}, 32)
	w = helperPost(t, mux, "/v1/helper/invites/claim", "volunteer", claimBody(s, vol), true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	w = helperPost(t, mux, "/v1/helper/invites/claim", "someone-else", claimBody(s, vol), true)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "already claimed")
}

func TestHelperClaimUnknownInvite(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, _ := newTestInvite(t)
	w := helperPost(t, mux, "/v1/helper/invites/claim", "volunteer", claimBody(s, bytes.Repeat([]byte{5}, 32)), true)
	assert.Equal(t, http.StatusConflict, w.Code, "unknown and used invites read the same")
}

func TestHelperInviteLimits(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	for i := 0; i < helperMaxPendingInvites; i++ {
		_, body := newTestInvite(t)
		require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/invites", "inviter", body, true).Code)
	}
	_, body := newTestInvite(t)
	w := helperPost(t, mux, "/v1/helper/invites", "inviter", body, true)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}
```

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelperCreateAndClaim|TestHelperClaimUnknownInvite|TestHelperInviteLimits' -v`
Expected: FAIL; the handlers return Unimplemented and the constants are undefined.

- [ ] **Step 2: Implement**

Append to `helper_handler.go` (add imports `errors`, `time`, `github.com/jackc/pgx/v5/pgconn`, `github.com/jackc/pgx/v5/pgtype`, `github.com/getlantern/lantern-cloud/cmd/api/dal`):

```go
const (
	helperInviteTTL            = 48 * time.Hour
	helperMaxPendingInvites    = 20
	helperMaxInvitesPerDay     = 20
	helperMaxInviterPairings   = 20
	helperMaxVolunteerPairings = 20
)

var errHelperAlreadyClaimed = &router.HTTPError{Status: http.StatusConflict, Msg: "invite already claimed or expired"}

func (s helperService) CreateInvite(ctx context.Context, req *services.HelperCreateInviteRequest) (*emptypb.Empty, error) {
	p, err := helperRequest(ctx, "helperCreateInvite")
	if err != nil {
		return nil, err
	}
	q := dal.New(s.apiDB.GetPool())
	pending, err := q.CountPendingHelperInvites(ctx, p.deviceID)
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	today, err := q.CountHelperInvitesSince(ctx, dal.CountHelperInvitesSinceParams{
		InviterDeviceID: p.deviceID,
		Since:           pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
	})
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	if pending >= helperMaxPendingInvites || today >= helperMaxInvitesPerDay {
		return nil, &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many invites"}
	}
	if err := q.InsertHelperInvite(ctx, dal.InsertHelperInviteParams{
		InviteHash:      req.InviteHash,
		InviterDeviceID: p.deviceID,
		ApprovalPubkey:  req.ApprovalPubkey,
		ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(helperInviteTTL), Valid: true},
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// A duplicate hash is a client bug or a replay; it never overwrites.
			return nil, &router.HTTPError{Status: http.StatusConflict, Msg: "invite exists"}
		}
		return nil, router.InternalError("internal error")
	}
	return &emptypb.Empty{}, nil
}

func (s helperService) ClaimInvite(ctx context.Context, req *services.HelperClaimInviteRequest) (*emptypb.Empty, error) {
	p, err := helperRequest(ctx, "helperClaimInvite")
	if err != nil {
		return nil, err
	}
	q := dal.New(s.apiDB.GetPool())
	n, err := q.CountHelperPairingsForVolunteer(ctx, p.deviceID)
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	if n >= helperMaxVolunteerPairings {
		return nil, &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many pairings"}
	}
	rows, err := q.ClaimHelperInvite(ctx, dal.ClaimHelperInviteParams{
		InviteHash: req.InviteHash, VolunteerDeviceID: p.deviceID,
		VolunteerPubkey: req.VolunteerPubkey, VolunteerBlob: req.VolunteerBlob,
	})
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	if rows == 0 {
		// Unknown, used, rejected and expired invites are indistinguishable,
		// so a claimant learns nothing about other people's invites.
		return nil, errHelperAlreadyClaimed
	}
	return &emptypb.Empty{}, nil
}
```

- [ ] **Step 3: Run the tests**

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelper' -v`
Expected: PASS, including `TestHelperGate`.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/helper_handler.go cmd/api/helper_handler_db_test.go
git commit -m "api: create and claim helper invites"
```

---

### Task 6: Inviter reads the claim and decides

**Files:**
- Modify: `cmd/api/helper_handler.go`
- Modify: `cmd/api/helper_handler_db_test.go`

**Interfaces:**
- Consumes: `dal.GetHelperInviteForInviter`, `dal.DeleteHelperInvite`, `dal.MarkHelperInviteRejected`, `dal.InsertHelperPairing`, `dal.CountHelperPairingsForInviter` (Task 3); `pairing.VerifyApproval`, `pairing.ApprovalMessage` (Task 1).
- Produces: `GetInvite`, `DecideInvite`; test helper `approveBody(s, volunteerPub []byte) map[string]any`.

- [ ] **Step 1: Write the failing tests**

```go
func approveBody(s, volunteerPub []byte) map[string]any {
	sig := ed25519.Sign(pairing.ApprovalKey(s), pairing.ApprovalMessage(pairing.InviteHash(s), volunteerPub))
	return map[string]any{"invite_hash": pairing.InviteHash(s), "approve": true,
		"approval_sig": sig, "inviter_blob": []byte("opaque-inviter-name")}
}

// createAndClaim runs an invite through claim and returns its secret and the
// volunteer key.
func createAndClaim(t *testing.T, mux http.Handler) ([]byte, []byte) {
	t.Helper()
	s, body := newTestInvite(t)
	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/invites", "inviter", body, true).Code)
	vol := bytes.Repeat([]byte{5}, 32)
	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/invites/claim", "volunteer", claimBody(s, vol), true).Code)
	return s, vol
}

func TestHelperGetInviteShowsClaimToInviterOnly(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, vol := createAndClaim(t, mux)
	ref := map[string]any{"invite_hash": pairing.InviteHash(s)}

	w := helperPost(t, mux, "/v1/helper/invites/get", "inviter", ref, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var st struct {
		State           string `json:"state"`
		VolunteerPubkey []byte `json:"volunteer_pubkey"`
		VolunteerBlob   []byte `json:"volunteer_blob"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	assert.Equal(t, "claimed", st.State)
	assert.Equal(t, vol, st.VolunteerPubkey)
	assert.Equal(t, []byte("opaque-name"), st.VolunteerBlob)

	w = helperPost(t, mux, "/v1/helper/invites/get", "volunteer", ref, true)
	assert.Equal(t, http.StatusNotFound, w.Code, "only the inviter's device sees the claim")
}

func TestHelperApproveCreatesPairing(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, vol := createAndClaim(t, mux)

	w := helperPost(t, mux, "/v1/helper/invites/decide", "inviter", approveBody(s, vol), true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct{ PairingID string `json:"pairing_id"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.PairingID)

	w = helperPost(t, mux, "/v1/helper/invites/get", "inviter", map[string]any{"invite_hash": pairing.InviteHash(s)}, true)
	assert.Equal(t, http.StatusNotFound, w.Code, "an approved invite is gone")
}

func TestHelperApproveRejectsBadSignature(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, _ := createAndClaim(t, mux)
	other := bytes.Repeat([]byte{6}, 32)
	w := helperPost(t, mux, "/v1/helper/invites/decide", "inviter", approveBody(s, other), true)
	assert.Equal(t, http.StatusForbidden, w.Code, "a signature over a different volunteer key must not approve")
}

func TestHelperApproveRequiresInviterDevice(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, vol := createAndClaim(t, mux)
	w := helperPost(t, mux, "/v1/helper/invites/decide", "volunteer", approveBody(s, vol), true)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHelperRejectBurnsInvite(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	s, vol := createAndClaim(t, mux)
	w := helperPost(t, mux, "/v1/helper/invites/decide", "inviter",
		map[string]any{"invite_hash": pairing.InviteHash(s), "approve": false}, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	w = helperPost(t, mux, "/v1/helper/invites/claim", "real-friend", claimBody(s, vol), true)
	assert.Equal(t, http.StatusConflict, w.Code, "a rejected invite can't be claimed again")
}
```

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelperGetInvite|TestHelperApprove|TestHelperReject' -v`
Expected: FAIL with Unimplemented responses.

- [ ] **Step 2: Implement**

Append to `helper_handler.go` (add imports `crypto/ed25519`, `github.com/google/uuid`, `github.com/jackc/pgx/v5`, `github.com/getlantern/lantern-cloud/cmd/api/pairing`):

```go
var errHelperNoInvite = router.NotFound("no such invite")

// inviterInvite loads an unexpired invite owned by the caller's device.
func (s helperService) inviterInvite(ctx context.Context, deviceID string, hash []byte) (dal.HelperInvite, error) {
	inv, err := dal.New(s.apiDB.GetPool()).GetHelperInviteForInviter(ctx, dal.GetHelperInviteForInviterParams{
		InviteHash: hash, InviterDeviceID: deviceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, errHelperNoInvite
	}
	if err != nil {
		return inv, router.InternalError("internal error")
	}
	return inv, nil
}

func (s helperService) GetInvite(ctx context.Context, req *services.HelperInviteRef) (*services.HelperInviteStatus, error) {
	p, err := helperRequest(ctx, "helperGetInvite")
	if err != nil {
		return nil, err
	}
	inv, err := s.inviterInvite(ctx, p.deviceID, req.InviteHash)
	if err != nil {
		return nil, err
	}
	return &services.HelperInviteStatus{State: inv.State, VolunteerPubkey: inv.VolunteerPubkey, VolunteerBlob: inv.VolunteerBlob}, nil
}

func (s helperService) DecideInvite(ctx context.Context, req *services.HelperDecideInviteRequest) (*services.HelperDecideInviteResponse, error) {
	p, err := helperRequest(ctx, "helperDecideInvite")
	if err != nil {
		return nil, err
	}
	inv, err := s.inviterInvite(ctx, p.deviceID, req.InviteHash)
	if err != nil {
		return nil, err
	}
	if inv.State != "claimed" {
		return nil, &router.HTTPError{Status: http.StatusConflict, Msg: "invite has no pending claim"}
	}
	q := dal.New(s.apiDB.GetPool())
	if !req.Approve {
		if err := q.MarkHelperInviteRejected(ctx, inv.InviteHash); err != nil {
			return nil, router.InternalError("internal error")
		}
		return &services.HelperDecideInviteResponse{}, nil
	}
	if !pairing.VerifyApproval(ed25519.PublicKey(inv.ApprovalPubkey), inv.InviteHash, inv.VolunteerPubkey, req.ApprovalSig) {
		return nil, router.Forbidden("approval signature does not match")
	}
	n, err := q.CountHelperPairingsForInviter(ctx, p.deviceID)
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	if n >= helperMaxInviterPairings {
		return nil, &router.HTTPError{Status: http.StatusTooManyRequests, Msg: "too many pairings"}
	}
	id := uuid.New()
	if err := q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: id, InviterDeviceID: p.deviceID,
		VolunteerDeviceID: inv.VolunteerDeviceID.String, InviterBlob: req.InviterBlob,
	}); err != nil {
		return nil, router.InternalError("internal error")
	}
	if err := q.DeleteHelperInvite(ctx, inv.InviteHash); err != nil {
		return nil, router.InternalError("internal error")
	}
	return &services.HelperDecideInviteResponse{PairingId: id.String()}, nil
}
```

`VolunteerDeviceID` is a nullable column, so sqlc generates `pgtype.Text`; adjust `.String` if your sqlc config maps it to `*string`. Run the pairing insert and the invite update in one transaction (`peer_handler.go` shows the pattern), and consume the invite with a conditional update so only one approval can create a pairing. The shipped handler does both, and a retry gets a 409.

- [ ] **Step 3: Run the tests**

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelper' -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/helper_handler.go cmd/api/helper_handler_db_test.go
git commit -m "api: inviter reviews a claim and approves with a signature or rejects"
```

---

### Task 7: List and end pairings

**Files:**
- Modify: `cmd/api/helper_handler.go`
- Modify: `cmd/api/helper_handler_db_test.go`

**Interfaces:**
- Consumes: `dal.ListHelperPairingsForDevice`, `dal.TakeHelperPairingBlob`, `dal.DeleteHelperPairingForDevice` (Task 3); `createAndClaim`, `approveBody` (Task 6 tests).
- Produces: real `ListPairings` (replacing the Task 4 stub), `EndPairing`; test helper `approvedPairing(t, mux) string`.

- [ ] **Step 1: Write the failing tests**

```go
// approvedPairing returns the ID of a fresh inviter/volunteer pairing.
func approvedPairing(t *testing.T, mux http.Handler) string {
	t.Helper()
	s, vol := createAndClaim(t, mux)
	w := helperPost(t, mux, "/v1/helper/invites/decide", "inviter", approveBody(s, vol), true)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct{ PairingID string `json:"pairing_id"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.PairingID
}

type pairingListJSON struct {
	Pairings []struct {
		PairingID   string `json:"pairing_id"`
		Role        string `json:"role"`
		InviterBlob []byte `json:"inviter_blob"`
	} `json:"pairings"`
}

func listPairings(t *testing.T, mux http.Handler, device string) pairingListJSON {
	t.Helper()
	w := helperPost(t, mux, "/v1/helper/pairings", device, map[string]any{}, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var l pairingListJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &l))
	return l
}

func TestHelperListPairingsBothSides(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	id := approvedPairing(t, mux)

	inv := listPairings(t, mux, "inviter")
	require.Len(t, inv.Pairings, 1)
	assert.Equal(t, id, inv.Pairings[0].PairingID)
	assert.Equal(t, "inviter", inv.Pairings[0].Role)
	assert.Empty(t, inv.Pairings[0].InviterBlob, "the inviter never gets its own blob")

	first := listPairings(t, mux, "volunteer")
	require.Len(t, first.Pairings, 1)
	assert.Equal(t, "volunteer", first.Pairings[0].Role)
	assert.Equal(t, []byte("opaque-inviter-name"), first.Pairings[0].InviterBlob)
	second := listPairings(t, mux, "volunteer")
	assert.Empty(t, second.Pairings[0].InviterBlob, "the blob is delivered once, then deleted")
}

func TestHelperEitherSideEndsPairing(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	id := approvedPairing(t, mux)

	w := helperPost(t, mux, "/v1/helper/pairings/end", "stranger", map[string]any{"pairing_id": id}, true)
	assert.Equal(t, http.StatusNotFound, w.Code, "a non-member can't end it")
	w = helperPost(t, mux, "/v1/helper/pairings/end", "volunteer", map[string]any{"pairing_id": id}, true)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Empty(t, listPairings(t, mux, "inviter").Pairings)
}
```

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelperListPairings|TestHelperEitherSide' -v`
Expected: FAIL; the stub returns an empty list and `EndPairing` is Unimplemented.

- [ ] **Step 2: Implement**

Replace the Task 4 `ListPairings` stub and add `EndPairing`:

```go
func (s helperService) ListPairings(ctx context.Context, _ *emptypb.Empty) (*services.HelperPairingList, error) {
	p, err := helperRequest(ctx, "helperListPairings")
	if err != nil {
		return nil, err
	}
	q := dal.New(s.apiDB.GetPool())
	rows, err := q.ListHelperPairingsForDevice(ctx, p.deviceID)
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	out := &services.HelperPairingList{}
	for _, r := range rows {
		hp := &services.HelperPairing{PairingId: r.PairingID.String(), Role: string(pairing.RoleInviter)}
		if r.VolunteerDeviceID == p.deviceID {
			hp.Role = string(pairing.RoleVolunteer)
			if len(r.InviterBlob) > 0 {
				blob, err := q.TakeHelperPairingBlob(ctx, dal.TakeHelperPairingBlobParams{
					PairingID: r.PairingID, VolunteerDeviceID: p.deviceID})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, router.InternalError("internal error")
				}
				hp.InviterBlob = blob
			}
		}
		out.Pairings = append(out.Pairings, hp)
	}
	return out, nil
}

func (s helperService) EndPairing(ctx context.Context, req *services.HelperPairingRef) (*emptypb.Empty, error) {
	p, err := helperRequest(ctx, "helperEndPairing")
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.PairingId)
	if err != nil {
		return nil, router.BadRequest("invalid pairing_id")
	}
	n, err := dal.New(s.apiDB.GetPool()).DeleteHelperPairingForDevice(ctx, dal.DeleteHelperPairingForDeviceParams{
		PairingID: id, DeviceID: p.deviceID})
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	if n == 0 {
		return nil, router.NotFound("no such pairing")
	}
	return &emptypb.Empty{}, nil
}
```

- [ ] **Step 3: Run the tests**

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelper' -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/api/helper_handler.go cmd/api/helper_handler_db_test.go
git commit -m "api: list and end helper pairings"
```

---

### Task 8: Tickets, the ticket key, and the invite sweep

**Files:**
- Modify: `cmd/api/helper_handler.go`
- Modify: `cmd/api/helper_handler_db_test.go`
- Modify: `cmd/api/main.go` (load the key from Vault; start the sweep)

**Interfaces:**
- Consumes: `pairing.SignTicket`, `pairing.VerifyTicket`, `pairing.TicketLifetime`, `pairing.Ticket`, `pairing.Role*` (Task 2); `dal.ListHelperPairingsForDevice`, `dal.DeleteExpiredHelperInvites` (Task 3); `approvedPairing` (Task 7 tests).
- Produces: `IssueTickets`, `TicketKey`, `func loadHelperTicketKey(ctx) ed25519.PrivateKey`, `func runHelperInviteSweep(ctx context.Context, q *dal.Queries, every time.Duration)`. Vault path `secret/data/lantern_cloud/helper_tickets`, field `seed` (base64 of 32 bytes).

- [ ] **Step 1: Write the failing tests**

```go
func TestHelperIssueTicketsPerPairingAndRole(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	id := approvedPairing(t, mux)
	pub := ls.helperTicketKey.Public().(ed25519.PublicKey)

	for device, role := range map[string]pairing.Role{"inviter": pairing.RoleInviter, "volunteer": pairing.RoleVolunteer} {
		w := helperPost(t, mux, "/v1/helper/tickets", device, map[string]any{}, true)
		require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
		var resp struct {
			Tickets         []string `json:"tickets"`
			LifetimeSeconds int32    `json:"lifetime_seconds"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Tickets, 1)
		assert.EqualValues(t, pairing.TicketLifetime.Seconds(), resp.LifetimeSeconds)
		tk, err := pairing.VerifyTicket(pub, resp.Tickets[0], time.Now())
		require.NoError(t, err)
		assert.Equal(t, id, tk.PairingID.String())
		assert.Equal(t, device, tk.DeviceID)
		assert.Equal(t, role, tk.Role)
	}
}

func TestHelperNoTicketsAfterEnd(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	id := approvedPairing(t, mux)
	require.Equal(t, http.StatusOK, helperPost(t, mux, "/v1/helper/pairings/end", "inviter", map[string]any{"pairing_id": id}, true).Code)
	w := helperPost(t, mux, "/v1/helper/tickets", "volunteer", map[string]any{}, true)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "tickets\":[\"", "an ended pairing gets no tickets")
}

func TestHelperTicketKeyIsPublic(t *testing.T) {
	ls, _ := newHelperTestService(t)
	mux := newHelperTestRouter(t, ls)
	r := httptest.NewRequest(http.MethodGet, "/v1/helper/ticket-key", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, "no device id or flag needed")
	var resp struct{ PublicKey []byte `json:"public_key"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, []byte(ls.helperTicketKey.Public().(ed25519.PublicKey)), resp.PublicKey)
}
```

Add import `time`.

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelperIssueTickets|TestHelperNoTickets|TestHelperTicketKey' -v`
Expected: FAIL with Unimplemented.

- [ ] **Step 2: Implement the endpoints**

Append to `helper_handler.go`:

```go
func (s helperService) IssueTickets(ctx context.Context, _ *emptypb.Empty) (*services.HelperTickets, error) {
	p, err := helperRequest(ctx, "helperIssueTickets")
	if err != nil {
		return nil, err
	}
	if s.helperTicketKey == nil {
		return nil, &router.HTTPError{Status: http.StatusServiceUnavailable, Msg: "tickets unavailable"}
	}
	rows, err := dal.New(s.apiDB.GetPool()).ListHelperPairingsForDevice(ctx, p.deviceID)
	if err != nil {
		return nil, router.InternalError("internal error")
	}
	exp := time.Now().Add(pairing.TicketLifetime)
	out := &services.HelperTickets{LifetimeSeconds: int32(pairing.TicketLifetime.Seconds())}
	for _, r := range rows {
		role := pairing.RoleInviter
		if r.VolunteerDeviceID == p.deviceID {
			role = pairing.RoleVolunteer
		}
		tok, err := pairing.SignTicket(s.helperTicketKey, pairing.Ticket{
			Version: 1, PairingID: r.PairingID, DeviceID: p.deviceID, Role: role, Expires: exp})
		if err != nil {
			return nil, router.InternalError("internal error")
		}
		out.Tickets = append(out.Tickets, tok)
	}
	return out, nil
}

// TicketKey needs no device ID or flag: the public key is not a secret, and
// freddie fetches it before any client is involved.
func (s helperService) TicketKey(context.Context, *emptypb.Empty) (*services.HelperTicketKey, error) {
	if s.helperTicketKey == nil {
		return nil, &router.HTTPError{Status: http.StatusServiceUnavailable, Msg: "tickets unavailable"}
	}
	return &services.HelperTicketKey{PublicKey: s.helperTicketKey.Public().(ed25519.PublicKey)}, nil
}

// runHelperInviteSweep deletes expired invites so neither their hashes nor
// their volunteer blobs outlive the 48-hour window.
func runHelperInviteSweep(ctx context.Context, q *dal.Queries, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := q.DeleteExpiredHelperInvites(ctx); err != nil {
				log.Error(ctx, "helper invite sweep failed", err, nil)
			}
		}
	}
}
```

Add the `log` import using the same path `peer_handler.go` uses.

- [ ] **Step 3: Load the key and start the sweep in `main.go`**

Next to the bandit report key loading:

```go
	ls.helperTicketKey = loadHelperTicketKey(ctx)
```

and next to the peer reaper start:

```go
		go runHelperInviteSweep(ctx, dal.New(ls.apiDB.GetPool()), time.Hour)
```

with, in `helper_handler.go`:

```go
// loadHelperTicketKey reads the ticket signing seed from Vault. When it is
// absent, ticket issuance is disabled rather than fatal.
func loadHelperTicketKey(ctx context.Context) ed25519.PrivateKey {
	var secret struct {
		Seed string `json:"seed"`
	}
	if err := vault.ReadKV(ctx, "secret/data/lantern_cloud/helper_tickets", &secret); err != nil {
		log.WarnErr(ctx, "helper ticket key not configured, helper tickets disabled", err, nil)
		return nil
	}
	seed, err := base64.StdEncoding.DecodeString(secret.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		log.Error(ctx, "helper ticket key is malformed, helper tickets disabled", err, nil)
		return nil
	}
	return ed25519.NewKeyFromSeed(seed)
}
```

Add imports `encoding/base64` and the `vault` package path from `main.go`.

- [ ] **Step 4: Run all helper and pairing tests**

Run: `bin/testsetup go test -tags=test ./cmd/api/ -run 'TestHelper' -v && cd cmd/api && go test ./pairing/ -v`
Expected: PASS.

- [ ] **Step 5: Provision the key in Vault (production)**

Generate a seed once and store it:

```bash
seed=$(head -c 32 /dev/urandom | base64)
bin/vault kv put secret/lantern_cloud/helper_tickets seed="$seed"
```

Keep the seed out of shell history and logs. Rotating it invalidates outstanding tickets within one ticket lifetime, which is safe.

- [ ] **Step 6: Commit**

```bash
git add cmd/api/helper_handler.go cmd/api/helper_handler_db_test.go cmd/api/main.go
git commit -m "api: helper pairing tickets, public ticket key, and invite sweep"
```

---

### Task 9: Full-suite check and telemetry audit

**Files:**
- Modify: none expected

- [ ] **Step 1: Run the repo's full test target**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 2: Lint**

Run: `golangci-lint run --new-from-rev=origin/main ./cmd/api/...`
Expected: no new issues.

- [ ] **Step 3: Telemetry audit**

Run: `grep -nE 'attribute\.|log\.(Info|Debug|Error|WarnErr)' cmd/api/helper_handler.go`
Expected: no line records a pairing ID, device ID, invite hash or IP. If one does, remove that field, rerun the tests, and commit with `api: keep helper identifiers out of telemetry`.
