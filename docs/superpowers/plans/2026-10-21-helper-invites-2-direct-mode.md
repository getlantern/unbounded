# Helper Invites, Plan 2: Direct-Mode Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an approved pairing's inviter reach her volunteer's residential peer route directly. Her `/v1/config-new` carries a dedicated outbound to each paired volunteer's live route, authenticated with a per-pairing samizdat short ID that rotates every 15 minutes. The volunteer's inbound accepts exactly the current pairing IDs. A volunteer can turn off "help others too" so the route serves only its pairings.

**Architecture:** lantern-cloud derives each pairing's short ID with an HMAC keyed by `K_sid`, which comes from the plan 1 ticket seed. It stores nothing new per pairing: the short ID is recomputed on demand. The volunteer learns its pairings' accepted IDs from the peer register and heartbeat responses. radiance pushes them onto the running inbound through a new lantern-box `Inbound.UpdateShortIDs`, which uses a new samizdat `Server.SetShortIDs`. The launch config's own short ID always stays. `configNew` adds a `helper-<route uuid>` samizdat outbound for each paired volunteer's live route. These outbounds sit outside the bandit, the slot reservation and the connect-config cache. A new `peer_routes.serve_pool` column keeps private routes out of the bandit catalog and out of force_track.

```mermaid
sequenceDiagram
    autonumber
    participant V as Volunteer<br/>radiance peer.go
    participant API as lantern-cloud<br/>peer_handler.go / config.go
    participant I as Inviter<br/>client
    V->>API: POST /v1/peer/register {serve_pool}
    API-->>V: server_config (base short ID) + pairing_short_ids [e-1,e,e+1]
    Note over V: Inbound.UpdateShortIDs(base ∪ pairing)
    loop every 150 s
        V->>API: POST /v1/peer/heartbeat
        API-->>V: pairing_short_ids (an ended pairing is gone)
        Note over V: UpdateShortIDs; samizdat closes revoked sessions
    end
    I->>API: POST /v1/config-new (helper_invites on)
    API-->>I: outbound helper-<route> {short_id = sid(pairing, e)}
    I->>V: samizdat handshake with the pairing short ID
```

**Tech Stack:** Go; samizdat; lantern-box (sing-box adapter); lantern-cloud with PostgreSQL via sqlc v1.30.0 (pgx/v5), protobuf/gRPC with buf, testify and `testdb`; radiance (sing-box `*sbox.Box`, koanf settings).

**Spec:** `docs/superpowers/specs/2026-10-20-helper-invites-design.md` (unbounded repo, branch `docs/helper-invites-spec`), Section 2 "Direct mode" and "Revocation".

**Plan series:** 1 Pairing core (done, lantern-cloud PR #3660) · **2 Direct-mode routing (this plan)** · 3 Unbounded rooms and ticket-key rotation · 4 App · 5 Landing page and rewards.

## Global Constraints

### Wire formats (normative; later plans and the app implement against these)

| Name | Definition |
|---|---|
| `K_sid` | `HKDF-SHA256(ikm = helper ticket seed (32 bytes, Vault secret/data/lantern_cloud/helper_tickets field seed, base64), salt = empty, info = "lantern-helper-invite-v1/peer-sid")`, 32 bytes. There is no new secret. In code it is `pairing.PeerSIDKey(helperTicketKey.Seed())`. If the ticket key is absent, pairing short IDs are off: there are no paired outbounds and the volunteer gets an empty list. Nothing fails. |
| Epoch | `epoch = floor(unix_seconds / 900)`: 15-minute windows, the ticket lifetime. Times before 1970 are epoch 0. |
| Pairing short ID | `sid = HMAC-SHA256(K_sid, "lantern-helper-peer-sid-v1" ‖ pairing_id (16 raw UUID bytes) ‖ uint64be(epoch))[:8]`. On the wire it is 16 lowercase hex characters. |
| Accepted epochs | A volunteer's inbound accepts the IDs of epochs `e-1`, `e` and `e+1`, in that order, with `e` computed on lantern-cloud's clock at heartbeat time. At epoch 0 it accepts only `e` and `e+1`. The inviter is handed the ID of epoch `e`, computed at config-fetch time. `e+1` covers the up-to-150 s gap between a volunteer's heartbeat and an inviter's fetch, and `e-1` covers an inviter still using an ID fetched in the previous epoch. Her 10-minute config poll refreshes the ID long before the ID drops out 15 to 30 minutes after the fetch. |
| `pairing_short_ids` | `repeated string pairing_short_ids` on `PeerRegisterResponse` (field 4) and on the new `PeerHeartbeatResponse` (field 1). It holds every pairing in which the requesting device is the volunteer, three IDs per pairing in accepted-epoch order, pairings ordered by `created_at`. If the list is empty, the volunteer has no pairings or `K_sid` is absent. The volunteer replaces its pairing set with each response. |
| Volunteer inbound set | `base ∪ pairing_short_ids`, base first, with invalid and duplicate pairing IDs dropped. `base` is the inbound's `short_ids` from the launch config the register response carried. It is never removed, because lantern-cloud's verifier and pool clients use it. |
| `serve_pool` | `optional bool serve_pool = 4` on `PeerRegisterRequest`. If the field is absent, it is true, so clients that predate it are unchanged. It is stored in `peer_routes.serve_pool BOOLEAN NOT NULL DEFAULT true`. A route with `serve_pool = false` is left out of `GetAllPeerRoutesForTrack`, which covers both the bandit catalog and force_track, and out of `GetPeerRouteConfigsForRoutes`, the assignment-time hydration. Its base short ID therefore never reaches a pool client. Re-registering the same (address, port) overwrites it. |
| Paired outbound | sing-box outbound `{"type":"samizdat","tag":"helper-<peer_route uuid>", ...}`. It is the pool's samizdat connect config (`samizdatOutboundOptions`) with `short_id` set to the pairing's epoch-`e` ID and `server_port` set to `peer_routes.port`, the external port. |
| Live route | `deprecated IS NULL AND callback_verified IS NOT NULL AND expiration > now()`. If a volunteer has several live routes, the most recent by `registered_at` is used. |

**Port decision (resolved from the code map).** `Register` stores `peer_routes.port = external_port` and `ProxyLaunchConfig.ListenPort = internal_port` when the client sends one, which radiance always does (`peer.go` Start: `InternalPort: mapping.InternalPort`, a random 30000–50000 port that UPnP may map to a different external port). The verifier dials `peer_routes.port` (`verifyPeerWithRetries(..., row.Port, ...)`). The pool's `pcfg.ConnectConfig` uses `launchCfg.ListenPort`, the internal port. The paired outbound uses `peer_routes.port`, because that is the port verification proved reachable. This plan does not change the pool path. Its use of the internal port is a pre-existing divergence that only matters when UPnP picks a different external port. It is flagged in the self-review.

### Rules

- **Dependency bumps:** after every `go get module@version`, run `go mod tidy`, check `git diff` for unexpected changes, and commit `go.mod` and `go.sum` together in the same commit as the bump.
- **sqlc (lantern-cloud):** after any query or migration change, run `cd cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`, not `make sqlc` (which installs `@latest`). Commit the generated `cmd/api/dal/` files with the change.
- **proto (lantern-cloud):** after editing `peer.proto`, run `make proto`, then revert every generated file that does not belong to `peer.proto`. `make proto` rewrites unrelated swagger files. Keep only `cmd/api/apipb/services/peer.pb.go`, `peer_grpc.pb.go`, `cmd/backoffice/src/gen/services/peer_pb.ts`, `peer-PeerService_connectquery.ts` and `openapiv2/services/peer.swagger.json`.
- **Tests (lantern-cloud):** DB-backed tests run as `bin/testsetup go test -tags=test,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme ./cmd/api/ -run '<pattern>' -v`. This document writes that as `$LC_TEST`, so `$LC_TEST 'Pattern'` means exactly that command with that pattern. Pure packages run with `cd cmd/api && go test ./pairing/ -v`. New test files that use the peer test helpers carry `//go:build !codeanalysis`, like `peer_handler_db_test.go`.
- **Telemetry:** no log line, span attribute or metric may contain a pairing ID or a short ID, base or pairing. The direct-mode code logs fixed messages and errors only. A span already carrying the device ID must not record whether the device has helpers.
- **Paired outbounds** never call `ReservePeerSlot` and never read or write the Redis connect-config cache: their credential is per pairing. Volunteers count paired users in `active_clients`, so paired users crowd out the pool and the pool never crowds out paired users.
- **PRs:** open every PR with the `skills:create-pr` skill. A hook blocks a bare `gh pr create`.
- **[Human]** marks a step that needs a person: merging, anything that tags a release, force-pushing a shared branch, and every production change.

### Repos, branches and worktrees (in execution order)

| Repo | Worktree | Branch | Base |
|---|---|---|---|
| samizdat | `~/go/src/github.com/getlantern/samizdat-shortids` | `fisk/set-short-ids` | `origin/main` (18bd41c; the local `main` is 6 commits behind) |
| lantern-box | `~/go/src/github.com/getlantern/lantern-box-shortids` | `fisk/samizdat-update-short-ids` | `origin/main` (a861756, v0.0.167; the local `main` is at 431b85d) |
| lantern-cloud | `~/go/src/github.com/getlantern/lantern-cloud-helper-direct` | `fisk/helper-direct-mode` | `fisk/helper-invites-core` (plan 1, PR #3660) |
| radiance | `~/go/src/github.com/getlantern/radiance-helper-direct` | `fisk/helper-direct-mode` | `origin/main` (90f5972; the main checkout is on another branch) |

## File Structure

| Repo | File | Responsibility |
|---|---|---|
| samizdat | `server.go` | Atomic short-ID set, `SetShortIDs`, `ShortIDs`, session tracking that closes revoked sessions |
| samizdat | `shortids_test.go` | Add, revoke and empty-set tests over a real client and server |
| lantern-box | `protocol/samizdat/inbound.go` | `decodeShortIDs`, `(*Inbound).UpdateShortIDs` |
| lantern-box | `protocol/samizdat/shortids_test.go` | UpdateShortIDs tests |
| lantern-cloud | `cmd/api/pairing/peersid.go`, `peersid_test.go`, `invite_test.go`, `testdata/vectors.json` | `K_sid`, epoch, short ID, accepted set, golden vectors |
| lantern-cloud | `cmd/api/migrations/infra/000146_peer_routes_serve_pool.{up,down}.sql` | `serve_pool` column |
| lantern-cloud | `cmd/api/queries/infra/peer_routes.sql`, `helper.sql` | serve_pool filters and upsert; `ListHelperRoutesForInviter` |
| lantern-cloud | `cmd/api/apipb/proto/services/peer.proto` | `serve_pool`, `pairing_short_ids`, `PeerHeartbeatResponse` |
| lantern-cloud | `cmd/api/peer_handler.go` | Store serve_pool; return pairing short IDs on register and heartbeat; serve_pool in the arm-transition count |
| lantern-cloud | `cmd/api/helper_direct.go` | Clock, `K_sid` wiring, volunteer short IDs, paired outbounds, pool dedup |
| lantern-cloud | `cmd/api/pcfg/samizdat.go` | `samizdatOutboundOptions` (extracted), `HelperSamizdatOutbound` |
| lantern-cloud | `cmd/api/config.go`, `cmd/api/main.go` | Append paired outbounds in `configNew`; derive `K_sid` at startup |
| lantern-cloud | `cmd/api/helper_direct_db_test.go`, `peer_handler_db_test.go`, `peer_handler_test.go`, `pcfg/samizdat_test.go` | Tests, including the end-to-end test |
| radiance | `peer/api.go` | `ServePool`, `PairingShortIDs`, `HeartbeatResponse`, tolerant decode |
| radiance | `common/settings/settings.go` | `PeerServePoolKey` |
| radiance | `peer/peer.go` | `servePool`, base and pairing short IDs pushed to the inbound on start, heartbeat and rotation |
| radiance | `peer/peer_test.go`, `peer/shortids_test.go` | Tests |

---

### Task 1: samizdat — replace the short-ID set at runtime and revoke sessions

**Repo / worktree:** samizdat, `~/go/src/github.com/getlantern/samizdat-shortids`

**Files:**
- Modify: `server.go`
- Create: `shortids_test.go`

**Interfaces:**
- Consumes: `NewServer`, `ServerConfig`, `VerifySessionID`, `NewClient`, `ClientConfig`, `GenerateKeyPair`, `GenerateShortID`; test helpers `generateSelfSignedCert` and `startTestServer` (integration_test.go).
- Produces:
  - `func (s *Server) SetShortIDs(ids [][8]byte) error`: rejects an empty set, copies `ids`, and closes every authenticated connection whose short ID the new set leaves out.
  - `func (s *Server) ShortIDs() [][8]byte`: returns a copy.

- [ ] **Step 1: Create the worktree**

```bash
git -C ~/go/src/github.com/getlantern/samizdat fetch origin
git -C ~/go/src/github.com/getlantern/samizdat worktree add ~/go/src/github.com/getlantern/samizdat-shortids -b fisk/set-short-ids origin/main
```

- [ ] **Step 2: Write the failing tests**

`shortids_test.go`:

```go
package samizdat

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// startEcho starts a TCP echo server and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// relayHandler dials the CONNECT destination and copies both ways.
func relayHandler(_ context.Context, conn net.Conn, destination string) {
	defer conn.Close()
	target, err := net.DialTimeout("tcp", destination, 5*time.Second)
	if err != nil {
		return
	}
	defer target.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(target, conn) }()
	go func() { defer wg.Done(); io.Copy(conn, target) }()
	wg.Wait()
}

// dialEcho opens a tunnel to echoAddr authenticating as shortID, and proves it
// with one round trip. It returns the open tunnel.
func dialEcho(t *testing.T, serverAddr string, pub []byte, shortID [8]byte, echoAddr string) (net.Conn, error) {
	t.Helper()
	client, err := NewClient(ClientConfig{
		ServerAddr:  serverAddr,
		ServerName:  "test.example.com",
		PublicKey:   pub,
		ShortID:     shortID,
		Fingerprint: "chrome",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", echoAddr)
	if err != nil {
		return nil, err
	}
	msg := []byte("ping")
	if _, err := conn.Write(msg); err != nil {
		conn.Close()
		return nil, err
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Time{})
	return conn, nil
}

func TestSetShortIDsAddsAndRevokes(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	base, _ := GenerateShortID()
	paired, _ := GenerateShortID()
	certPEM, keyPEM := generateSelfSignedCert(t)
	echo := startEcho(t)
	server, ln := startTestServer(t, ServerConfig{
		ListenAddr: "127.0.0.1:0",
		PrivateKey: priv,
		ShortIDs:   [][8]byte{base},
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		Handler:    relayHandler,
	})
	addr := ln.Addr().String()

	if c, err := dialEcho(t, addr, pub, paired, echo); err == nil {
		c.Close()
		t.Fatal("a short ID the server was never given authenticated")
	}

	if err := server.SetShortIDs([][8]byte{base, paired}); err != nil {
		t.Fatalf("SetShortIDs: %v", err)
	}
	held, err := dialEcho(t, addr, pub, paired, echo)
	if err != nil {
		t.Fatalf("an added short ID was rejected: %v", err)
	}
	defer held.Close()

	if err := server.SetShortIDs([][8]byte{base}); err != nil {
		t.Fatalf("SetShortIDs: %v", err)
	}
	if c, err := dialEcho(t, addr, pub, paired, echo); err == nil {
		c.Close()
		t.Fatal("a removed short ID still authenticates")
	}
	c, err := dialEcho(t, addr, pub, base, echo)
	if err != nil {
		t.Fatalf("the remaining short ID was rejected: %v", err)
	}
	c.Close()

	// The session that authenticated with the removed ID was closed, not left
	// to run until it ends on its own.
	held.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = held.Read(make([]byte, 1))
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatalf("a session using a removed short ID survived: %v", err)
	}
}

func TestSetShortIDsRejectsEmptySetAndCopies(t *testing.T) {
	priv, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	a, _ := GenerateShortID()
	b, _ := GenerateShortID()
	certPEM, keyPEM := generateSelfSignedCert(t)
	s, err := NewServer(ServerConfig{PrivateKey: priv, ShortIDs: [][8]byte{a}, CertPEM: certPEM, KeyPEM: keyPEM, Handler: relayHandler})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := s.SetShortIDs(nil); err == nil {
		t.Fatal("an empty set must be refused")
	}
	if got := s.ShortIDs(); len(got) != 1 || got[0] != a {
		t.Fatalf("a refused update changed the set: %v", got)
	}
	ids := [][8]byte{b}
	if err := s.SetShortIDs(ids); err != nil {
		t.Fatalf("SetShortIDs: %v", err)
	}
	ids[0] = a
	if got := s.ShortIDs(); got[0] != b {
		t.Fatal("SetShortIDs must copy its argument")
	}
	got := s.ShortIDs()
	got[0] = a
	if s.ShortIDs()[0] != b {
		t.Fatal("ShortIDs must return a copy")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/samizdat-shortids && go test -race -run 'TestSetShortIDs' -v .`
Expected: the build fails with `server.SetShortIDs undefined` and `s.ShortIDs undefined`.

- [ ] **Step 4: Implement**

In `server.go`, add `"slices"` to the imports and replace the `Server` struct:

```go
type Server struct {
	config       ServerConfig
	serverPubKey []byte // derived from config.PrivateKey
	// shortIDs is the set a client may authenticate with. It starts as
	// config.ShortIDs; SetShortIDs replaces it while the server runs.
	shortIDs   atomic.Pointer[[][8]byte]
	sessionsMu sync.Mutex
	// sessions maps each authenticated connection to the short ID it
	// authenticated with, so SetShortIDs can close the ones it revokes.
	sessions   map[net.Conn][8]byte
	listenerMu sync.Mutex
	listener   net.Listener
	masquerade *Masquerade
	ctx        context.Context
	cancel     context.CancelCauseFunc
	wg         sync.WaitGroup
}
```

In `NewServer`, replace the `s := &Server{...}` literal with:

```go
	s := &Server{
		config:       config,
		serverPubKey: serverPubKey,
		sessions:     make(map[net.Conn][8]byte),
		ctx:          ctx,
		cancel:       cancel,
	}
	ids := append([][8]byte(nil), config.ShortIDs...)
	s.shortIDs.Store(&ids)
```

In `handleConnection`, replace the verify-and-dispatch block:

```go
	// Verify Samizdat auth using PSK derived from server public key
	shortID, authenticated, err := VerifySessionID(sessionID, s.serverPubKey, *s.shortIDs.Load())
	if err != nil || !authenticated {
		// Auth failed — enter masquerade mode
		s.doMasquerade(conn, clientHelloRecord)
		return
	}

	// Auth passed — complete TLS handshake and enter H2 proxy mode
	s.handleAuthenticated(conn, clientHelloRecord, shortID)
```

Change `handleAuthenticated`'s signature and open it with the session registration:

```go
// handleAuthenticated completes the TLS handshake with the authenticated
// client and serves HTTP/2 CONNECT requests.
func (s *Server) handleAuthenticated(conn net.Conn, clientHelloRecord []byte, shortID [8]byte) {
	// Registered before the handshake: a SetShortIDs that revokes shortID
	// from here on closes conn, and one that already did refuses it.
	if !s.trackSession(conn, shortID) {
		return
	}
	defer s.untrackSession(conn)

	// Create a connection that replays the ClientHello record first,
	// then reads from the real connection
	replayConn := newReplayConn(conn, clientHelloRecord)
```

The rest of `handleAuthenticated` is unchanged. Add after `Addr`:

```go
// SetShortIDs replaces the short IDs clients may authenticate with, while the
// server runs. A connection that authenticated with an ID the new set leaves
// out is closed, so removing an ID revokes it at once rather than whenever
// its sessions end. As in NewServer the set must not be empty. ids is copied.
func (s *Server) SetShortIDs(ids [][8]byte) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one ShortID is required")
	}
	next := append([][8]byte(nil), ids...)
	s.sessionsMu.Lock()
	s.shortIDs.Store(&next)
	var revoked []net.Conn
	for conn, id := range s.sessions {
		if !slices.Contains(next, id) {
			revoked = append(revoked, conn)
			delete(s.sessions, conn)
		}
	}
	s.sessionsMu.Unlock()
	for _, conn := range revoked {
		conn.Close()
	}
	return nil
}

// ShortIDs returns a copy of the short IDs clients may authenticate with.
func (s *Server) ShortIDs() [][8]byte {
	return append([][8]byte(nil), *s.shortIDs.Load()...)
}

// trackSession records conn as authenticated with id, unless id has been
// revoked since it was verified. The check and the record share
// SetShortIDs' lock, so no revocation falls between them.
func (s *Server) trackSession(conn net.Conn, id [8]byte) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if !slices.Contains(*s.shortIDs.Load(), id) {
		return false
	}
	s.sessions[conn] = id
	return true
}

func (s *Server) untrackSession(conn net.Conn) {
	s.sessionsMu.Lock()
	delete(s.sessions, conn)
	s.sessionsMu.Unlock()
}
```

`config.ShortIDs` is now read only in `NewServer`. Check that nothing else reads it:

Run: `grep -n 'config.ShortIDs' *.go`
Expected: only the two lines in `NewServer` (the length check and the copy).

- [ ] **Step 5: Run the tests**

Run: `cd ~/go/src/github.com/getlantern/samizdat-shortids && go test -race -run 'TestSetShortIDs' -v . && go test -race ./...`
Expected: PASS for both new tests and the whole package, with no race reports.

- [ ] **Step 6: Commit and open the PR**

```bash
git add server.go shortids_test.go
git commit -m "Replace the server's short IDs at runtime and close revoked sessions"
```

Open the PR with `skills:create-pr`.

- [ ] **Step 7: [Human] Merge the PR to `main`.** samizdat is consumed by pseudo-version, so it needs no tag. Record the merge commit, because Task 2 pins it.

---

### Task 2: lantern-box — `Inbound.UpdateShortIDs`

**Repo / worktree:** lantern-box, `~/go/src/github.com/getlantern/lantern-box-shortids`

**Files:**
- Modify: `go.mod`, `go.sum` (samizdat bump)
- Modify: `protocol/samizdat/inbound.go`
- Create: `protocol/samizdat/shortids_test.go`

**Interfaces:**
- Consumes: `samizdat.Server.SetShortIDs` and `ShortIDs` (Task 1); test helpers `testCertKeyPEM`, `testPrivKeyHex` and `testShortIDHex` (samizdat_test.go).
- Produces:
  - `func (i *Inbound) UpdateShortIDs(ids []string) error`: validates 16-hex IDs and rejects an empty set. On error the accepted set is unchanged.
  - `func decodeShortIDs(ids []string) ([][8]byte, error)`, shared with `NewInbound` and keeping its error messages.

- [ ] **Step 1: Create the worktree and bump samizdat**

```bash
git -C ~/go/src/github.com/getlantern/lantern-box fetch origin
git -C ~/go/src/github.com/getlantern/lantern-box worktree add ~/go/src/github.com/getlantern/lantern-box-shortids -b fisk/samizdat-update-short-ids origin/main
cd ~/go/src/github.com/getlantern/lantern-box-shortids
go get github.com/getlantern/samizdat@main
go mod tidy
git diff --stat
```

Expected: the diff touches only `go.mod` and `go.sum`, and `github.com/getlantern/samizdat` moves from `v0.0.3-0.20261019142234-18bd41c612f1` to the pseudo-version of Task 1's merge commit.

```bash
go build ./... && git add go.mod go.sum && git commit -m "Bump samizdat for runtime short-ID updates"
```

- [ ] **Step 2: Write the failing tests**

`protocol/samizdat/shortids_test.go`:

```go
package samizdat

import (
	"context"
	"encoding/hex"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	samizdat "github.com/getlantern/samizdat"
)

// newTestInbound returns an Inbound around a samizdat server that accepts one
// short ID, and that ID. It does not listen.
func newTestInbound(t *testing.T) (*Inbound, [8]byte) {
	t.Helper()
	cert, key := testCertKeyPEM(t)
	priv, err := hex.DecodeString(testPrivKeyHex(t))
	require.NoError(t, err)
	var base [8]byte
	b, err := hex.DecodeString(testShortIDHex(t))
	require.NoError(t, err)
	copy(base[:], b)
	srv, err := samizdat.NewServer(samizdat.ServerConfig{
		PrivateKey: priv,
		ShortIDs:   [][8]byte{base},
		CertPEM:    []byte(cert),
		KeyPEM:     []byte(key),
		Handler:    func(context.Context, net.Conn, string) {},
	})
	require.NoError(t, err)
	return &Inbound{server: srv}, base
}

func TestInbound_UpdateShortIDs(t *testing.T) {
	ib, base := newTestInbound(t)
	baseHex := hex.EncodeToString(base[:])
	paired := testShortIDHex(t)

	require.NoError(t, ib.UpdateShortIDs([]string{baseHex, paired}))
	got := ib.server.ShortIDs()
	require.Len(t, got, 2)
	assert.Equal(t, base, got[0])
	assert.Equal(t, paired, hex.EncodeToString(got[1][:]))

	require.NoError(t, ib.UpdateShortIDs([]string{baseHex}))
	assert.Equal(t, [][8]byte{base}, ib.server.ShortIDs())
}

func TestInbound_UpdateShortIDsRejectsBadSets(t *testing.T) {
	ib, base := newTestInbound(t)
	for name, ids := range map[string][]string{
		"nil":       nil,
		"empty":     {},
		"not hex":   {"zz" + strings.Repeat("00", 7)},
		"too short": {"0011"},
		"one bad":   {hex.EncodeToString(base[:]), "bad"},
	} {
		err := ib.UpdateShortIDs(ids)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "short_ids", name)
		assert.Equal(t, [][8]byte{base}, ib.server.ShortIDs(), "%s: a rejected update leaves the set unchanged", name)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/lantern-box-shortids && go test ./protocol/samizdat/ -run 'UpdateShortIDs' -v`
Expected: the build fails with `ib.UpdateShortIDs undefined (type *Inbound has no field or method UpdateShortIDs)`.

- [ ] **Step 4: Implement**

In `NewInbound`, replace the short-ID block (from `if len(options.ShortIDs) < 1 {` through the end of the decode loop) with:

```go
	shortIDs, err := decodeShortIDs(options.ShortIDs)
	if err != nil {
		return nil, err
	}
```

Append to `inbound.go`:

```go
// UpdateShortIDs replaces the short IDs the inbound accepts while it runs.
// Connections that authenticated with an ID the new set leaves out are
// closed. ids takes the form of the short_ids option and, like it, must not
// be empty. On error the accepted set is unchanged.
func (i *Inbound) UpdateShortIDs(ids []string) error {
	shortIDs, err := decodeShortIDs(ids)
	if err != nil {
		return err
	}
	return i.server.SetShortIDs(shortIDs)
}

// decodeShortIDs parses short IDs of 16 hex characters each.
func decodeShortIDs(ids []string) ([][8]byte, error) {
	if len(ids) < 1 {
		return nil, fmt.Errorf("short_ids must contain at least one element")
	}
	out := make([][8]byte, len(ids))
	for i, sid := range ids {
		b, err := hex.DecodeString(sid)
		if err != nil || len(b) != 8 {
			return nil, fmt.Errorf("short_ids[%d] must be 16 hex characters (8 bytes)", i)
		}
		copy(out[i][:], b)
	}
	return out, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `cd ~/go/src/github.com/getlantern/lantern-box-shortids && go test ./protocol/samizdat/ -v && make test`
Expected: PASS, including the existing `TestNewInbound_InvalidShortIDs`, whose messages are unchanged.

- [ ] **Step 6: Commit and open the PR**

```bash
git add protocol/samizdat/inbound.go protocol/samizdat/shortids_test.go
git commit -m "Let the samizdat inbound replace its short IDs at runtime"
```

Open the PR with `skills:create-pr`.

- [ ] **Step 7: [Human] Merge the PR and record the release tag.** `.github/workflows/auto-tag.yaml` tags a patch release on every merge to `main` that touches Go files. The tag then builds and publishes the lantern-box release images, so the merge is a release. Record the tag for Task 10:

Run: `git -C ~/go/src/github.com/getlantern/lantern-box fetch --tags origin && git -C ~/go/src/github.com/getlantern/lantern-box tag -l 'v*' --sort=-v:refname | head -1`
Expected: `v0.0.168`, unless another merge landed first. Use whatever tag this prints as `LB_TAG` in Task 10.

---

### Task 3: lantern-cloud — pairing short-ID derivation and vectors

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Create: `cmd/api/pairing/peersid.go`
- Create: `cmd/api/pairing/peersid_test.go`
- Modify: `cmd/api/pairing/invite_test.go` (`TestVectors`)
- Modify: `cmd/api/pairing/testdata/vectors.json` (regenerated)

**Interfaces:**
- Consumes: `expand`, `TicketLifetime` and `testTicketKey` (plan 1).
- Produces:
  - `const PeerSIDEpochSeconds int64 = 900`
  - `func PeerSIDKey(ticketSeed []byte) []byte`
  - `func PeerSIDEpoch(t time.Time) uint64`
  - `func PeerShortID(kSID []byte, pairingID uuid.UUID, epoch uint64) [8]byte`
  - `func AcceptedPeerShortIDs(kSID []byte, pairingID uuid.UUID, now time.Time) [][8]byte`

- [ ] **Step 1: Create the worktree, stacked on plan 1**

PR #3660's branch was rebased onto main and force-pushed on 2026-10-21 (head d4de5a10a or later). A stacked PR must sit on what is pushed, so check that the local branch matches origin:

Run: `git -C ~/go/src/github.com/getlantern/lantern-cloud fetch origin && git -C ~/go/src/github.com/getlantern/lantern-cloud rev-list --left-right --count fisk/helper-invites-core...origin/fisk/helper-invites-core`

Expected: `0	0`. If the local branch is behind, fast-forward it. If it has diverged, stop and ask, because PR #3660 changed after this plan was written.

```bash
git -C ~/go/src/github.com/getlantern/lantern-cloud fetch origin
git -C ~/go/src/github.com/getlantern/lantern-cloud worktree add ~/go/src/github.com/getlantern/lantern-cloud-helper-direct -b fisk/helper-direct-mode origin/fisk/helper-invites-core
```

- [ ] **Step 2: Write the failing tests**

`cmd/api/pairing/peersid_test.go`:

```go
package pairing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func testPeerSIDKey() []byte { return PeerSIDKey(bytes.Repeat([]byte{9}, 32)) }

// TestPeerShortIDMatchesTheWireFormat recomputes the HMAC by hand, so a change
// to PeerShortID that drifts from the published formula fails here and not
// only in the vectors.
func TestPeerShortIDMatchesTheWireFormat(t *testing.T) {
	k := testPeerSIDKey()
	id := uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab")
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte("lantern-helper-peer-sid-v1"))
	mac.Write(id[:])
	mac.Write([]byte{0, 0, 0, 0, 0, 0x1e, 0x84, 0x80}) // uint64be(2_000_000)
	got := PeerShortID(k, id, 2_000_000)
	assert.Equal(t, mac.Sum(nil)[:8], got[:])
}

func TestPeerSIDKeyIsDomainSeparated(t *testing.T) {
	seed := bytes.Repeat([]byte{9}, 32)
	assert.Len(t, PeerSIDKey(seed), 32)
	assert.Equal(t, PeerSIDKey(seed), PeerSIDKey(seed))
	assert.NotEqual(t, PeerSIDKey(seed), DeriveKey(seed), "K_sid must differ from every invite key")
	assert.NotEqual(t, PeerSIDKey(seed), seed)
}

func TestPeerSIDEpochBoundaries(t *testing.T) {
	assert.Equal(t, uint64(0), PeerSIDEpoch(time.Unix(899, 0)))
	assert.Equal(t, uint64(1), PeerSIDEpoch(time.Unix(900, 0)))
	assert.Equal(t, uint64(2_000_000), PeerSIDEpoch(time.Unix(1_800_000_899, 0)))
	assert.Equal(t, uint64(0), PeerSIDEpoch(time.Unix(-5, 0)), "pre-1970 clamps to epoch 0")
	assert.EqualValues(t, 900, PeerSIDEpochSeconds)
}

func TestAcceptedPeerShortIDs(t *testing.T) {
	k := testPeerSIDKey()
	id := uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab")
	now := time.Unix(1_800_000_450, 0)
	got := AcceptedPeerShortIDs(k, id, now)
	assert.Equal(t, [][8]byte{
		PeerShortID(k, id, 1_999_999),
		PeerShortID(k, id, 2_000_000),
		PeerShortID(k, id, 2_000_001),
	}, got, "epochs e-1, e, e+1 in that order")
	assert.NotEqual(t, got[0], got[1], "IDs rotate per epoch")
	other := AcceptedPeerShortIDs(k, uuid.MustParse("00000000-0000-0000-0000-000000000001"), now)
	assert.NotEqual(t, got[1], other[1], "IDs are per pairing")
	otherKey := AcceptedPeerShortIDs(PeerSIDKey(bytes.Repeat([]byte{8}, 32)), id, now)
	assert.NotEqual(t, got[1], otherKey[1], "IDs are per K_sid")
	assert.Len(t, AcceptedPeerShortIDs(k, id, time.Unix(10, 0)), 2, "epoch 0 has no predecessor")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct/cmd/api && go test ./pairing/ -run 'PeerS|AcceptedPeer' -v`
Expected: the build fails with `undefined: PeerSIDKey`.

- [ ] **Step 4: Implement**

`cmd/api/pairing/peersid.go`:

```go
package pairing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
)

// PeerSIDEpochSeconds is how long a pairing's samizdat short ID lasts: the
// ticket lifetime, 15 minutes.
const PeerSIDEpochSeconds = int64(TicketLifetime / time.Second)

const (
	infoPeerSID   = "lantern-helper-invite-v1/peer-sid"
	peerSIDDomain = "lantern-helper-peer-sid-v1"
)

// PeerSIDKey is K_sid, the key pairing short IDs are derived with. It comes
// from the helper ticket seed, so direct mode needs no secret of its own.
func PeerSIDKey(ticketSeed []byte) []byte { return expand(ticketSeed, infoPeerSID) }

// PeerSIDEpoch is the epoch t falls in: floor(unix seconds / 900).
func PeerSIDEpoch(t time.Time) uint64 {
	s := t.Unix()
	if s < 0 {
		return 0
	}
	return uint64(s / PeerSIDEpochSeconds)
}

// PeerShortID is pairingID's samizdat short ID in epoch:
// HMAC-SHA256(kSID, "lantern-helper-peer-sid-v1" ‖ pairing ID ‖ uint64be(epoch))[:8].
func PeerShortID(kSID []byte, pairingID uuid.UUID, epoch uint64) [8]byte {
	mac := hmac.New(sha256.New, kSID)
	mac.Write([]byte(peerSIDDomain))
	mac.Write(pairingID[:])
	var e [8]byte
	binary.BigEndian.PutUint64(e[:], epoch)
	mac.Write(e[:])
	var sid [8]byte
	copy(sid[:], mac.Sum(nil))
	return sid
}

// AcceptedPeerShortIDs is what a volunteer's inbound accepts for pairingID at
// now: the IDs of epochs e-1, e and e+1, in that order. e+1 covers an inviter
// who fetches her config in the next epoch before the volunteer's next
// heartbeat; e-1 covers one still using the previous epoch's ID.
func AcceptedPeerShortIDs(kSID []byte, pairingID uuid.UUID, now time.Time) [][8]byte {
	e := PeerSIDEpoch(now)
	out := make([][8]byte, 0, 3)
	if e > 0 {
		out = append(out, PeerShortID(kSID, pairingID, e-1))
	}
	return append(out, PeerShortID(kSID, pairingID, e), PeerShortID(kSID, pairingID, e+1))
}
```

- [ ] **Step 5: Run the tests**

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct/cmd/api && go test ./pairing/ -run 'PeerS|AcceptedPeer' -v`
Expected: PASS for all four tests.

- [ ] **Step 6: Add the golden vectors**

In `invite_test.go`'s `TestVectors`, insert before the `got, err := json.MarshalIndent(...)` line:

```go
	sidKey := PeerSIDKey(tpriv.Seed())
	sidPairing := uuid.MustParse("6f1c2b0e-8a7d-4c1e-9f00-0123456789ab")
	sidNow := time.Unix(1_800_000_450, 0)
	var accepted []string
	for _, sid := range AcceptedPeerShortIDs(sidKey, sidPairing, sidNow) {
		accepted = append(accepted, hex.EncodeToString(sid[:]))
	}
	peerSIDVec := map[string]any{
		"ticket_seed_hex":        hex.EncodeToString(tpriv.Seed()),
		"k_sid_hex":              hex.EncodeToString(sidKey),
		"pairing_id":             sidPairing.String(),
		"now_unix":               sidNow.Unix(),
		"epoch":                  PeerSIDEpoch(sidNow),
		"accepted_short_ids_hex": accepted, // epochs e-1, e, e+1
	}
```

Change the marshal call to:

```go
	got, err := json.MarshalIndent(map[string]any{"invites": vs, "ticket": ticketVec, "peer_sid": peerSIDVec}, "", "  ")
```

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct/cmd/api && go test ./pairing/ -run TestVectors -update && go test ./pairing/ -v`
Expected: `testdata/vectors.json` gains a `peer_sid` object with `"epoch": 2000000` and three 16-character IDs, and the whole package passes. Check the diff: `git diff cmd/api/pairing/testdata/vectors.json` adds lines only.

- [ ] **Step 7: Commit**

```bash
git add cmd/api/pairing/peersid.go cmd/api/pairing/peersid_test.go cmd/api/pairing/invite_test.go cmd/api/pairing/testdata/vectors.json
git commit -m "pairing: per-pairing samizdat short IDs derived from the ticket seed"
```

---

### Task 4: lantern-cloud — `serve_pool` column and the direct-mode queries

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Create: `cmd/api/migrations/infra/000146_peer_routes_serve_pool.up.sql`, `.down.sql`
- Modify: `cmd/api/queries/infra/peer_routes.sql`, `cmd/api/queries/infra/helper.sql`
- Modify: `cmd/api/peer_handler.go` (pass `ServePool: true` for now; the arm-transition count)
- Generated: `cmd/api/dal/peer_routes.sql.go`, `cmd/api/dal/helper.sql.go`, `cmd/api/dal/models.go`
- Create: `cmd/api/helper_direct_db_test.go`

**Interfaces:**
- Consumes: `newPeerTestService`, `peerTrackName`, `bandit.MaxClientsPerPeer`, `dal.InsertHelperPairing` (plan 1).
- Produces:
  - `peer_routes.serve_pool BOOLEAN NOT NULL DEFAULT true`. Migration 000146 is the next free number: `origin/main` ends at 000144 and plan 1 adds 000145.
  - `dal.InsertPeerRouteParams.ServePool bool`
  - `dal.PeerRoute.ServePool bool`
  - `ListHelperRoutesForInviter(ctx, inviterDeviceID string) ([]ListHelperRoutesForInviterRow, error)`, where the row is `{PairingID uuid.UUID; PeerRoute uuid.UUID; Address netip.Addr; Port int32; TrackID int32; CountryCode sql.NullString; LaunchCfg []byte}`
  - Test helpers `peerRow` and `insertPeerRoute(t, tdb, peerRow) uuid.UUID`

- [ ] **Step 1: Write the failing test**

`cmd/api/helper_direct_db_test.go`:

```go
//go:build !codeanalysis

package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/bandit"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/testdb"
)

// peerRow is a peer_routes row for insertPeerRoute. Zero values: launch_cfg
// '{}', address 198.51.100.7, expiring in an hour, registered now,
// unverified, not deprecated, serve_pool false.
type peerRow struct {
	device     string
	addr       string
	port       int32
	track      int
	launch     []byte
	verified   bool
	deprecated bool
	expiresIn  time.Duration
	age        time.Duration
	servePool  bool
}

func insertPeerRoute(t *testing.T, tdb *testdb.TestDB, r peerRow) uuid.UUID {
	t.Helper()
	if r.launch == nil {
		r.launch = []byte("{}")
	}
	if r.addr == "" {
		r.addr = "198.51.100.7"
	}
	if r.expiresIn == 0 {
		r.expiresIn = time.Hour
	}
	var id uuid.UUID
	err := tdb.GetPool().QueryRow(context.Background(), `
		INSERT INTO peer_routes (owner_device_id, address, port, track, launch_cfg, expiration,
		                         registered_at, callback_verified, deprecated, serve_pool)
		VALUES ($1, $2::inet, $3, $4, $5, now() + $6::interval, now() - $7::interval,
		        CASE WHEN $8::bool THEN now() END, CASE WHEN $9::bool THEN now() END, $10)
		RETURNING peer_route`,
		r.device, r.addr, r.port, r.track, r.launch, r.expiresIn, r.age, r.verified, r.deprecated, r.servePool).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestDirectModeQueries(t *testing.T) {
	_, tdb := newPeerTestService(t, &fakePeerVerifier{})
	ctx := context.Background()
	q := dal.New(tdb.GetPool())
	tr, err := q.GetTrackByName(ctx, peerTrackName)
	require.NoError(t, err)
	trackID := int(tr.ID)
	pid := uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: pid, InviterDeviceID: "inviter", VolunteerDeviceID: "volunteer"}))

	older := insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 1001, track: trackID, verified: true, age: 10 * time.Minute, servePool: true})
	newest := insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 1002, track: trackID, verified: true, age: time.Minute})
	insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 1003, track: trackID, servePool: true})
	insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 1004, track: trackID, verified: true, deprecated: true, servePool: true})
	insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 1005, track: trackID, verified: true, expiresIn: -time.Minute, servePool: true})

	rows, err := q.ListHelperRoutesForInviter(ctx, "inviter")
	require.NoError(t, err)
	require.Len(t, rows, 1, "one route per pairing")
	assert.Equal(t, pid, rows[0].PairingID)
	assert.Equal(t, newest, rows[0].PeerRoute, "the most recent live, verified route; serve_pool does not matter")
	assert.Equal(t, int32(1002), rows[0].Port)
	assert.Equal(t, tr.ID, rows[0].TrackID)
	for _, d := range []string{"volunteer", "stranger"} {
		rows, err := q.ListHelperRoutesForInviter(ctx, d)
		require.NoError(t, err)
		assert.Empty(t, rows, "%s is no pairing's inviter", d)
	}

	pool, err := q.GetAllPeerRoutesForTrack(ctx, dal.GetAllPeerRoutesForTrackParams{TrackID: trackID, MaxClients: bandit.MaxClientsPerPeer})
	require.NoError(t, err)
	assert.Equal(t, []string{older.String()}, pool, "a route with serve_pool false is not in the pool")
	cfgs, err := q.GetPeerRouteConfigsForRoutes(ctx, []uuid.UUID{older, newest})
	require.NoError(t, err)
	require.Len(t, cfgs, 1, "nor hydrated for assignment from a stale catalog")
	assert.Equal(t, older, cfgs[0].ID)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `$LC_TEST 'TestDirectModeQueries'`
Expected: the build fails with `q.ListHelperRoutesForInviter undefined`.

- [ ] **Step 3: Write the migration**

`000146_peer_routes_serve_pool.up.sql`:

```sql
-- serve_pool says whether a peer route serves the general pool ("help others
-- too") or only its owner's helper pairings. Existing routes, and peers on
-- clients that predate the field, keep serving the pool.
ALTER TABLE peer_routes ADD COLUMN serve_pool BOOLEAN NOT NULL DEFAULT true;
```

`000146_peer_routes_serve_pool.down.sql`:

```sql
ALTER TABLE peer_routes DROP COLUMN serve_pool;
```

- [ ] **Step 4: Change the queries**

In `peer_routes.sql`, `InsertPeerRoute` becomes:

```sql
-- name: InsertPeerRoute :one
INSERT INTO peer_routes (
    owner_device_id, owner_user_id,
    address, port, region, asn, country_code,
    track, launch_cfg,
    expiration, serve_pool
) VALUES (
    @owner_device_id, sqlc.narg('owner_user_id'),
    @address::inet, @port, sqlc.narg('region'), sqlc.narg('asn'), sqlc.narg('country_code'),
    @track, @launch_cfg,
    @expiration, @serve_pool
)
ON CONFLICT (address, port) WHERE deprecated IS NULL
DO UPDATE SET
    owner_device_id = EXCLUDED.owner_device_id,
    owner_user_id = EXCLUDED.owner_user_id,
    region = EXCLUDED.region,
    asn = EXCLUDED.asn,
    country_code = EXCLUDED.country_code,
    track = EXCLUDED.track,
    launch_cfg = EXCLUDED.launch_cfg,
    serve_pool = EXCLUDED.serve_pool,
    registered_at = now(),
    last_heartbeat = now(),
    expiration = EXCLUDED.expiration,
    callback_verified = NULL,
    callback_failures = 0
RETURNING peer_route, registered_at, expiration;
```

Append one paragraph to the comment above `GetAllPeerRoutesForTrack` and add the filter:

```sql
-- A route with serve_pool false serves only its owner's helper pairings, so
-- it never enters the pool. That also keeps its base short ID away from pool
-- clients.
-- name: GetAllPeerRoutesForTrack :many
SELECT peer_route::text
FROM peer_routes
WHERE track = @track_id
  AND deprecated IS NULL
  AND expiration > now()
  AND callback_verified IS NOT NULL
  AND serve_pool
  AND (active_clients + provisional_clients) < @max_clients;
```

In `GetPeerRouteConfigsForRoutes`, add `AND pr.serve_pool` after `AND pr.callback_verified IS NOT NULL`, with this comment line above `-- name:`:

```sql
-- serve_pool is rechecked here so a route that re-registered as private is
-- not handed out from a catalog cached before it did.
```

Append to `helper.sql`:

```sql
-- The live peer route of each of an inviter's paired volunteers: the most
-- recently registered one that is verified, unexpired and not deprecated.
-- serve_pool is deliberately not checked: a private route exists for exactly
-- these clients.
-- name: ListHelperRoutesForInviter :many
SELECT DISTINCT ON (hp.pairing_id)
       hp.pairing_id,
       pr.peer_route,
       pr.address,
       pr.port,
       pr.track AS track_id,
       pr.country_code,
       pr.launch_cfg
FROM helper_pairings hp
JOIN peer_routes pr ON pr.owner_device_id = hp.volunteer_device_id
WHERE hp.inviter_device_id = @inviter_device_id
  AND pr.deprecated IS NULL
  AND pr.callback_verified IS NOT NULL
  AND pr.expiration > now()
ORDER BY hp.pairing_id, pr.registered_at DESC;
```

The `peer_routes_owner` partial index on `(owner_device_id) WHERE deprecated IS NULL` serves this join.

- [ ] **Step 5: Generate, and keep the register handler's behavior**

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct/cmd/api && go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate && git status --short dal/`
Expected: `dal/peer_routes.sql.go`, `dal/helper.sql.go` and `dal/models.go` are modified. `ListHelperRoutesForInviterRow` has the field types listed under Interfaces.

`InsertPeerRouteParams` now has `ServePool bool`, and its zero value would make every route private. In `peer_handler.go` `Register`, add the field to the `dal.InsertPeerRouteParams` literal, after `Expiration`:

```go
		// Every route serves the pool until the request field lands.
		ServePool:     true,
```

In `onPeerArmPopulationChange`'s inline count, add `AND pr.serve_pool` after `AND pr.callback_verified IS NOT NULL`, so a private route never counts as an arm's first or last live peer:

```go
			(SELECT COUNT(*) FROM peer_routes pr
			 WHERE pr.track = self.track
			   AND pr.region IS NOT DISTINCT FROM self.region
			   AND pr.deprecated IS NULL
			   AND pr.callback_verified IS NOT NULL
			   AND pr.serve_pool
			   AND pr.expiration > now())
```

That hook returns early without a cache manager, which every test fixture lacks. The change is a filter on the same predicate the catalog uses, and it is checked by review rather than by a test.

- [ ] **Step 6: Run the tests**

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct && go build ./cmd/api/... && $LC_TEST 'TestDirectModeQueries|TestPeer|TestBuildArmCatalog_PeerTrack|TestBuildPeerArms|TestReservePeerSlot'`
Expected: PASS. The existing peer and catalog tests still see their routes, because the handler passes `true` and the raw inserts get the column default.

- [ ] **Step 7: Commit**

```bash
git add cmd/api/migrations/infra/000146_peer_routes_serve_pool.up.sql cmd/api/migrations/infra/000146_peer_routes_serve_pool.down.sql cmd/api/queries/infra/peer_routes.sql cmd/api/queries/infra/helper.sql cmd/api/dal/ cmd/api/peer_handler.go cmd/api/helper_direct_db_test.go
git commit -m "api: peer_routes.serve_pool and the inviter's paired-route query"
```

---

### Task 5: lantern-cloud — proto fields and `serve_pool` on register

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Modify: `cmd/api/apipb/proto/services/peer.proto`
- Generated: `cmd/api/apipb/services/peer.pb.go`, `peer_grpc.pb.go`, `cmd/backoffice/src/gen/services/peer_pb.ts`, `peer-PeerService_connectquery.ts`, `openapiv2/services/peer.swagger.json`
- Modify: `cmd/api/peer_handler.go` (Register stores serve_pool; Heartbeat returns `PeerHeartbeatResponse`)
- Modify: `cmd/api/peer_handler_test.go` (wire JSON types)
- Modify: `cmd/api/peer_handler_db_test.go` (`registerRequestWith`, new tests)

**Interfaces:**
- Consumes: `InsertPeerRouteParams.ServePool` (Task 4), `bandit.BuildArmCatalog`, `ls.allRoutesForTrack`, `trackRoutesCache`, `peerRegionID`.
- Produces:
  - `services.PeerRegisterRequest.ServePool *bool`
  - `services.PeerRegisterResponse.PairingShortIds []string`
  - `services.PeerHeartbeatResponse{PairingShortIds []string}`
  - `peerService.Heartbeat` returns `(*services.PeerHeartbeatResponse, error)`
  - Test helpers: `registerRequestWith(t, deviceID, port, servePool *bool)`, `registerAndVerify(t, mux, deviceID, port, servePool) string`, `promotePeerTrackForIR(t, tdb)`, `boolPtr`, and the `peerHeartbeatResponseJSON` type.

- [ ] **Step 1: Write the failing tests**

In `peer_handler_test.go`, replace the wire-type block with:

```go
type (
	peerRegisterRequestJSON struct {
		ExternalIP   string `json:"external_ip"`
		ExternalPort int32  `json:"external_port"`
		InternalPort int32  `json:"internal_port,omitempty"`
		ServePool    *bool  `json:"serve_pool,omitempty"`
	}
	peerRegisterResponseJSON struct {
		RouteID                  string   `json:"route_id"`
		ServerConfig             string   `json:"server_config"`
		HeartbeatIntervalSeconds int64    `json:"heartbeat_interval_seconds"`
		PairingShortIDs          []string `json:"pairing_short_ids"`
	}
	peerLifecycleRequestJSON struct {
		RouteID       string `json:"route_id"`
		ActiveClients int    `json:"active_clients,omitempty"`
	}
	peerHeartbeatResponseJSON struct {
		PairingShortIDs []string `json:"pairing_short_ids"`
	}
)
```

In `peer_handler_db_test.go`, replace `registerRequest` with:

```go
func registerRequest(t *testing.T, deviceID string, externalPort int32) *http.Request {
	return registerRequestWith(t, deviceID, externalPort, nil)
}

// registerRequestWith is registerRequest with serve_pool set, or absent when
// servePool is nil.
func registerRequestWith(t *testing.T, deviceID string, externalPort int32, servePool *bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(peerRegisterRequestJSON{
		ExternalIP:   "203.0.113.1",
		ExternalPort: externalPort,
		ServePool:    servePool,
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/peer/register", bytes.NewReader(body))
	r.Header.Set("X-Lantern-Feature-Override", common.UNBOUNDED)
	r.Header.Set("X-Lantern-Device-Id", deviceID)
	r.RemoteAddr = "203.0.113.1:443"
	return r
}

func boolPtr(b bool) *bool { return &b }
```

Append to `peer_handler_db_test.go`:

```go
// registerAndVerify registers deviceID's peer on port, verifies it, and
// returns its route ID.
func registerAndVerify(t *testing.T, mux http.Handler, deviceID string, port int32, servePool *bool) string {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, registerRequestWith(t, deviceID, port, servePool))
	require.Equal(t, http.StatusOK, w.Code, "register: %s", w.Body.String())
	var reg peerRegisterResponseJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))
	wv := httptest.NewRecorder()
	mux.ServeHTTP(wv, lifecycleRequest(t, "/v1/peer/verify", deviceID, reg.RouteID))
	require.Equal(t, http.StatusOK, wv.Code, "verify: %s", wv.Body.String())
	return reg.RouteID
}

// promotePeerTrackForIR makes the seeded peer track a bandit-eligible,
// peer-sourced track for free desktop clients in Iran, as
// TestBuildArmCatalog_PeerTrack_DispatchesToPeerBuilder does inline.
func promotePeerTrackForIR(t *testing.T, tdb *testdb.TestDB) {
	t.Helper()
	ctx := context.Background()
	_, err := tdb.GetPool().Exec(ctx,
		`UPDATE tracks SET route_source='peer', bandit_eligible=true,
		                  target_tier='FREE', target_countries='{IR}',
		                  docker_image_ref='sing-box'
		 WHERE name=$1`, peerTrackName)
	require.NoError(t, err)
	_, err = tdb.GetPool().Exec(ctx,
		`INSERT INTO target_platform_to_track (platform, track)
		 VALUES ((SELECT id FROM platforms WHERE name='DESKTOP'),
		         (SELECT id FROM tracks WHERE name=$1))`, peerTrackName)
	require.NoError(t, err)
	track.ClearTrackCaches()
}

func TestPeerRegister_StoresServePool(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	mux := newPeerTestRouter(t, ls)
	stored := func(routeID string) bool {
		t.Helper()
		var v bool
		require.NoError(t, tdb.GetPool().QueryRow(context.Background(),
			`SELECT serve_pool FROM peer_routes WHERE peer_route=$1`, uuid.MustParse(routeID)).Scan(&v))
		return v
	}
	register := func(port int32, servePool *bool) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, registerRequestWith(t, fmt.Sprintf("device-%d", port), port, servePool))
		require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
		var reg peerRegisterResponseJSON
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))
		return reg.RouteID
	}

	assert.True(t, stored(register(12001, nil)), "absent means true: clients that predate the field are unchanged")
	assert.True(t, stored(register(12002, boolPtr(true))))
	private := register(12003, boolPtr(false))
	assert.False(t, stored(private))
	assert.Equal(t, private, register(12003, nil), "re-registering keeps the route")
	assert.True(t, stored(private), "and takes the new value")
}

func TestPeerCatalog_ExcludesRoutesThatDoNotServeThePool(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	promotePeerTrackForIR(t, tdb)
	mux := newPeerTestRouter(t, ls)
	public := registerAndVerify(t, mux, "device-public", 13001, nil)
	registerAndVerify(t, mux, "device-private", 13002, boolPtr(false))

	ctx := context.Background()
	regionID := peerRegionID(t, tdb)
	allRegions, err := region.GetRegions(ctx, region.GetRegionsFilters{}, ls.apiDB)
	require.NoError(t, err)
	var peerRegion region.Region
	for _, r := range allRegions {
		if r.ID == regionID {
			peerRegion = r
		}
	}
	require.NotZero(t, peerRegion.ID)
	cf := track.ClientFactors{
		Platform:           track.Desktop,
		Tier:               track.Free,
		Version:            semver.MustParse("9.5.0"),
		DeviceID:           "consumer-A",
		Country:            "IR",
		TargetBackend:      track.SINGBOX,
		SupportedProtocols: []string{"samizdat"},
	}
	arms, err := bandit.BuildArmCatalog(ctx, []region.Region{peerRegion}, cf, ls.apiDB,
		bandit.CatalogOpts{AllRegions: allRegions})
	require.NoError(t, err)
	require.Len(t, arms, 1)
	assert.Equal(t, []string{public}, arms[0].RouteIDs, "a private route is not in the bandit catalog")

	tr, err := track.GetTrackByName(ctx, peerTrackName, ls.apiDB)
	require.NoError(t, err)
	trackRoutesCache.DeleteAll()
	t.Cleanup(func() { trackRoutesCache.DeleteAll() })
	routes, err := ls.allRoutesForTrack(ctx, tr)
	require.NoError(t, err)
	assert.Equal(t, []string{public}, routes, "nor in force_track's route list")
}
```

`peer_handler_db_test.go` already imports `fmt`, `semver`, `region`, `track`, `bandit`, `uuid` and `testdb`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `$LC_TEST 'TestPeerRegister_StoresServePool|TestPeerCatalog_ExcludesRoutesThatDoNotServeThePool'`
Expected: FAIL. Before the proto field exists, the JSON binding refuses `serve_pool`, so registers that send it return 400.

- [ ] **Step 3: Change the proto and regenerate**

In `peer.proto`, change the Heartbeat RPC:

```proto
  // Heartbeat extends the route's expiration and returns the short IDs its
  // inbound must accept for the owner's helper pairings. NotFound means the
  // route is gone and the peer should stop.
  rpc Heartbeat(PeerLifecycleRequest) returns (PeerHeartbeatResponse) {
    option (google.api.http) = {
      post: "/v1/peer/heartbeat"
      body: "*"
    };
  }
```

Add to `PeerRegisterRequest`:

```proto
  // Whether the route also serves the general pool ("help others too").
  // Absent means true, so clients that predate the field keep serving the
  // pool. A route with serve_pool false is reachable only through its
  // owner's helper pairings.
  optional bool serve_pool = 4;
```

Add to `PeerRegisterResponse`:

```proto
  // The short IDs, 16 hex characters each, the inbound must accept for the
  // owner's helper pairings, beside the launch config's own short ID. Same
  // contents as PeerHeartbeatResponse.pairing_short_ids.
  repeated string pairing_short_ids = 4;
```

Append:

```proto
message PeerHeartbeatResponse {
  // For each helper pairing where this device is the volunteer, the short
  // IDs of the previous, current and next 15-minute epochs, in that order.
  // The peer replaces its pairing short IDs with these and keeps the launch
  // config's own, so an ended pairing stops working at the next heartbeat.
  repeated string pairing_short_ids = 1;
}
```

Run:

```bash
cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct
make proto
git status --porcelain | awk '{print $2}' | grep -v -E 'peer(\.proto|\.pb\.go|_grpc\.pb\.go|_pb\.ts|-PeerService_connectquery\.ts|\.swagger\.json)$' | xargs -r git checkout --
git status --short
```

Expected: only `peer.proto` and its five generated files are modified. Run this before editing any Go file, so the revert cannot touch hand-written changes.

- [ ] **Step 4: Update the handlers**

In `Register`, replace the interim `ServePool: true` line from Task 4 with:

```go
		// Absent means true, so peers on clients that predate the field keep
		// serving the pool.
		ServePool:     req.ServePool == nil || req.GetServePool(),
```

Change `Heartbeat`'s signature and final return:

```go
func (ls peerService) Heartbeat(ctx context.Context, req *services.PeerLifecycleRequest) (*services.PeerHeartbeatResponse, error) {
```

```go
	return &services.PeerHeartbeatResponse{}, nil
```

`emptypb` is still used by `Verify` and `Deregister`.

- [ ] **Step 5: Run the tests**

Run: `go build ./cmd/api/... && $LC_TEST 'TestPeer|TestBuildArmCatalog_PeerTrack|TestBuildPeerArms|TestReservePeerSlot|TestGRPC|TestHTTPSurface'`
Expected: PASS. The `grpc_services_test.go` heartbeat calls already discard the response (`_, err = client.Heartbeat(...)`).

- [ ] **Step 6: Commit**

```bash
git add cmd/api/apipb/proto/services/peer.proto cmd/api/apipb/services/peer.pb.go cmd/api/apipb/services/peer_grpc.pb.go cmd/backoffice/src/gen/services/peer_pb.ts cmd/backoffice/src/gen/services/peer-PeerService_connectquery.ts openapiv2/services/peer.swagger.json cmd/api/peer_handler.go cmd/api/peer_handler_test.go cmd/api/peer_handler_db_test.go
git commit -m "api: serve_pool on peer register keeps private routes out of the pool"
```

---

### Task 6: lantern-cloud — pairing short IDs in the register and heartbeat responses

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Create: `cmd/api/helper_direct.go`
- Modify: `cmd/api/main.go` (field and startup derivation)
- Modify: `cmd/api/peer_handler.go` (Register and Heartbeat)
- Modify: `cmd/api/helper_direct_db_test.go`

**Interfaces:**
- Consumes: `pairing.PeerSIDKey` and `AcceptedPeerShortIDs` (Task 3), `dal.ListHelperPairingsForDevice` and `DeleteHelperPairingForDevice` (plan 1), `PeerHeartbeatResponse` (Task 5).
- Produces:
  - `lanternService.helperPeerSIDKey []byte`
  - `var helperNow = time.Now`
  - `func helperPeerSIDKeyFor(ticketKey ed25519.PrivateKey) []byte`
  - `func (ls *lanternService) volunteerPairingShortIDs(ctx, deviceID string, now time.Time) ([]string, error)`
  - Test helpers `acceptedHex`, `pinHelperNow` and `heartbeat`.

- [ ] **Step 1: Write the failing tests**

Replace the import block of `helper_direct_db_test.go` with:

```go
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/lantern-cloud/cmd/api/bandit"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/cmd/api/pairing"
	"github.com/getlantern/lantern-cloud/testdb"
)
```

Append:

```go
// testTicketSeed is the seed newHelperTestService signs tickets with.
func testTicketSeed() []byte { return bytes.Repeat([]byte{9}, ed25519.SeedSize) }

// acceptedHex is pairing.AcceptedPeerShortIDs as the API returns it.
func acceptedHex(key []byte, id uuid.UUID, now time.Time) []string {
	var out []string
	for _, sid := range pairing.AcceptedPeerShortIDs(key, id, now) {
		out = append(out, hex.EncodeToString(sid[:]))
	}
	return out
}

// pinHelperNow fixes the clock pairing short IDs are derived against.
func pinHelperNow(t *testing.T, now time.Time) {
	t.Helper()
	helperNow = func() time.Time { return now }
	t.Cleanup(func() { helperNow = time.Now })
}

func heartbeat(t *testing.T, mux http.Handler, deviceID, routeID string) peerHeartbeatResponseJSON {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, lifecycleRequest(t, "/v1/peer/heartbeat", deviceID, routeID))
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var hb peerHeartbeatResponseJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hb))
	return hb
}

func TestHelperPeerSIDKeyFor(t *testing.T) {
	assert.Nil(t, helperPeerSIDKeyFor(nil), "no ticket key, no pairing short IDs")
	assert.Equal(t, pairing.PeerSIDKey(testTicketSeed()),
		helperPeerSIDKeyFor(ed25519.NewKeyFromSeed(testTicketSeed())))
}

func TestPeerVolunteerGetsPairingShortIDs(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	ls.helperPeerSIDKey = helperPeerSIDKeyFor(ed25519.NewKeyFromSeed(testTicketSeed()))
	now := time.Unix(1_800_000_450, 0)
	pinHelperNow(t, now)
	ctx := context.Background()
	q := dal.New(tdb.GetPool())
	asVolunteer := uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: asVolunteer, InviterDeviceID: "friend", VolunteerDeviceID: "device-A"}))
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: uuid.New(), InviterDeviceID: "device-A", VolunteerDeviceID: "someone"}))
	want := acceptedHex(ls.helperPeerSIDKey, asVolunteer, now)
	require.Len(t, want, 3)

	mux := newPeerTestRouter(t, ls)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, registerRequest(t, "device-A", 12345))
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var reg peerRegisterResponseJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))
	assert.Equal(t, want, reg.PairingShortIDs, "only pairings where this device volunteers")
	assert.Equal(t, want, heartbeat(t, mux, "device-A", reg.RouteID).PairingShortIDs)

	// Ending the pairing drops its IDs at the volunteer's next heartbeat.
	n, err := q.DeleteHelperPairingForDevice(ctx, dal.DeleteHelperPairingForDeviceParams{
		PairingID: asVolunteer, DeviceID: "friend"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	assert.Empty(t, heartbeat(t, mux, "device-A", reg.RouteID).PairingShortIDs)
}

func TestPeerPairingShortIDsNeedTheTicketKey(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	require.NoError(t, dal.New(tdb.GetPool()).InsertHelperPairing(context.Background(), dal.InsertHelperPairingParams{
		PairingID: uuid.New(), InviterDeviceID: "friend", VolunteerDeviceID: "device-A"}))
	mux := newPeerTestRouter(t, ls)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, registerRequest(t, "device-A", 12345))
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var reg peerRegisterResponseJSON
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))
	assert.Empty(t, reg.PairingShortIDs, "without K_sid direct mode is off, and registration still works")
	assert.Empty(t, heartbeat(t, mux, "device-A", reg.RouteID).PairingShortIDs)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `$LC_TEST 'TestHelperPeerSIDKeyFor|TestPeerVolunteerGetsPairingShortIDs|TestPeerPairingShortIDsNeedTheTicketKey'`
Expected: the build fails with `undefined: helperPeerSIDKeyFor`, `undefined: helperNow` and `ls.helperPeerSIDKey undefined`.

- [ ] **Step 3: Implement**

In `main.go`, add the field after `helperTicketKey`:

```go
	// helperPeerSIDKey is K_sid, derived from helperTicketKey's seed; nil
	// turns direct-mode pairing short IDs off.
	helperPeerSIDKey []byte
```

After `ls.helperTicketKey = loadHelperTicketKey(ctx)`:

```go
	ls.helperPeerSIDKey = helperPeerSIDKeyFor(ls.helperTicketKey)
```

`cmd/api/helper_direct.go`:

```go
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/cmd/api/pairing"
)

// helperNow is the clock pairing short IDs are derived against. Tests pin it.
var helperNow = time.Now

// helperPeerSIDKeyFor derives K_sid from the ticket signing key's seed. With
// no ticket key, pairing short IDs are off too, and direct mode degrades to
// nothing rather than failing.
func helperPeerSIDKeyFor(ticketKey ed25519.PrivateKey) []byte {
	if ticketKey == nil {
		return nil
	}
	return pairing.PeerSIDKey(ticketKey.Seed())
}

// volunteerPairingShortIDs returns, as hex, the short IDs the inbound of a
// route owned by deviceID must accept at now: for each pairing where deviceID
// is the volunteer, the IDs of epochs e-1, e and e+1. Nothing is logged: the
// IDs are credentials.
func (ls *lanternService) volunteerPairingShortIDs(ctx context.Context, deviceID string, now time.Time) ([]string, error) {
	if len(ls.helperPeerSIDKey) == 0 {
		return nil, nil
	}
	rows, err := dal.New(ls.apiDB.GetPool()).ListHelperPairingsForDevice(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list helper pairings: %w", err)
	}
	var out []string
	for _, r := range rows {
		if r.VolunteerDeviceID != deviceID {
			continue
		}
		for _, sid := range pairing.AcceptedPeerShortIDs(ls.helperPeerSIDKey, r.PairingID, now) {
			out = append(out, hex.EncodeToString(sid[:]))
		}
	}
	return out, nil
}
```

In `peer_handler.go` `Register`, replace the final `return &services.PeerRegisterResponse{...}, nil` with:

```go
	pairingIDs, err := ls.volunteerPairingShortIDs(ctx, deviceID, helperNow())
	if err != nil {
		// The registration stands; the first heartbeat delivers them.
		log.Error(ctx, "peer register: pairing short ids", err, nil)
	}
	return &services.PeerRegisterResponse{
		RouteId:                  insert.PeerRoute.String(),
		ServerConfig:             singboxCfg.LaunchCfgSingbox.Options,
		HeartbeatIntervalSeconds: peerHeartbeatIntervalSec,
		PairingShortIds:          pairingIDs,
	}, nil
```

In `Heartbeat`, insert after `routeID, activeClients := lifecycleRoute(req)`:

```go
	// Looked up before the expiration is extended. If the lookup fails, the
	// route is not kept alive on a pairing set that may still hold ended
	// pairings: unless a later heartbeat succeeds, it expires within
	// peerRouteTTL and the peer stops.
	pairingIDs, err := ls.volunteerPairingShortIDs(ctx, deviceID, helperNow())
	if err != nil {
		return nil, publicError(ctx, http.StatusInternalServerError, "list pairing short ids", err)
	}
```

The next statement becomes `rows, err = dal.New(...)...` (plain assignment, since `err` now exists), and the final return becomes:

```go
	return &services.PeerHeartbeatResponse{PairingShortIds: pairingIDs}, nil
```

The IDs go out only after the owner-gated extension succeeds. A caller that probes someone else's route gets the same 404 as before and learns nothing.

- [ ] **Step 4: Run the tests**

Run: `go build ./cmd/api/... && $LC_TEST 'TestHelperPeerSIDKeyFor|TestPeerVolunteerGetsPairingShortIDs|TestPeerPairingShortIDsNeedTheTicketKey|TestPeer|TestDirectModeQueries'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/api/helper_direct.go cmd/api/main.go cmd/api/peer_handler.go cmd/api/helper_direct_db_test.go
git commit -m "api: give a volunteer its pairings' short IDs on register and heartbeat"
```

---

### Task 7: lantern-cloud — `pcfg.HelperSamizdatOutbound`

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Modify: `cmd/api/pcfg/samizdat.go`
- Modify: `cmd/api/pcfg/samizdat_test.go`

**Interfaces:**
- Consumes: `GenerateSamizdat` and `singboxCtx` (existing); test helpers `testCertKeyPEM` and `parseLaunchOptions`.
- Produces:
  - `func samizdatOutboundOptions(launchOptions string, protocolOpts map[string]string, addr string, listenPort int32) (*LO.SamizdatOutboundOptions, error)`, extracted from `generateSamizdatConnectConf` with its behavior unchanged.
  - `func HelperSamizdatOutbound(launchCfgJSON []byte, protocolOpts map[string]string, addr string, port int32, shortIDHex, tag string) (option.Outbound, error)`

- [ ] **Step 1: Write the failing tests**

Append to `pcfg/samizdat_test.go`:

```go
// helperLaunchJSON is a samizdat peer launch config as peer_routes.launch_cfg
// stores it, listening on listenPort.
func helperLaunchJSON(t *testing.T, listenPort int32) []byte {
	t.Helper()
	certPEM, keyPEM := testCertKeyPEM(t)
	sb, _, _, err := GenerateSamizdat(certPEM, keyPEM, map[string]string{"port": strconv.Itoa(int(listenPort))})
	require.NoError(t, err)
	b, err := protojson.Marshal(&apipb.ProxyLaunchConfig{
		ListenPort: listenPort, CertPem: certPEM, KeyPem: keyPEM, ProtocolConfig: sb})
	require.NoError(t, err)
	return b
}

func TestHelperSamizdatOutbound(t *testing.T) {
	launch := helperLaunchJSON(t, 35000)
	var lc apipb.ProxyLaunchConfig
	require.NoError(t, protojson.Unmarshal(launch, &lc))
	inOpts, _ := parseLaunchOptions(t, lc.GetLaunchCfgSingbox().Options)
	protoOpts := map[string]string{"fingerprint": "firefox"}

	out, err := HelperSamizdatOutbound(launch, protoOpts, "198.51.100.7", 4444, "a1b2c3d4e5f60718", "helper-route")
	require.NoError(t, err)
	assert.Equal(t, "samizdat", out.Type)
	assert.Equal(t, "helper-route", out.Tag)
	o, ok := out.Options.(*LO.SamizdatOutboundOptions)
	require.True(t, ok, "options are typed samizdat outbound options, got %T", out.Options)
	assert.Equal(t, "a1b2c3d4e5f60718", o.ShortID, "the pairing's short ID, not the route's base one")
	assert.NotEqual(t, inOpts.ShortIDs[0], o.ShortID)
	assert.Equal(t, "198.51.100.7", o.Server)
	assert.Equal(t, uint16(4444), o.ServerPort, "the route's external port, not the launch config's listen port")
	assert.Equal(t, "firefox", o.Fingerprint)

	// Everything else is what the pool's connect config would carry.
	pool, err := generateSamizdatConnectConf(lc.GetLaunchCfgSingbox().Options, protoOpts, "198.51.100.7", 4444)
	require.NoError(t, err)
	poolOpts, err := singjson.UnmarshalExtendedContext[option.Options](singboxCtx, []byte(pool.ConnectSingbox.Options))
	require.NoError(t, err)
	p := poolOpts.Outbounds[0].Options.(*LO.SamizdatOutboundOptions)
	p.ShortID = o.ShortID
	assert.Equal(t, p, o)
}

func TestHelperSamizdatOutboundRejectsBadInput(t *testing.T) {
	launch := helperLaunchJSON(t, 35000)
	_, err := HelperSamizdatOutbound(launch, nil, "198.51.100.7", 4444, "short", "helper-route")
	assert.ErrorContains(t, err, "short id")
	_, err = HelperSamizdatOutbound([]byte(`{}`), nil, "198.51.100.7", 4444, "a1b2c3d4e5f60718", "helper-route")
	assert.ErrorContains(t, err, "not a samizdat")
	_, err = HelperSamizdatOutbound([]byte(`not json`), nil, "198.51.100.7", 4444, "a1b2c3d4e5f60718", "helper-route")
	assert.ErrorContains(t, err, "launch config")
}
```

The test file already imports `strconv`, `protojson`, `apipb`, `LO`, `option` and `singjson`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/lantern-cloud-helper-direct && go test -tags=test,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme ./cmd/api/pcfg/ -run 'HelperSamizdatOutbound' -v`
Expected: the build fails with `undefined: HelperSamizdatOutbound`.

- [ ] **Step 3: Implement**

In `pcfg/samizdat.go`, replace `generateSamizdatConnectConf` with the extracted builder and a thin wrapper:

```go
// samizdatOutboundOptions builds the samizdat client options for a proxy from
// the typed inbound options in its launch config.
func samizdatOutboundOptions(launchOptions string, protocolOpts map[string]string, addr string, listenPort int32) (*LO.SamizdatOutboundOptions, error) {
	if listenPort < 1 || listenPort > 65535 {
		return nil, fmt.Errorf("listen port %d out of range (1-65535)", listenPort)
	}

	// Parse launch options as sing-box config and extract typed inbound options
	sbOpts, err := json.UnmarshalExtendedContext[option.Options](singboxCtx, []byte(launchOptions))
	if err != nil {
		return nil, fmt.Errorf("failed to parse launch options: %w", err)
	}
	if len(sbOpts.Inbounds) == 0 {
		return nil, fmt.Errorf("no inbound found in launch options")
	}
	inOpts, ok := sbOpts.Inbounds[0].Options.(*LO.SamizdatInboundOptions)
	if !ok {
		return nil, fmt.Errorf("unexpected inbound options type: %T", sbOpts.Inbounds[0].Options)
	}

	// Derive public key from inbound private key
	privKeyBytes, err := hex.DecodeString(inOpts.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key: %w", err)
	}
	pubKeyBytes, err := curve25519.X25519(privKeyBytes, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}
	pubKey := hex.EncodeToString(pubKeyBytes)

	slog.Debug("generateSamizdatConnectConf: derived key",
		"addr", addr,
		"private_key_len", len(inOpts.PrivateKey),
		"public_key_prefix", pubKey[:16],
		"public_key_len", len(pubKey),
		"short_ids_count", len(inOpts.ShortIDs),
	)

	if len(inOpts.ShortIDs) == 0 {
		return nil, fmt.Errorf("no short IDs in launch options")
	}

	// Server name defaults to masquerade domain unless overridden
	serverName := protocolOpts["server_name"]
	if serverName == "" {
		serverName = inOpts.MasqueradeDomain
	}

	disableJitter, _ := strconv.ParseBool(protocolOpts["disable_jitter"])
	disableTCPFrag, _ := strconv.ParseBool(protocolOpts["disable_tcp_fragmentation"])
	disableRecordFrag, _ := strconv.ParseBool(protocolOpts["disable_record_fragmentation"])

	// Client-side settings (fingerprint, disable_padding, disable_jitter, etc.) are
	// passed through from Protocol Options; defaults are defined in backoffice.
	// Fields left unset (zero-value false for bool) leave features enabled in lantern-box.
	return &LO.SamizdatOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     addr,
			ServerPort: uint16(listenPort),
		},
		PublicKey:                  pubKey,
		ShortID:                    inOpts.ShortIDs[0],
		ServerName:                 serverName,
		Fingerprint:                protocolOpts["fingerprint"],
		DisableJitter:              disableJitter,
		DisableTCPFragmentation:    disableTCPFrag,
		DisableRecordFragmentation: disableRecordFrag,
	}, nil
}

// generateSamizdatConnectConf creates a samizdat client connect configuration
// by extracting per-proxy credentials from the typed inbound options in the
// launch config.
func generateSamizdatConnectConf(launchOptions string, protocolOpts map[string]string, addr string, listenPort int32) (*apipb.ProxyConnectConfig_ConnectSingbox, error) {
	outboundOpts, err := samizdatOutboundOptions(launchOptions, protocolOpts, addr, listenPort)
	if err != nil {
		return nil, err
	}
	opts := option.Options{
		Outbounds: []option.Outbound{
			{
				Type:    "samizdat",
				Tag:     "samizdat-out",
				Options: outboundOpts,
			},
		},
	}
	buf, err := json.MarshalContext(singboxCtx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal samizdat connect options: %w", err)
	}
	slog.Debug("generateSamizdatConnectConf: output generated",
		"addr", addr,
		"public_key_prefix", outboundOpts.PublicKey[:16],
		"options_len", len(buf),
	)
	return &apipb.ProxyConnectConfig_ConnectSingbox{
		ConnectSingbox: &apipb.SingboxConfig{
			Variant: "samizdat",
			Options: string(buf),
		},
	}, nil
}

// HelperSamizdatOutbound builds the samizdat outbound a paired inviter uses to
// reach her volunteer's peer route. It is the pool's connect config with two
// differences. The short ID is the pairing's, so only she can use it and only
// while the pairing lasts. The port is the route's external port
// (peer_routes.port), the one the verifier proved reachable; the launch
// config's ListenPort is the internal port the peer binds behind UPnP.
func HelperSamizdatOutbound(launchCfgJSON []byte, protocolOpts map[string]string, addr string, port int32, shortIDHex, tag string) (option.Outbound, error) {
	if b, err := hex.DecodeString(shortIDHex); err != nil || len(b) != 8 {
		return option.Outbound{}, fmt.Errorf("short id must be 16 hex characters")
	}
	var launchCfg apipb.ProxyLaunchConfig
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(launchCfgJSON, &launchCfg); err != nil {
		return option.Outbound{}, fmt.Errorf("unmarshal launch config: %w", err)
	}
	sb := launchCfg.GetLaunchCfgSingbox()
	if sb == nil || sb.Variant != "samizdat" {
		return option.Outbound{}, fmt.Errorf("launch config is not a samizdat sing-box config")
	}
	o, err := samizdatOutboundOptions(sb.Options, protocolOpts, addr, port)
	if err != nil {
		return option.Outbound{}, err
	}
	o.ShortID = shortIDHex
	return option.Outbound{Type: "samizdat", Tag: tag, Options: o}, nil
}
```

The public key is always 64 hex characters, because X25519 returns 32 bytes. So `pubKey[:16]` replaces the old length-guarded prefix without changing what is logged.

- [ ] **Step 4: Run the tests**

Run: `go test -tags=test,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme ./cmd/api/pcfg/ -v`
Expected: PASS, including the unchanged `TestGenerateSamizdatConnectConfig` and `TestSamizdatLaunchConnectRoundTrip`.

- [ ] **Step 5: Commit**

```bash
git add cmd/api/pcfg/samizdat.go cmd/api/pcfg/samizdat_test.go
git commit -m "pcfg: samizdat outbound for a paired inviter, with the pairing's short ID"
```

---

### Task 8: lantern-cloud — paired outbounds in `/config-new`

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Modify: `cmd/api/helper_direct.go`
- Modify: `cmd/api/config.go` (`configNew`)
- Modify: `cmd/api/helper_direct_db_test.go`

**Interfaces:**
- Consumes: `dal.ListHelperRoutesForInviter` (Task 4), `pairing.PeerShortID` and `PeerSIDEpoch` (Task 3), `pcfg.HelperSamizdatOutbound` (Task 7), `track.GetTrackByID`, `extractRouteID`, `featureFlagHelperInvites` (plan 1).
- Produces:
  - `type helperOutbound struct{ routeID string; outbound option.Outbound; location *common.ServerLocation }`
  - `func helperOutboundTag(route uuid.UUID) string`, which returns `"helper-" + route.String()`
  - `func (ls *lanternService) helperOutbounds(ctx, deviceID string, now time.Time) []helperOutbound`
  - `func dropPairedPoolConfigs(cfgs []*apipb.ProxyConnectConfig, helpers []helperOutbound) []*apipb.ProxyConnectConfig`

- [ ] **Step 1: Write the failing tests**

Replace the import block of `helper_direct_db_test.go` with:

```go
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	LO "github.com/getlantern/lantern-box/option"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb"
	"github.com/getlantern/lantern-cloud/cmd/api/bandit"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/cmd/api/pairing"
	"github.com/getlantern/lantern-cloud/cmd/api/pcfg"
	"github.com/getlantern/lantern-cloud/testdb"
)
```

Append:

```go
// samizdatLaunchJSON is a real samizdat peer launch config listening on
// listenPort, as Register stores it.
func samizdatLaunchJSON(t *testing.T, listenPort int32) []byte {
	t.Helper()
	certPEM, keyPEM, err := pcfg.GeneratePeerCert("")
	require.NoError(t, err)
	sb, _, _, err := pcfg.GenerateSamizdat(certPEM, keyPEM, map[string]string{"port": strconv.Itoa(int(listenPort))})
	require.NoError(t, err)
	b, err := protojson.Marshal(&apipb.ProxyLaunchConfig{
		ListenPort: listenPort, CertPem: certPEM, KeyPem: keyPEM, ProtocolConfig: sb})
	require.NoError(t, err)
	return b
}

func TestHelperOutbounds(t *testing.T) {
	ls, tdb := newPeerTestService(t, &fakePeerVerifier{})
	ls.helperPeerSIDKey = pairing.PeerSIDKey(testTicketSeed())
	ctx := context.Background()
	q := dal.New(tdb.GetPool())
	tr, err := q.GetTrackByName(ctx, peerTrackName)
	require.NoError(t, err)
	pid := uuid.New()
	require.NoError(t, q.InsertHelperPairing(ctx, dal.InsertHelperPairingParams{
		PairingID: pid, InviterDeviceID: "inviter", VolunteerDeviceID: "volunteer"}))
	route := insertPeerRoute(t, tdb, peerRow{device: "volunteer", port: 4444, track: int(tr.ID),
		launch: samizdatLaunchJSON(t, 35000), verified: true})
	now := time.Unix(1_800_000_450, 0)

	got := ls.helperOutbounds(ctx, "inviter", now)
	require.Len(t, got, 1)
	h := got[0]
	assert.Equal(t, route.String(), h.routeID)
	assert.Equal(t, "helper-"+route.String(), h.outbound.Tag)
	assert.Equal(t, "samizdat", h.outbound.Type)
	o := h.outbound.Options.(*LO.SamizdatOutboundOptions)
	want := pairing.PeerShortID(ls.helperPeerSIDKey, pid, pairing.PeerSIDEpoch(now))
	assert.Equal(t, hex.EncodeToString(want[:]), o.ShortID, "the current epoch's pairing short ID")
	assert.Equal(t, "198.51.100.7", o.Server)
	assert.Equal(t, uint16(4444), o.ServerPort, "the external port")
	require.NotNil(t, h.location)

	assert.Empty(t, ls.helperOutbounds(ctx, "stranger", now), "an unpaired device gets nothing")
	assert.Empty(t, ls.helperOutbounds(ctx, "volunteer", now), "a volunteer gets no outbound to itself")
	ls.helperPeerSIDKey = nil
	assert.Empty(t, ls.helperOutbounds(ctx, "inviter", now), "no K_sid, no paired outbounds")
}

func TestDropPairedPoolConfigs(t *testing.T) {
	paired := uuid.New().String()
	other := uuid.New().String()
	cfgs := []*apipb.ProxyConnectConfig{
		{Name: "samizdat-peer-1-" + paired, Track: "samizdat-peer-1"},
		{Name: "pikachu-" + other, Track: "pikachu"},
	}
	assert.Equal(t, cfgs, dropPairedPoolConfigs(cfgs, nil), "no helpers, nothing dropped")
	got := dropPairedPoolConfigs(cfgs, []helperOutbound{{routeID: paired}})
	require.Len(t, got, 1)
	assert.Equal(t, "pikachu-"+other, got[0].Name, "the paired outbound replaces the pool's copy of its route")
	assert.Len(t, cfgs, 2, "the input, which may be a cached response, is not modified")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `$LC_TEST 'TestHelperOutbounds|TestDropPairedPoolConfigs'`
Expected: the build fails with `ls.helperOutbounds undefined`, `undefined: dropPairedPoolConfigs` and `undefined: helperOutbound`.

- [ ] **Step 3: Implement the builder**

Replace the import block of `helper_direct.go` with:

```go
import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/getlantern/common"
	"github.com/google/uuid"
	"github.com/sagernet/sing-box/option"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/cmd/api/pairing"
	"github.com/getlantern/lantern-cloud/cmd/api/pcfg"
	"github.com/getlantern/lantern-cloud/cmd/api/track"
	"github.com/getlantern/lantern-cloud/log"
)
```

Append:

```go
// helperOutbound is one paired volunteer's route as an outbound in its
// inviter's config.
type helperOutbound struct {
	routeID  string
	outbound option.Outbound
	location *common.ServerLocation
}

func helperOutboundTag(route uuid.UUID) string { return "helper-" + route.String() }

// helperOutbounds returns an outbound for each of deviceID's paired
// volunteers that has a live peer route, authenticated with the pairing's
// short ID for now's epoch. They are added beside the bandit's selection, so
// her helpers never depend on exploration weights. They skip
// ReservePeerSlot, because volunteers report paired users in active_clients,
// which is what makes paired users crowd out the pool and not the reverse.
// They skip the Redis connect-config cache, because the credential is per
// pairing. Failures are logged without identifiers and drop only the route
// concerned.
func (ls *lanternService) helperOutbounds(ctx context.Context, deviceID string, now time.Time) []helperOutbound {
	if deviceID == "" || len(ls.helperPeerSIDKey) == 0 {
		return nil
	}
	rows, err := dal.New(ls.apiDB.GetPool()).ListHelperRoutesForInviter(ctx, deviceID)
	if err != nil {
		log.Error(ctx, "helper outbounds: list routes", err, nil)
		return nil
	}
	epoch := pairing.PeerSIDEpoch(now)
	out := make([]helperOutbound, 0, len(rows))
	for _, r := range rows {
		tr, err := track.GetTrackByID(ctx, int(r.TrackID), ls.apiDB)
		if err != nil {
			log.Error(ctx, "helper outbounds: track", err, nil)
			continue
		}
		sid := pairing.PeerShortID(ls.helperPeerSIDKey, r.PairingID, epoch)
		ob, err := pcfg.HelperSamizdatOutbound(r.LaunchCfg, tr.ProtocolOpts, r.Address.String(), r.Port,
			hex.EncodeToString(sid[:]), helperOutboundTag(r.PeerRoute))
		if err != nil {
			log.Error(ctx, "helper outbounds: build", err, nil)
			continue
		}
		out = append(out, helperOutbound{
			routeID:  r.PeerRoute.String(),
			outbound: ob,
			location: &common.ServerLocation{CountryCode: r.CountryCode.String},
		})
	}
	return out
}

// dropPairedPoolConfigs returns cfgs without the pool's assignments of routes
// the device also reaches through a pairing, so each such route appears once,
// as its paired outbound. It never modifies cfgs, which can belong to a
// cached response.
func dropPairedPoolConfigs(cfgs []*apipb.ProxyConnectConfig, helpers []helperOutbound) []*apipb.ProxyConnectConfig {
	if len(helpers) == 0 {
		return cfgs
	}
	paired := make(map[string]bool, len(helpers))
	for _, h := range helpers {
		paired[h.routeID] = true
	}
	out := make([]*apipb.ProxyConnectConfig, 0, len(cfgs))
	for _, c := range cfgs {
		if !paired[extractRouteID(c.Name)] {
			out = append(out, c)
		}
	}
	return out
}
```

`ListHelperRoutesForInviterRow.LaunchCfg` is `[]byte`, as with every `launch_cfg` column in `dal`, so it passes to `HelperSamizdatOutbound` directly.

- [ ] **Step 4: Wire it into `configNew`**

In `config.go`, insert immediately before `	outboundLocations := make(common.OutboundLocations)`:

```go
	// A paired inviter's volunteers, added beside whatever the pool assigned
	// (see helperOutbounds). poolConfigs is the pool's share, less any route
	// she also reaches through a pairing. The device ID is the one in the
	// request body, as for the rest of this handler.
	poolConfigs := assignmentResponse.Configs
	var helpers []helperOutbound
	if featureEnabled(featureFlagHelperInvites) {
		helpers = ls.helperOutbounds(ctx, req.DeviceID, helperNow())
		poolConfigs = dropPairedPoolConfigs(poolConfigs, helpers)
	}
```

Change the outbound loop header from `for _, proxy := range assignmentResponse.Configs {` to:

```go
	for _, proxy := range poolConfigs {
```

Replace the `numProxiesSkipped` block that follows the loop with:

```go
	if diff := len(poolConfigs) - (len(opts.Outbounds) + len(opts.Endpoints)); diff > 0 {
		span.SetAttribute("numProxiesSkipped", attribute.IntValue(diff))
	}
	// Appended after the pool's counts, so those describe the pool alone, and
	// with nothing recorded about them: this span carries the device ID and
	// must not say whether it has helpers.
	for _, h := range helpers {
		opts.Outbounds = append(opts.Outbounds, h.outbound)
		outboundLocations[h.outbound.Tag] = h.location
	}
```

Paired outbounds get no `routeIDToTag` entry. They are not bandit arms, so they carry no callback URL override and no report token.

- [ ] **Step 5: Run the tests**

Run: `go build ./cmd/api/... && $LC_TEST 'TestHelperOutbounds|TestDropPairedPoolConfigs|TestBanditIntegration|TestConfig'`
Expected: PASS. The existing config-new tests run with the flag off and are unchanged.

- [ ] **Step 6: Commit**

```bash
git add cmd/api/helper_direct.go cmd/api/config.go cmd/api/helper_direct_db_test.go
git commit -m "api: config-new adds each paired volunteer's live route for the inviter"
```

---

### Task 9: lantern-cloud — end-to-end test, full suite, telemetry audit, PR

**Repo / worktree:** lantern-cloud, `~/go/src/github.com/getlantern/lantern-cloud-helper-direct`

**Files:**
- Modify: `cmd/api/helper_direct_db_test.go`

**Interfaces:**
- Consumes: `Init`, `newTestJWTManager`, `setupTestTracks`, `seedPeerTrack`, `testutils.PrepareRedisForCacheTesting`, `testutils.InitCaches`, `flags.InitFlagsForTesting`, `approvedPairing` (plan 1; devices `"inviter"` and `"volunteer"`), `registerAndVerify` (Task 5), `heartbeat`, `acceptedHex` and `pinHelperNow` (Task 6).

- [ ] **Step 1: Write the end-to-end test**

Replace the import block of `helper_direct_db_test.go` with:

```go
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/common"
	"github.com/google/uuid"
	sagerNetJson "github.com/sagernet/sing/common/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	box "github.com/getlantern/lantern-box"
	LO "github.com/getlantern/lantern-box/option"

	"github.com/getlantern/lantern-cloud/cmd/api/apipb"
	"github.com/getlantern/lantern-cloud/cmd/api/bandit"
	"github.com/getlantern/lantern-cloud/cmd/api/dal"
	"github.com/getlantern/lantern-cloud/cmd/api/pairing"
	"github.com/getlantern/lantern-cloud/cmd/api/pcfg"
	"github.com/getlantern/lantern-cloud/cmd/api/track"
	"github.com/getlantern/lantern-cloud/flags"
	"github.com/getlantern/lantern-cloud/maxmind"
	"github.com/getlantern/lantern-cloud/settings"
	"github.com/getlantern/lantern-cloud/testdb"
	"github.com/getlantern/lantern-cloud/testutils"
)
```

Append:

```go
// configNewAs fetches /v1/config-new as deviceID with helper invites on, in
// the shape TestBanditIntegration_FallbackToHashRing uses.
func configNewAs(t *testing.T, root http.Handler, deviceID string) common.ConfigResponse {
	t.Helper()
	body, err := json.Marshal(&common.ConfigRequest{
		DeviceID:  deviceID,
		UserID:    "10",
		Platform:  "android",
		ProToken:  "helper-direct-pro-token",
		Backend:   "sing-box",
		Protocols: []string{"hysteria2"},
	})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/config-new", bytes.NewReader(body))
	r.Header.Set("x-lantern-client-country", "US")
	r.Header.Set("x-forwarded-for", "89.160.20.134")
	r.Header.Set("X-Lantern-App-Version", "7.2.3")
	r.Header.Set("X-Lantern-Feature-Override", featureFlagHelperInvites)
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, "config-new as %s: %s", deviceID, w.Body.String())
	cfg, err := sagerNetJson.UnmarshalExtendedContext[common.ConfigResponse](box.BaseContext(), w.Body.Bytes())
	require.NoError(t, err)
	return cfg
}

// TestHelperDirectModeEndToEnd runs direct mode through the real router: a
// pairing is approved, the volunteer registers a private route and is
// verified, and then the inviter's config, a stranger's config and the
// volunteer's heartbeat are checked.
func TestHelperDirectModeEndToEnd(t *testing.T) {
	srv := maxmind.SetupMaxmindTestServer(t)
	t.Cleanup(srv.Close)
	maxmind.WaitForTestMaxmindServer(t)
	track.ClearTrackCaches()
	bandit.ClearCatalogCache()
	t.Cleanup(track.ClearTrackCaches)
	t.Cleanup(bandit.ClearCatalogCache)
	tdb := testdb.New(t, "testdata/fixtures.sql")
	t.Cleanup(tdb.Teardown)
	ctx := context.Background()
	storeCache := testutils.PrepareRedisForCacheTesting(t)
	t.Cleanup(func() { storeCache.Close(ctx) })

	ls := &lanternService{
		apiDB:            tdb,
		cacheManager:     testutils.InitCaches(t),
		settings:         settings.MakeMockManager(),
		peerVerifier:     &fakePeerVerifier{},
		helperTicketKey:  ed25519.NewKeyFromSeed(testTicketSeed()),
		helperPeerSIDKey: helperPeerSIDKeyFor(ed25519.NewKeyFromSeed(testTicketSeed())),
	}
	t.Setenv("DASHBOARD_OAUTH_CLIENT_ID", "api-test")
	ls.jwtManager = newTestJWTManager(t)
	root, _, _ := Init(ctx, ls)
	setupTestTracks(t, "")
	seedPeerTrack(t, tdb)
	mockOff := flags.InitFlagsForTesting("{}")
	t.Cleanup(mockOff)
	time.Sleep(200 * time.Millisecond)
	now := time.Unix(1_800_000_450, 0)
	pinHelperNow(t, now)

	// 1. "inviter" and "volunteer" pair.
	pairingID := uuid.MustParse(approvedPairing(t, root))

	// 2. The volunteer registers a route that serves only its pairings, and
	// is verified.
	routeID := registerAndVerify(t, root, "volunteer", 23456, boolPtr(false))

	// 3. Its heartbeat carries the pairing's IDs for e-1, e and e+1.
	want := acceptedHex(ls.helperPeerSIDKey, pairingID, now)
	require.Len(t, want, 3)
	assert.Equal(t, want, heartbeat(t, root, "volunteer", routeID).PairingShortIDs)

	// 4. The inviter's config has the paired outbound with the current ID.
	cur := pairing.PeerShortID(ls.helperPeerSIDKey, pairingID, pairing.PeerSIDEpoch(now))
	var found bool
	for _, o := range configNewAs(t, root, "inviter").Options.Outbounds {
		if o.Tag != "helper-"+routeID {
			continue
		}
		found = true
		assert.Equal(t, "samizdat", o.Type)
		so, ok := o.Options.(*LO.SamizdatOutboundOptions)
		require.True(t, ok, "options are %T", o.Options)
		assert.Equal(t, hex.EncodeToString(cur[:]), so.ShortID)
		assert.Equal(t, "203.0.113.1", so.Server)
		assert.Equal(t, uint16(23456), so.ServerPort)
	}
	assert.True(t, found, "the inviter's config has outbound helper-%s", routeID)

	// 5. A device without the pairing never sees the route.
	for _, o := range configNewAs(t, root, "stranger").Options.Outbounds {
		assert.False(t, strings.Contains(o.Tag, routeID), "stranger got %s", o.Tag)
		if so, ok := o.Options.(*LO.SamizdatOutboundOptions); ok {
			assert.False(t, so.Server == "203.0.113.1" && so.ServerPort == 23456, "stranger got the private route as %s", o.Tag)
		}
	}
}
```

- [ ] **Step 2: Run it**

Run: `$LC_TEST 'TestHelperDirectModeEndToEnd'`
Expected: PASS. Tasks 4–8 already implement everything it checks. If it fails, fix the code the failure points at, not the test: the test is this plan's acceptance check.

- [ ] **Step 3: Full suite and lint**

Run: `make test`
Expected: all packages pass.

Run: `golangci-lint run --new-from-rev=origin/fisk/helper-invites-core ./cmd/api/...`
Expected: no new issues.

- [ ] **Step 4: Telemetry audit**

Run: `grep -nE 'log\.|attribute\.|slog\.' cmd/api/helper_direct.go; git diff origin/fisk/helper-invites-core -- cmd/api/peer_handler.go cmd/api/config.go | grep -E '^\+.*(log\.|attribute\.|SetAttribute)'`
Expected: only the four fixed-message `log.Error(ctx, "...", err, nil)` calls in `helper_direct.go` and the one in `Register`, with no attribute maps. No line records a pairing ID, a short ID or a helper count. If one does, remove it, rerun Step 3, and commit with `api: keep pairing identifiers out of direct-mode telemetry`.

- [ ] **Step 5: Commit and open the stacked PR**

```bash
git add cmd/api/helper_direct_db_test.go
git commit -m "api: end-to-end test of helper direct mode through config-new"
```

Open the PR with `skills:create-pr`, base `fisk/helper-invites-core`. Put the sequence diagram from this plan's header in the description, with `file.ext:LINE` labels checked against the branch.

- [ ] **Step 6: [Human] Production.** After PR #3660 and this PR merge: apply migration 000146 to production with the repo's migrator (`bin/migrator`) before or with the API deploy, then deploy the API. Direct mode stays dark until the `helper_invites` flag is on for a client, because plan 1's API and this plan's `configNew` branch are both behind that flag. Turning the flag on for the pilot is a separate production change, made once radiance (Task 11) has shipped.

---

### Task 10: radiance — lantern-box bump, API fields and the `serve_pool` setting

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-direct`

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `peer/api.go`
- Modify: `common/settings/settings.go`
- Modify: `peer/peer.go` (`servePool`, both `Register` calls, heartbeat call site)
- Modify: `peer/peer_test.go`

**Interfaces:**
- Consumes: `LB_TAG`, the lantern-box tag from Task 2 Step 7.
- Produces:
  - `RegisterRequest.ServePool bool` (`json:"serve_pool"`, always sent)
  - `RegisterResponse.PairingShortIDs []string`
  - `type HeartbeatResponse struct{ PairingShortIDs []string }`
  - `func (a *API) Heartbeat(ctx, routeID string, activeClients int) (*HeartbeatResponse, error)`
  - `settings.PeerServePoolKey` (`"peer_serve_pool"`; unset means true)
  - `func servePool() bool`
  - On `stubServer`: `heartbeatShortIDs atomic.Pointer[[]string]`

- [ ] **Step 1: Create the worktree and bump lantern-box**

```bash
git -C ~/go/src/github.com/getlantern/radiance fetch origin
git -C ~/go/src/github.com/getlantern/radiance worktree add ~/go/src/github.com/getlantern/radiance-helper-direct -b fisk/helper-direct-mode origin/main
cd ~/go/src/github.com/getlantern/radiance-helper-direct
go mod download -json github.com/getlantern/lantern-box@$LB_TAG | jq -r .GoMod | xargs grep 'github.com/sagernet/sing-box =>'
grep -n 'github.com/sagernet/sing-box =>' go.mod
```

Expected: both lines name the same `github.com/getlantern/sing-box-minimal` version. Replace directives are not inherited, so if they differ, copy lantern-box's version into radiance's `replace` line before continuing.

```bash
go get github.com/getlantern/lantern-box@$LB_TAG
go mod tidy
git diff --stat
go build ./...
git add go.mod go.sum
git commit -m "build: bump lantern-box to $LB_TAG for runtime samizdat short IDs"
```

Expected: the diff touches only `go.mod` and `go.sum`, including the indirect `github.com/getlantern/samizdat` moving to Task 1's commit. Commit the `replace` line in this same commit if it changed.

- [ ] **Step 2: Write the failing tests**

In `peer_test.go`, add a field to `stubServer` after `deregisterStatus int`:

```go
	// heartbeatShortIDs, when set, is returned as the heartbeat response's
	// pairing_short_ids. Unset, the heartbeat answers with an empty body, as
	// servers that predate the field do.
	heartbeatShortIDs atomic.Pointer[[]string]
```

In `newStubServer`'s `/v1/peer/heartbeat` handler, replace the final `w.WriteHeader(http.StatusOK)` with:

```go
		if ids := s.heartbeatShortIDs.Load(); ids != nil {
			_ = json.NewEncoder(w).Encode(HeartbeatResponse{PairingShortIDs: *ids})
			return
		}
		w.WriteHeader(http.StatusOK)
```

In the API header test, which calls each endpoint, change `require.NoError(t, api.Heartbeat(ctx, "00000000-0000-0000-0000-000000000123", 0))` to:

```go
	_, err = api.Heartbeat(ctx, "00000000-0000-0000-0000-000000000123", 0)
	require.NoError(t, err)
```

Append to `peer_test.go`:

```go
func TestAPI_HeartbeatReturnsPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	api := NewAPI(srv.server.Client(), srv.server.URL+"/v1", "test-device")
	resp, err := api.Heartbeat(context.Background(), "00000000-0000-0000-0000-000000000123", 0)
	require.NoError(t, err, "an empty body is a heartbeat with no pairings")
	assert.Empty(t, resp.PairingShortIDs)

	srv.heartbeatShortIDs.Store(&[]string{"1111111111111111"})
	resp, err = api.Heartbeat(context.Background(), "00000000-0000-0000-0000-000000000123", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"1111111111111111"}, resp.PairingShortIDs)
}

func TestAPI_RegisterReturnsPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	srv.registerResp.PairingShortIDs = []string{"2222222222222222"}
	api := NewAPI(srv.server.Client(), srv.server.URL+"/v1", "test-device")
	resp, err := api.Register(context.Background(), RegisterRequest{ExternalIP: "203.0.113.42", ExternalPort: 5698})
	require.NoError(t, err)
	assert.Equal(t, []string{"2222222222222222"}, resp.PairingShortIDs)
}

func TestClient_RegisterSendsServePool(t *testing.T) {
	require.NoError(t, settings.InitSettings(t.TempDir()))
	t.Cleanup(settings.Reset)
	srv := newStubServer(t)
	c := newTestClient(t, &fakeForwarder{}, &fakeBoxService{}, srv, func(cfg *Config) {
		cfg.HeartbeatInterval = time.Hour
	})
	ctx := context.Background()

	require.NoError(t, c.Start(ctx))
	assert.True(t, srv.lastRegisterReq.Load().(RegisterRequest).ServePool, "unset means help others too")
	require.NoError(t, c.Stop(ctx))

	require.NoError(t, settings.Set(settings.PeerServePoolKey, false))
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Stop(ctx) })
	assert.False(t, srv.lastRegisterReq.Load().(RegisterRequest).ServePool)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/radiance-helper-direct && go test ./peer/ -run 'TestAPI_|TestClient_RegisterSendsServePool' -v`
Expected: the build fails with `undefined: HeartbeatResponse`, `resp.PairingShortIDs undefined` and `undefined: settings.PeerServePoolKey`.

- [ ] **Step 4: Implement**

In `common/settings/settings.go`, add after `PeerManualPortKey`:

```go
	// PeerServePoolKey says whether the peer proxy also serves the general
	// pool ("help others too") rather than only its helper pairings. Unset
	// means true.
	PeerServePoolKey _key = "peer_serve_pool" // bool (unset = true)
```

In `peer/api.go`, add `"errors"` to the imports and replace the request and response types and `Heartbeat`:

```go
type RegisterRequest struct {
	ExternalIP   string `json:"external_ip"`
	ExternalPort uint16 `json:"external_port"`
	InternalPort uint16 `json:"internal_port"`
	// ServePool says whether the route also serves the general pool. False
	// keeps it to this device's helper pairings.
	ServePool bool `json:"serve_pool"`
}

type RegisterResponse struct {
	RouteID                  string `json:"route_id"`
	ServerConfig             string `json:"server_config"`
	HeartbeatIntervalSeconds int64  `json:"heartbeat_interval_seconds"`
	// PairingShortIDs are the short IDs, 16 hex characters each, the inbound
	// must accept for this device's helper pairings, beside the launch
	// config's own.
	PairingShortIDs []string `json:"pairing_short_ids"`
}

// HeartbeatResponse carries the current pairing short IDs. The peer replaces
// its pairing set with them, so an ended pairing drops out here.
type HeartbeatResponse struct {
	PairingShortIDs []string `json:"pairing_short_ids"`
}
```

```go
func (a *API) Heartbeat(ctx context.Context, routeID string, activeClients int) (*HeartbeatResponse, error) {
	req := HeartbeatRequest{RouteID: routeID, ActiveClients: activeClients}
	var resp HeartbeatResponse
	if err := a.do(ctx, http.MethodPost, "/peer/heartbeat", req, &resp); err != nil {
		return nil, fmt.Errorf("heartbeat: %w", err)
	}
	return &resp, nil
}
```

Keep `Heartbeat`'s doc comment, and add one sentence to it: "It returns the pairing short IDs the inbound must accept."

In `do`, accept an empty body. Servers that predate `PeerHeartbeatResponse` send none:

```go
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("decode response: %w", err)
		}
	}
```

In `peer/peer.go`, add after `pickInternalPort`:

```go
// servePool reports whether this peer also serves the general pool. Unset
// means yes, matching the server's default for clients that send nothing.
func servePool() bool {
	if settings.Get(settings.PeerServePoolKey) == nil {
		return true
	}
	return settings.GetBool(settings.PeerServePoolKey)
}
```

Add `ServePool: servePool(),` to the `RegisterRequest` literal in `Start` and to the one in `rotateCreds`, so a changed setting takes effect at the next registration. In `heartbeatLoop`, change `err := c.cfg.API.Heartbeat(hbCtx, routeID, c.ActiveClients())` to:

```go
			_, err := c.cfg.API.Heartbeat(hbCtx, routeID, c.ActiveClients())
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./peer/ ./common/settings/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add common/settings/settings.go peer/api.go peer/peer.go peer/peer_test.go
git commit -m "peer: send serve_pool and read pairing short IDs from register and heartbeat"
```

---

### Task 11: radiance — push pairing short IDs onto the running inbound

**Repo / worktree:** radiance, `~/go/src/github.com/getlantern/radiance-helper-direct`

**Files:**
- Modify: `peer/peer.go`
- Modify: `peer/peer_test.go` (`fakeBoxService` records short-ID updates)
- Create: `peer/shortids_test.go`

**Interfaces:**
- Consumes: lantern-box `(*samizdat.Inbound).UpdateShortIDs` (Task 2, through `*sbox.Box.Inbound().Get("samizdat-in")`); `API.Heartbeat` and `RegisterResponse.PairingShortIDs` (Task 10); test helpers `newStubServer`, `newTestClient`, `fakeForwarder`, `portBoxes` and `minimalValidLaunchCfg`.
- Produces:
  - `const peerInboundTag = "samizdat-in"`
  - `type shortIDUpdater interface{ UpdateShortIDs(ids []string) error }`
  - `func samizdatInbound(b boxService) (shortIDUpdater, bool)`
  - `func inboundShortIDs(options string) []string`
  - `func mergeShortIDs(base, pairing []string) []string`
  - `func (c *Client) applyShortIDs(b boxService, base, pairing []string)`
  - `func (c *Client) applyCurrentShortIDs(pairing []string)`
  - Field `Client.baseShortIDs []string`, guarded by `c.mu`.

- [ ] **Step 1: Write the failing tests**

In `peer_test.go`, replace `fakeBoxService` and its two methods with:

```go
type fakeBoxService struct {
	startErr  error
	closeErr  error
	started   atomic.Bool
	closed    atomic.Bool
	gotConfig string

	idsMu    sync.Mutex
	shortIDs [][]string
}

func (b *fakeBoxService) Start() error {
	if b.startErr != nil {
		return b.startErr
	}
	b.started.Store(true)
	return nil
}

func (b *fakeBoxService) Close() error {
	b.closed.Store(true)
	return b.closeErr
}

// UpdateShortIDs records each set, as the samizdat inbound would apply it.
func (b *fakeBoxService) UpdateShortIDs(ids []string) error {
	b.idsMu.Lock()
	defer b.idsMu.Unlock()
	b.shortIDs = append(b.shortIDs, append([]string(nil), ids...))
	return nil
}

// lastShortIDs is the set most recently applied, or nil.
func (b *fakeBoxService) lastShortIDs() []string {
	b.idsMu.Lock()
	defer b.idsMu.Unlock()
	if len(b.shortIDs) == 0 {
		return nil
	}
	return b.shortIDs[len(b.shortIDs)-1]
}
```

`peer/shortids_test.go`:

```go
package peer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBaseShortID = "0011223344556677"

// launchCfgWithShortID is minimalValidLaunchCfg with the base short ID a real
// launch config carries.
func launchCfgWithShortID() string {
	return strings.Replace(minimalValidLaunchCfg,
		`{"type":"samizdat","tag":"samizdat-in"}`,
		`{"type":"samizdat","tag":"samizdat-in","short_ids":["`+testBaseShortID+`"]}`, 1)
}

func TestMergeShortIDs(t *testing.T) {
	assert.Equal(t,
		[]string{testBaseShortID, "1111111111111111", "aaaaaaaaaaaaaaaa"},
		mergeShortIDs([]string{testBaseShortID},
			[]string{"1111111111111111", "bad", "1111111111111111", testBaseShortID, "AAAAAAAAAAAAAAAA", "zzzzzzzzzzzzzzzz"}),
		"base first; invalid, duplicate and base-equal pairing IDs dropped; hex lowercased")
	assert.Equal(t, []string{testBaseShortID}, mergeShortIDs([]string{testBaseShortID}, nil))
}

func TestInboundShortIDs(t *testing.T) {
	assert.Equal(t, []string{testBaseShortID}, inboundShortIDs(launchCfgWithShortID()))
	assert.Empty(t, inboundShortIDs(minimalValidLaunchCfg))
	assert.Empty(t, inboundShortIDs("not json"))
}

func TestClient_StartAddsRegisterPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	srv.registerResp.ServerConfig = launchCfgWithShortID()
	srv.registerResp.PairingShortIDs = []string{"1111111111111111"}
	b := &fakeBoxService{}
	c := newTestClient(t, &fakeForwarder{}, b, srv, func(cfg *Config) { cfg.HeartbeatInterval = time.Hour })
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	assert.Equal(t, []string{testBaseShortID, "1111111111111111"}, b.lastShortIDs())
}

func TestClient_WithoutBaseShortIDLeavesInboundAlone(t *testing.T) {
	srv := newStubServer(t) // minimalValidLaunchCfg has no short_ids
	srv.registerResp.PairingShortIDs = []string{"1111111111111111"}
	b := &fakeBoxService{}
	c := newTestClient(t, &fakeForwarder{}, b, srv, func(cfg *Config) { cfg.HeartbeatInterval = time.Hour })
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	assert.Nil(t, b.lastShortIDs(), "never replace the set without the base ID the verifier uses")
}

func TestClient_HeartbeatReplacesPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	srv.registerResp.ServerConfig = launchCfgWithShortID()
	srv.heartbeatShortIDs.Store(&[]string{"2222222222222222"})
	b := &fakeBoxService{}
	c := newTestClient(t, &fakeForwarder{}, b, srv, func(cfg *Config) { cfg.HeartbeatInterval = 20 * time.Millisecond })
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	require.Eventually(t, func() bool {
		return slices.Equal(b.lastShortIDs(), []string{testBaseShortID, "2222222222222222"})
	}, 2*time.Second, 10*time.Millisecond)

	// An ended pairing drops out at the next heartbeat; the base ID stays.
	srv.heartbeatShortIDs.Store(&[]string{})
	require.Eventually(t, func() bool {
		return slices.Equal(b.lastShortIDs(), []string{testBaseShortID})
	}, 2*time.Second, 10*time.Millisecond)
}

func TestClient_FailedHeartbeatKeepsPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	srv.registerResp.ServerConfig = launchCfgWithShortID()
	srv.registerResp.PairingShortIDs = []string{"1111111111111111"}
	srv.heartbeatStatus = http.StatusInternalServerError
	b := &fakeBoxService{}
	c := newTestClient(t, &fakeForwarder{}, b, srv, func(cfg *Config) { cfg.HeartbeatInterval = 20 * time.Millisecond })
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.Eventually(t, func() bool { return srv.heartbeatCount.Load() >= 3 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{testBaseShortID, "1111111111111111"}, b.lastShortIDs(),
		"a failed heartbeat says nothing about pairings, so the last set stays")
}

func TestClient_RotationReappliesPairingShortIDs(t *testing.T) {
	srv := newStubServer(t)
	srv.registerResp.ServerConfig = launchCfgWithShortID()
	srv.registerResp.PairingShortIDs = []string{"3333333333333333"}
	port := &portBoxes{}
	c := newTestClient(t, &fakeForwarder{externalIP: "203.0.113.42"}, &fakeBoxService{}, srv, func(cfg *Config) {
		cfg.CredRotationInterval = 50 * time.Millisecond
		cfg.HeartbeatInterval = time.Hour
		cfg.BuildBoxService = port.build
	})
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	require.Eventually(t, func() bool {
		port.mu.Lock()
		defer port.mu.Unlock()
		if len(port.boxes) < 2 {
			return false
		}
		return slices.Equal(port.boxes[len(port.boxes)-1].lastShortIDs(), []string{testBaseShortID, "3333333333333333"})
	}, 2*time.Second, 10*time.Millisecond, "a rotated box starts with the base ID only, so the pairing IDs must be pushed again")
}

// realSamizdatOptions is a launch config the real lantern-box samizdat inbound
// accepts, listening on a free loopback port.
func realSamizdatOptions(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"example.com"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	priv := make([]byte, 32)
	_, err = rand.Read(priv)
	require.NoError(t, err)
	b, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"type": "samizdat", "tag": "samizdat-in", "listen": "127.0.0.1", "listen_port": 0,
		"private_key":       hex.EncodeToString(priv),
		"short_ids":         []string{testBaseShortID},
		"cert_pem":          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"key_pem":           string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		"masquerade_domain": "example.com",
	}}})
	require.NoError(t, err)
	return string(b)
}

// TestSamizdatInbound_RealBox checks the seam the fakes stand in for: a
// sing-box built by defaultBuildBoxService exposes lantern-box's samizdat
// inbound, and its UpdateShortIDs is reachable.
func TestSamizdatInbound_RealBox(t *testing.T) {
	b, err := defaultBuildBoxService(context.Background(), realSamizdatOptions(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	u, ok := samizdatInbound(b)
	require.True(t, ok, "the box's samizdat-in inbound must expose UpdateShortIDs")
	require.NoError(t, u.UpdateShortIDs([]string{testBaseShortID, "4444444444444444"}))
	require.Error(t, u.UpdateShortIDs(nil), "lantern-box's own validation runs: an empty set is refused")
}
```

`portBox` embeds `*fakeBoxService`, so the rotation test's boxes record updates without changes to `rotation_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/go/src/github.com/getlantern/radiance-helper-direct && go test -race ./peer/ -run 'TestMergeShortIDs|TestInboundShortIDs|PairingShortIDs|WithoutBaseShortID|TestSamizdatInbound_RealBox' -v`
Expected: the build fails with `undefined: mergeShortIDs`, `undefined: inboundShortIDs` and `undefined: samizdatInbound`.

- [ ] **Step 3: Implement**

In `peer/peer.go`, add these imports: `"encoding/hex"`, `"github.com/sagernet/sing-box/adapter"` and `lbO "github.com/getlantern/lantern-box/option"`.

Add the field to `Client`, after `boxOptions string`:

```go
	// baseShortIDs are the inbound's short IDs from the current launch config.
	// They are always kept when pairing short IDs are applied, because
	// lantern-cloud's verifier and pool clients authenticate with them.
	baseShortIDs []string
```

Add after `servePool`:

```go
// peerInboundTag is the tag lantern-cloud gives the peer's samizdat inbound.
const peerInboundTag = "samizdat-in"

// shortIDUpdater is lantern-box's samizdat inbound's runtime short-ID setter.
type shortIDUpdater interface {
	UpdateShortIDs(ids []string) error
}

// samizdatInbound finds b's samizdat inbound. A test double can implement
// shortIDUpdater itself; a real box is searched by tag.
func samizdatInbound(b boxService) (shortIDUpdater, bool) {
	if u, ok := b.(shortIDUpdater); ok {
		return u, true
	}
	sb, ok := b.(interface{ Inbound() adapter.InboundManager })
	if !ok {
		return nil, false
	}
	in, found := sb.Inbound().Get(peerInboundTag)
	if !found {
		return nil, false
	}
	u, ok := in.(shortIDUpdater)
	return u, ok
}

// inboundShortIDs returns the samizdat inbound's short IDs from a launch
// config, or nil if it has none or cannot be read.
func inboundShortIDs(options string) []string {
	opts, err := json.UnmarshalExtendedContext[option.Options](box.Context(context.Background()), []byte(options))
	if err != nil {
		return nil
	}
	for _, in := range opts.Inbounds {
		if in.Tag != peerInboundTag {
			continue
		}
		if o, ok := in.Options.(*lbO.SamizdatInboundOptions); ok {
			return o.ShortIDs
		}
	}
	return nil
}

// mergeShortIDs is base followed by the valid pairing IDs not already
// present, lowercased. An invalid ID is dropped rather than passed on,
// because lantern-box refuses a whole set over one bad entry.
func mergeShortIDs(base, pairing []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base)+len(pairing))
	for _, id := range base {
		seen[strings.ToLower(id)] = true
	}
	for _, id := range pairing {
		id = strings.ToLower(id)
		if b, err := hex.DecodeString(id); err != nil || len(b) != 8 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// applyShortIDs sets b's inbound to accept base plus the pairing short IDs.
// Without a base ID it changes nothing: replacing the set would cut off
// lantern-cloud's verifier and every pool client. The IDs are credentials and
// are never logged.
func (c *Client) applyShortIDs(b boxService, base, pairing []string) {
	if len(base) == 0 {
		slog.Warn("peer: launch config has no samizdat short IDs; not applying pairing short IDs")
		return
	}
	u, ok := samizdatInbound(b)
	if !ok {
		slog.Warn("peer: samizdat inbound cannot update short IDs; paired clients cannot connect")
		return
	}
	ids := mergeShortIDs(base, pairing)
	if err := u.UpdateShortIDs(ids); err != nil {
		slog.Warn("peer: updating samizdat short IDs failed", "err", err)
		return
	}
	slog.Debug("peer: samizdat short IDs updated", "pairing_ids", len(ids)-len(base))
}

// applyCurrentShortIDs applies a heartbeat's pairing short IDs to the box
// serving now. A rotation that swaps the box applies its own register
// response's IDs to the new one.
func (c *Client) applyCurrentShortIDs(pairing []string) {
	c.mu.Lock()
	b, base := c.box, c.baseShortIDs
	c.mu.Unlock()
	if b == nil {
		return
	}
	c.applyShortIDs(b, base, pairing)
}
```

In `Start`, insert directly after the `box.Start()` error check:

```go
	// Paired inviters authenticate with per-pairing short IDs that
	// lantern-cloud rotates. The launch config's own ID stays.
	baseShortIDs := inboundShortIDs(options)
	c.applyShortIDs(box, baseShortIDs, regResp.PairingShortIDs)
```

In `Start`'s success block (`c.mu.Lock(); c.active = true; ...`), add `c.baseShortIDs = baseShortIDs` after `c.boxOptions = options`.

In `stopHeld`, add `c.baseShortIDs = nil` after `c.boxOptions = ""`.

In `heartbeatLoop`, change the call to keep the response, and apply it on success. The block becomes:

```go
			hbCtx, cancel := context.WithTimeout(ctx, c.cfg.HeartbeatTimeout)
			resp, err := c.cfg.API.Heartbeat(hbCtx, routeID, c.ActiveClients())
			cancel()
			if err != nil {
```

The existing body of `if err != nil { ... }` is unchanged. Immediately after its closing brace, still inside `case <-t.C:`, add:

```go
			if err == nil {
				// Replaces the pairing set, so an ended pairing stops working
				// here. A failed heartbeat says nothing about pairings, so the
				// last set stays.
				c.applyCurrentShortIDs(resp.PairingShortIDs)
			}
```

In `rotateCreds`, insert after the `buildAndStartWithRetry` error check:

```go
	newBase := inboundShortIDs(options)
```

In the final swap block, add `c.baseShortIDs = newBase` after `c.boxOptions = options`. Directly after that block's `c.mu.Unlock()`, before the `if !sameRoute {` deregister, add:

```go
	c.applyShortIDs(newBox, newBase, regResp.PairingShortIDs)
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./peer/ -v && make test`
Expected: PASS for the new tests and every existing peer and rotation test.

- [ ] **Step 5: Commit and open the PR**

```bash
git add peer/peer.go peer/peer_test.go peer/shortids_test.go
git commit -m "peer: accept helper pairings' short IDs on the samizdat inbound"
```

Open the PR with `skills:create-pr`.

- [ ] **Step 6: [Human] Merge and release.** Merge after the lantern-cloud PR (Task 9) is deployed. Until then the server never sends `pairing_short_ids`, and `serve_pool` is ignored by older servers that drop unknown fields. Shipping radiance in a client release, and then turning on `helper_invites` for the pilot, are production steps.

---

## Self-review

### Spec coverage: Section 2, "Direct mode"

| Spec bullet | How this plan implements it | Tasks |
|---|---|---|
| A volunteer's route gets an **audience**: its pairing IDs, plus `everyone` if "help others too" is on | The pairing part is derived, not stored: `helper_pairings` rows where the route owner is the volunteer. That join feeds `ListHelperRoutesForInviter` (inviter side) and `volunteerPairingShortIDs` (volunteer side). `everyone` is `peer_routes.serve_pool`, sent by radiance from `peer_serve_pool` (default true). | 4, 5, 6, 10 |
| The inviter's `/v1/config-new` always includes her paired volunteers' online routes, separately from the bandit's selections | `helperOutbounds` appends a `helper-<route>` outbound per pairing with a live route, independent of bandit, hash ring or force_track. It is dropped from the pool's share if the pool also picked it. It is checked end to end. | 4, 7, 8, 9 |
| A route without `everyone` is left out of the bandit catalog; with it, it enters the pool as today | `AND serve_pool` in `GetAllPeerRoutesForTrack` (catalog and force_track) and in `GetPeerRouteConfigsForRoutes` (assignment from a stale catalog). The arm-transition count excludes private routes too. Absent means true. | 4, 5 |
| The per-peer cap reserves slots for paired users first, so the pool cannot crowd them out | Paired outbounds never call `ReservePeerSlot` and are never filtered by `active_clients`. Volunteers count paired users in `active_clients`, so they fill the cap against the pool. Partial; see "Not fully covered". | 8 |
| Paired clients authenticate with per-pairing credentials issued to both sides, rotated on the ticket lifetime; no samizdat protocol change; revocation stops renewal | The credential is the pairing short ID: `HMAC(K_sid, pairing, epoch)` with 15-minute epochs. The inviter gets epoch `e` in her config; the volunteer gets `e-1..e+1` on register and heartbeat. The wire protocol is unchanged. Only a server-side setter is added (`SetShortIDs`), reached through `UpdateShortIDs`. A deleted pairing stops producing IDs. | 1, 2, 3, 6, 8, 11 |

### Spec coverage: "Revocation" (direct-mode half)

| Spec statement | Implementation | Tasks |
|---|---|---|
| lantern-cloud stops issuing credentials for the pairing | `EndPairing` (plan 1) deletes the row. Both derivations read `helper_pairings`, so no further short ID is handed out on either side. | 6, 8 |
| The route drops from Sara's config at the next fetch | `ListHelperRoutesForInviter` joins on the live pairing row. | 4, 8 |
| The volunteer's inbound rejects her pairing credentials | The volunteer's next heartbeat, within 150 s, replaces the pairing set. samizdat refuses the removed IDs and closes sessions already using them. If the pairing lookup fails, the heartbeat fails without extending the route, so a stale set cannot outlive `peerRouteTTL`. | 1, 6, 11 |
| Access ends within one ticket lifetime (15 minutes) at most | It ends within one heartbeat interval (≤150 s plus RTT), well inside 15 minutes. Tested at each layer: samizdat revokes a live session, the heartbeat drops ended pairings, and radiance replaces the set. | 1, 6, 11 |

Section 2's "Client behavior" needs no change: a paired outbound is an ordinary sing-box outbound that the client's URL test ranks and fails over like any other, and a same-tag outbound with new options is recreated on the 10-minute poll (`vpn/tunnel.go` `removeDuplicates`).

### Not fully covered in this plan

1. **"Reserves slots for paired users first."** There is no hard reservation. Paired users are exempt from the cap and count against it, so the pool cannot push them out. But a volunteer whose pool clients are already connected can briefly carry more than `MaxClientsPerPeer` (10): its pool clients plus its connected paired users. The spec's limits table says volunteers stay at "at most 10 concurrent clients". Holding that strictly would need the inbound to refuse pool clients while paired ones wait, which samizdat cannot tell apart before the handshake. Left for a decision after the pilot.
2. **"Within seconds where a revocation can be pushed."** Direct mode has no push channel to the volunteer, so revocation takes up to one heartbeat (≤150 s). Push belongs with freddie rooms in plan 3.
3. **Port.** The brief said to mirror the pool's port. The paired outbound deliberately uses `peer_routes.port` (external) rather than the pool's `launchCfg.ListenPort` (internal), because only the external port is proven reachable. The pool path's use of the internal port is a pre-existing bug when UPnP maps different ports. It is flagged here, not fixed.

### Deliberately deferred

- **Plan 3:** freddie rooms and the Unbounded mode of pairings; ticket-key rotation (a `kid` plus a key list), with freddie as the first verifier; push revocation. Rotating the ticket seed also rotates `K_sid`, which only renews every pairing short ID at the next fetch and heartbeat. Plan 3 must keep `K_sid` derived from the current seed.
- **Plan 4 (app):** the "Help others too" toggle and its IPC exposure (`settings.PeerServePoolKey` is not yet in `ipc/server.go`'s non-admin allowlist). Re-registering immediately when the toggle changes: until then, the change applies at the next Start or the hourly credential rotation. The inviter's helper list with online status. Account-bound pairings.
- **Plan 5:** reward eligibility from heartbeats and config-fetch country; the landing page.
- **Rollout:** turning on `helper_invites` for the pilot, a production flag change made after Tasks 9 and 11 ship.

### Placeholder and consistency check

- No step says "TBD", "add error handling" or "similar to Task N". The one value not known in advance, the lantern-box tag (`LB_TAG`), comes with the command that produces it (Task 2 Step 7).
- Names are consistent across tasks: `PeerSIDKey`, `PeerSIDEpoch`, `PeerShortID` and `AcceptedPeerShortIDs` (Task 3) are used unchanged in Tasks 6, 8 and 9. `helperPeerSIDKeyFor`, `helperNow`, `helperOutbound`, `helperOutbounds` and `dropPairedPoolConfigs` are defined once in `helper_direct.go`. The proto fields `ServePool` and `PairingShortIds` match the wire table. In radiance, `ServePool`, `PairingShortIDs`, `HeartbeatResponse`, `PeerServePoolKey` and `UpdateShortIDs` match lantern-box Task 2.
