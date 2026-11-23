# Room TURN Relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Room (personal proxy) pairings get Cloudflare TURN servers in their WebRTC ICE configuration, so a pairing that can't connect directly connects through a relay, and Freddie counts how many connections used one.

**Architecture:** Freddie calls an optional hook for ICE servers on every genesis carrying a verified room ticket, stamps them on the per-room genesis messages and, when a room consumer took the genesis, on the offer it returns to the producer. Each peer adds them to its peer connection with `SetConfiguration` before ICE gathering starts. The producer reports whether its connected attempt's selected pair was relayed; Freddie labels `freddie.producer.outcomes` with it. lantern-cloud implements the hook with Cloudflare credentials from Vault.

**Tech Stack:** Go, pion/webrtc v4.2.25 (native and wasm), pion/turn v5 (tests), OpenTelemetry metrics, Vault KV v2.

**Spec:** `docs/superpowers/specs/2026-11-12-room-turn-relay-design.md`

## Global Constraints

- Pool pairings must be unchanged: no ICE servers on any pool message, and a message without `ICEServers` is byte-identical to today's (`json:",omitempty"`).
- A nil `RoomICEServers` hook, or an empty result, changes nothing anywhere.
- Never log, trace, or put on a span: ICE server URLs, usernames, credentials, room/pairing IDs, IPs.
- Signaling must never wait on Cloudflare: the lantern-cloud hook returns cached data only.
- Every clientcore change must build for wasm: `GOOS=js GOARCH=wasm go build ./clientcore/ ./cmd/...` (whatever `cmd` packages build for js today).
- Failure to add ICE servers is logged at debug and the attempt continues without TURN.
- Metric label values are fixed enums (`relay`, `direct`, `unreported`).
- Go: run `go mod tidy` after any `go get`; commit `go.mod`/`go.sum` with the change.
- Commits end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE
  ```
- Tests: `go test -race -count=1 ./common/... ./freddie/... ./clientcore/...` must pass (CI runs `go test -race ./...`).

Repo/worktree for Tasks 1–4: `/Users/afisk/go/src/github.com/getlantern/unbounded-turn-relay`, branch `fisk/room-turn-relay` (module `github.com/getlantern/broflake`). Task 5 is in `/Users/afisk/go/src/github.com/getlantern/lantern-cloud` on a new branch from `origin/main`, in a new worktree.

---

### Task 1: Protocol fields in `common`

**Files:**
- Modify: `common/resource.go` (struct `SignalMsg`, ~line 143)
- Modify: `common/outcome.go` (field constants, normalizers)
- Test: `common/resource_test.go`, `common/outcome_test.go`

**Interfaces:**
- Produces: `SignalMsg.ICEServers []webrtc.ICEServer` (`json:",omitempty"`); `const PrevPathField = "prev-path"`; `const PathRelay = "relay"`, `PathDirect = "direct"`; `func OutcomePath(s string) string` returning `relay`, `direct`, or `unreported` (empty or unknown input → `unreported`).

- [ ] **Step 1: Write failing tests**

In `common/resource_test.go`:

```go
func TestSignalMsg_ICEServersRoundTrip(t *testing.T) {
	plain, err := json.Marshal(SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "ICEServers") {
		t.Fatalf("a message without ICE servers must not carry the field: %s", plain)
	}
	in := SignalMsg{ReplyTo: "r", Type: SignalMsgGenesis, Payload: "{}", ICEServers: []webrtc.ICEServer{
		{URLs: []string{"turns:turn.example:443?transport=tcp"}, Username: "u", Credential: "c"},
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := DecodeSignal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.ICEServers, in.ICEServers) {
		t.Fatalf("got %+v, want %+v", out.ICEServers, in.ICEServers)
	}
}
```

In `common/outcome_test.go`:

```go
func TestOutcomePath(t *testing.T) {
	for in, want := range map[string]string{"relay": "relay", "direct": "direct", "": "unreported", "bogus": "unreported"} {
		if got := OutcomePath(in); got != want {
			t.Errorf("OutcomePath(%q) = %q, want %q", in, got, want)
		}
	}
}
```

Add any missing imports (`encoding/json`, `reflect`, `strings`, `github.com/pion/webrtc/v4`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./common/ -run 'TestSignalMsg_ICEServersRoundTrip|TestOutcomePath'`
Expected: compile failure (`ICEServers` / `OutcomePath` undefined).

- [ ] **Step 3: Implement**

In `SignalMsg`, after `Pairing`:

```go
	// ICEServers is set by Freddie, with Room, on a genesis message it delivered through a room,
	// and on the offer it returns to a producer whose genesis a room consumer took. The peer adds
	// them (TURN relays) to its peer connection before gathering, so a pairing that can't connect
	// directly can connect through a relay. Producers and consumers cannot set it. Omitted
	// otherwise, so every other message is unchanged on the wire. Never logged: it carries
	// credentials.
	ICEServers []webrtc.ICEServer `json:",omitempty"`
```

In `common/outcome.go`, add to the field constants block:

```go
	// PrevPathField says whether a connected attempt's selected candidate pair went through a TURN
	// relay: PathRelay or PathDirect. Only sent with OutcomeConnected.
	PrevPathField = "prev-path"
```

and after the outcomes:

```go
// Values of PrevPathField.
const (
	PathRelay  = "relay"
	PathDirect = "direct"
)

// OutcomePath maps a reported path onto the path label: relay, direct, or unreported when the
// producer sent nothing (an attempt that didn't connect, or a producer that predates the field)
// or something unknown.
func OutcomePath(s string) string {
	switch s {
	case PathRelay, PathDirect:
		return s
	default:
		return "unreported"
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./common/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add common/
git commit -m "common: ICE servers on SignalMsg and a reported connection path" # plus trailers
```

---

### Task 2: Freddie stamps room ICE servers and labels the path

**Files:**
- Modify: `freddie/freddie.go` (struct `Freddie` ~line 311; `handleSignalPost` ~lines 816–960)
- Modify: `freddie/outcomes.go` (`recordProducerOutcome`)
- Create: `freddie/roomice_test.go`

**Interfaces:**
- Consumes: `common.SignalMsg.ICEServers`, `common.PrevPathField`, `common.OutcomePath` (Task 1).
- Produces: field `Freddie.RoomICEServers func(ctx context.Context) []webrtc.ICEServer`; label `path` on `freddie.producer.outcomes`.

- [ ] **Step 1: Write failing tests** in `freddie/roomice_test.go`

The tests drive Freddie over HTTP with the existing helpers (`newRoomKeys`, `startRoomServer`, `testServer.subscribe`, `waitForConsumers`, `roomGenesisForm`, `pairingP`, `pairingQ`, `ticketOK` in `rooms_test.go`/`freddie_test.go`). Add a helper that posts a genesis and waits for its response body, and one that posts an offer to a genesis:

```go
var testICE = []webrtc.ICEServer{{URLs: []string{"turns:turn.example:443?transport=tcp"}, Username: "u", Credential: "c"}}

// genesisResponse posts a genesis with form's fields and returns Freddie's response body: the
// offer a consumer sent, or "" if none did within the request's lifetime.
func (s *testServer) genesisResponse(form url.Values) chan string {
	form.Set("data", "{}")
	if form.Get("send-to") == "" {
		form.Set("send-to", common.SignalSendToGenesis)
	}
	form.Set("type", strconv.Itoa(int(common.SignalMsgGenesis)))
	out := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url+"/v1/signal", strings.NewReader(form.Encode()))
		if err != nil {
			out <- ""
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(common.VersionHeader, common.Version)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- ""
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		out <- strings.TrimSpace(string(body))
	}()
	return out
}

// answerGenesis waits for a genesis on consumer, answers it with an offer, and returns the
// genesis as the consumer decoded it. It answers the offer so the offer request returns.
func (s *testServer) answerGenesis(consumer chan string) common.SignalMsg {
	s.t.Helper()
	var g common.SignalMsg
	select {
	case raw := <-consumer:
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			s.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the genesis never reached the consumer")
	}
	go s.postOffer(g.ReplyTo, "RU", common.Version)
	return g
}

func decodeOffer(t *testing.T, raw string) common.SignalMsg {
	t.Helper()
	var m common.SignalMsg
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decoding the offer the producer got: %v", err)
	}
	return m
}

func TestRoomICE_StampedOnRoomGenesisAndOffer(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	var calls atomic.Int32
	s.f.RoomICEServers = func(context.Context) []webrtc.ICEServer { calls.Add(1); return testICE }
	paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
	s.waitForConsumers(1)

	resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
	g := s.answerGenesis(paired)
	if !reflect.DeepEqual(g.ICEServers, testICE) {
		t.Fatalf("room genesis ICE servers: got %+v", g.ICEServers)
	}
	offer := decodeOffer(t, <-resp)
	if offer.Type != common.SignalMsgOffer || !reflect.DeepEqual(offer.ICEServers, testICE) {
		t.Fatalf("offer to the producer: type %v, ICE servers %+v", offer.Type, offer.ICEServers)
	}
	if calls.Load() != 1 {
		t.Fatalf("the hook ran %d times for one genesis, want 1", calls.Load())
	}
}

func TestRoomICE_NotOnPool(t *testing.T) {
	k := newRoomKeys(t)
	s := startRoomServer(t, k)
	s.f.RoomICEServers = func(context.Context) []webrtc.ICEServer { return testICE }
	pool := s.subscribe("RU", "")
	s.waitForConsumers(1)

	for name, form := range map[string]url.Values{
		"no rooms": {},
		// Nobody is in Q's room, so the genesis falls through to the pool.
		"no room consumer": roomGenesisForm([]string{k.ticket(pairingQ, helperticket.RoleVolunteer, ticketOK)}, nil),
	} {
		resp := s.genesisResponse(form)
		g := s.answerGenesis(pool)
		if g.ICEServers != nil {
			t.Errorf("%s: pool genesis carried ICE servers", name)
		}
		if offer := decodeOffer(t, <-resp); offer.ICEServers != nil {
			t.Errorf("%s: pool offer carried ICE servers", name)
		}
		s.waitForConsumers(1)
	}
}

func TestRoomICE_NilHookAndEmptyResult(t *testing.T) {
	for name, hook := range map[string]func(context.Context) []webrtc.ICEServer{
		"nil hook":     nil,
		"empty result": func(context.Context) []webrtc.ICEServer { return nil },
	} {
		k := newRoomKeys(t)
		s := startRoomServer(t, k)
		s.f.RoomICEServers = hook
		paired := s.subscribe("RU", "", k.ticket(pairingP, helperticket.RoleInviter, ticketOK))
		s.waitForConsumers(1)
		resp := s.genesisResponse(roomGenesisForm([]string{k.ticket(pairingP, helperticket.RoleVolunteer, ticketOK)}, nil))
		g := s.answerGenesis(paired)
		raw := <-resp
		if g.ICEServers != nil || strings.Contains(raw, "ICEServers") {
			t.Errorf("%s: ICE servers stamped", name)
		}
		s.hangUp()
	}
}
```

For the path label, add to the same file a test that posts a genesis carrying `prev-outcome=connected` and `prev-path=relay` and checks the label, using the metric reader the existing outcome tests use (see `freddie/outcomes_test.go` for how `freddie.producer.outcomes` labels are read; reuse that helper, e.g. `outcomeLabels(t, form, version, "path")` if present, otherwise copy the pattern used there):

```go
func TestRecordProducerOutcome_LabelsPath(t *testing.T) {
	for reported, want := range map[string]string{"relay": "relay", "direct": "direct", "": "unreported", "x": "unreported"} {
		form := url.Values{common.PrevOutcomeField: {common.OutcomeConnected}}
		if reported != "" {
			form.Set(common.PrevPathField, reported)
		}
		got := outcomeLabels(t, form, common.Version, "path")
		if got[want] != 1 || len(got) != 1 {
			t.Errorf("prev-path %q: got %v, want %s", reported, got, want)
		}
	}
}
```

If `outcomeLabels` doesn't exist on this branch, write it in `roomice_test.go` following the reader setup in `outcomes_test.go`: it posts a genesis with `form` and `VersionHeader: version`, collects `freddie.producer.outcomes`, and returns a map from the named label's value to the count.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./freddie/ -run 'TestRoomICE|TestRecordProducerOutcome_LabelsPath'`
Expected: compile failure (`RoomICEServers` undefined).

- [ ] **Step 3: Implement the hook and stamping**

Add the field to `Freddie` beside `RoomTicket`:

```go
	// RoomICEServers, if non-nil, returns ICE servers (TURN relays) for room pairings. Freddie
	// calls it once per genesis that carries a verified room ticket and stamps the result on the
	// genesis each room's consumers receive and, when a room consumer took the genesis, on the
	// offer it returns to the producer. It must not block: signaling waits on it. Nil, or an empty
	// result, leaves every message unchanged.
	RoomICEServers func(ctx context.Context) []webrtc.ICEServer
```

In `handleSignalPost`, in the genesis branch, after `rooms := f.verifyRoomTickets(...)`:

```go
		var roomICE []webrtc.ICEServer
		if len(rooms) > 0 && f.RoomICEServers != nil {
			roomICE = f.RoomICEServers(ctx)
		}
```

Set `ICEServers: roomICE` in the per-room `common.SignalMsg{...}` literal. After `matched, outcome := consumerTable.DeliverGenesis(...)`, remember what to stamp on the response:

```go
		if matched {
			offerICE = roomICE
		}
```

Declare `var offerICE []webrtc.ICEServer` before `if isGenesis {`. In the final `select`, replace `fmt.Fprintf(w, "%v\n", res)` with:

```go
	case res := <-reqChan:
		fmt.Fprintf(w, "%v\n", withICEServers(res, offerICE))
```

and add:

```go
// withICEServers returns the signaling message raw with ICEServers set to servers, or raw unchanged
// when servers is empty or raw isn't a SignalMsg. Only a room consumer can answer a genesis a room
// consumer took (DeliverGenesis skips the pool then), so the offer it stamps is a room pairing's.
func withICEServers(raw string, servers []webrtc.ICEServer) string {
	if len(servers) == 0 {
		return raw
	}
	var msg common.SignalMsg
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return raw
	}
	msg.ICEServers = servers
	out, err := json.Marshal(msg)
	if err != nil {
		return raw
	}
	return string(out)
}
```

Check `reqChan`'s element type: if it is not `string`, convert accordingly (it is printed with `%v`). Do not add the servers to any span or log.

- [ ] **Step 4: Implement the path label**

In `recordProducerOutcome`, add to `attrs`:

```go
		attribute.String("path", common.OutcomePath(r.Form.Get(common.PrevPathField))),
```

- [ ] **Step 5: Run tests**

Run: `go test -race -count=1 ./freddie/... ./common/...`
Expected: PASS, including existing tests (some may assert the exact label set of `freddie.producer.outcomes`; update them to include `path`).

- [ ] **Step 6: Commit**

```bash
git add freddie/
git commit -m "freddie: stamp room ICE servers on room geneses and their offers, label outcomes by path"
```

---

### Task 3: clientcore helpers: add ICE servers, read the selected path

**Files:**
- Create: `clientcore/turn.go`
- Create: `clientcore/turn_test.go` (native only: `//go:build !js`)

**Interfaces:**
- Consumes: `common.PathRelay`, `common.PathDirect` (Task 1).
- Produces:
  - `func addICEServers(pc *webrtc.PeerConnection, servers []webrtc.ICEServer) error` — no-op for empty `servers`; otherwise `SetConfiguration` with the current configuration's servers followed by `servers`. Must be called before `SetLocalDescription`.
  - `func selectedPath(pc *webrtc.PeerConnection) string` — `common.PathRelay` if either candidate of the selected pair has type `relay`, `common.PathDirect` if a pair is selected and neither is, `""` if none is selected or anything is nil or panics (the JS bindings can panic on browsers that lack the API).

- [ ] **Step 1: Write the failing test**

`clientcore/turn_test.go` runs a local pion TURN server and two peer connections built with the same constructors the FSMs use, forced to relay-only so the only way to connect is through TURN:

```go
//go:build !js

package clientcore

import (
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

func startTURN(t *testing.T) []webrtc.ICEServer {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	key := turn.GenerateAuthKey("u", "test", "p")
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			return key, username == "u"
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return []webrtc.ICEServer{{URLs: []string{"turn:" + pc.LocalAddr().String() + "?transport=udp"}, Username: "u", Credential: "p"}}
}

func TestAddICEServers_KeepsExisting(t *testing.T) {
	stun := []webrtc.ICEServer{{URLs: []string{"stun:stun.example:3478"}}}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: stun})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	turnSrv := []webrtc.ICEServer{{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"}}
	if err := addICEServers(pc, nil); err != nil {
		t.Fatal(err)
	}
	if got := pc.GetConfiguration().ICEServers; len(got) != 1 {
		t.Fatalf("an empty list must change nothing, got %+v", got)
	}
	if err := addICEServers(pc, turnSrv); err != nil {
		t.Fatal(err)
	}
	got := pc.GetConfiguration().ICEServers
	if len(got) != 2 || got[0].URLs[0] != stun[0].URLs[0] || got[1].URLs[0] != turnSrv[0].URLs[0] {
		t.Fatalf("got %+v, want STUN then TURN", got)
	}
}

func TestAddICEServers_ConnectsThroughRelay(t *testing.T) {
	ice := startTURN(t)
	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay})
	if err != nil {
		t.Fatal(err)
	}
	defer offerer.Close()
	answerer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay})
	if err != nil {
		t.Fatal(err)
	}
	defer answerer.Close()
	if selectedPath(offerer) != "" {
		t.Fatal("no pair is selected before connecting")
	}
	for _, pc := range []*webrtc.PeerConnection{offerer, answerer} {
		if err := addICEServers(pc, ice); err != nil {
			t.Fatal(err)
		}
	}

	opened := make(chan struct{})
	answerer.OnDataChannel(func(d *webrtc.DataChannel) { d.OnOpen(func() { close(opened) }) })
	if _, err := offerer.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(offerer)
	if err := offerer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := answerer.SetRemoteDescription(*offerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered = webrtc.GatheringCompletePromise(answerer)
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err := offerer.SetRemoteDescription(*answerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(15 * time.Second):
		t.Fatal("no data channel through the relay")
	}
	if got := selectedPath(answerer); got != common.PathRelay {
		t.Fatalf("selectedPath = %q, want relay", got)
	}
}
```

If pion's TURN server refuses loopback peers by default, configure its permission handler to allow them (check `turn.ServerConfig` in the module cache for a `PermissionHandler`-style field) rather than changing addresses. `pion/turn/v5` is already an indirect dependency; `go mod tidy` after adding the import will make it direct.

- [ ] **Step 2: Run to see it fail**

Run: `go test ./clientcore/ -run 'TestAddICEServers'`
Expected: compile failure (`addICEServers`, `selectedPath` undefined).

- [ ] **Step 3: Implement** `clientcore/turn.go` (no build tag; it must compile for wasm)

```go
package clientcore

import (
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// addICEServers adds servers (TURN relays Freddie sent for a room pairing) to pc's ICE servers.
// ICE gathers with the new servers only if this runs before SetLocalDescription. An empty list
// changes nothing.
func addICEServers(pc *webrtc.PeerConnection, servers []webrtc.ICEServer) error {
	if len(servers) == 0 {
		return nil
	}
	config := pc.GetConfiguration()
	config.ICEServers = append(append([]webrtc.ICEServer(nil), config.ICEServers...), servers...)
	return pc.SetConfiguration(config)
}

// selectedPath reports whether pc's selected candidate pair goes through a TURN relay:
// common.PathRelay, common.PathDirect, or "" when no pair is selected or the browser doesn't say.
func selectedPath(pc *webrtc.PeerConnection) (path string) {
	defer func() {
		if recover() != nil {
			path = ""
		}
	}()
	sctp := pc.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return ""
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return ""
	}
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		return common.PathRelay
	}
	return common.PathDirect
}
```

If `SetConfiguration` rejects an unchanged field copied from `GetConfiguration` (pion returns errors for changing certificates, bundle policy, etc.), fix by copying only what pion allows, and keep the test green.

- [ ] **Step 4: Run tests and the wasm build**

Run: `go mod tidy && go test -race -count=1 ./clientcore/ -run 'TestAddICEServers' && GOOS=js GOARCH=wasm go build ./clientcore/`
Expected: PASS and a clean wasm build.

- [ ] **Step 5: Commit**

```bash
git add clientcore/turn.go clientcore/turn_test.go go.mod go.sum
git commit -m "clientcore: add ICE servers to a peer connection, report its selected path"
```

---

### Task 4: Wire the consumer and producer FSMs

**Files:**
- Modify: `clientcore/consumer.go` (state 1: genesis collection ~lines 240–320)
- Modify: `clientcore/producer.go` (state 2 response decode ~line 318; state 3 before `SetRemoteDescription` ~line 355; `attemptOutcome` ~line 868; connected record ~line 613)
- Test: `clientcore/outcome_test.go` (path reporting)

**Interfaces:**
- Consumes: `addICEServers`, `selectedPath` (Task 3); `common.SignalMsg.ICEServers`, `common.PrevPathField` (Task 1).
- Produces: `attemptOutcome.path string`, sent as `common.PrevPathField` by `addTo` when non-empty.

- [ ] **Step 1: Write the failing test** for path reporting in `clientcore/outcome_test.go` (find the existing `attemptOutcome`/`addTo` tests there and follow them):

```go
func TestAttemptOutcome_ReportsPath(t *testing.T) {
	var o attemptOutcome
	o.begin("offer")
	o.record(common.OutcomeConnected, nil)
	o.path = common.PathRelay
	form := url.Values{}
	o.addTo(form, 5*time.Second)
	if got := form.Get(common.PrevPathField); got != common.PathRelay {
		t.Fatalf("prev-path = %q, want relay", got)
	}

	o.begin("offer2")
	o.record(common.OutcomeNATTimeout, nil)
	form = url.Values{}
	o.addTo(form, 5*time.Second)
	if _, ok := form[common.PrevPathField]; ok {
		t.Fatal("an attempt with no path must not send prev-path")
	}
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./clientcore/ -run TestAttemptOutcome_ReportsPath`
Expected: compile failure (`o.path` undefined).

- [ ] **Step 3: Producer path reporting**

Add `path string` to `attemptOutcome`. `record` resets it like the other fields (it builds a fresh struct; keep it that way). In `addTo`, after the offer ID:

```go
	if o.path != "" {
		form.Set(common.PrevPathField, o.path)
	}
```

At `last.record(common.OutcomeConnected, peerConnection)` (~line 613), follow it with:

```go
			last.path = selectedPath(peerConnection)
```

- [ ] **Step 4: Producer applies the offer's ICE servers**

In state 2, replace

```go
			replyTo, offer, err := common.DecodeSignalMsg(offerBytes)
```

with

```go
			offerMsg, offer, err := common.DecodeSignal(offerBytes)
			replyTo := offerMsg.ReplyTo
```

keeping the error handling, and pass `offerMsg.ICEServers` as a new last element of state 3's input (`[]interface{}{peerConnection, replyTo, offer, connectionEstablished, connectionChange, connectionClosed, offerMsg.ICEServers}`). Update state 3's input comment block with `// input[6]: []webrtc.ICEServer (TURN relays for a room pairing, or nil)` and read it with `iceServers, _ := input[6].([]webrtc.ICEServer)`. Find every other `return 3, ...` into state 3 (grep `return 3,` in producer.go) and append `nil` so indexes line up. In state 3, immediately before `peerConnection.SetRemoteDescription(offer.SDP)`:

```go
			if err := addICEServers(peerConnection, iceServers); err != nil {
				slog.Debug("Error adding the room's ICE servers, going on without them", "error", err)
			}
```

Never log `iceServers`.

- [ ] **Step 5: Consumer applies the picked room genesis's ICE servers**

In state 1, beside `roomPairings := map[string]string{}`, add `roomICE := map[string][]webrtc.ICEServer{}`; where room pairings are recorded (`if msg.IsRoomGenesis() { ... }`), add:

```go
						if len(msg.ICEServers) > 0 {
							roomICE[msg.ReplyTo] = msg.ICEServers
						}
```

Then move the pick ahead of the offer, and apply its servers first. The end of state 1 becomes:

```go
			// Endgame case 2: pick a random genesis candidate, create an offer SDP, and shoot our shot.
			// The pick comes first so a room pairing's TURN servers are in place before the offer.
			replyTo := rooms.pick(ctx, genesisCandidates, roomCandidates)
			pairing = roomPairings[replyTo]
			if err := addICEServers(peerConnection, roomICE[replyTo]); err != nil {
				slog.Debug("Error adding the room's ICE servers, going on without them", "error", err)
			}
			sdp, err := peerConnection.CreateOffer(nil)
			if err != nil {
				// An error creating the offer is troubling, so let's start fresh by resetting the state
				slog.Debug("Error creating offer SDP", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			slog.Debug("Sending offer for genesis message", "total", len(genesisCandidates), "patience", options.Patience)
```

Check what `rooms.pick` does with `ctx` and whether calling it before `CreateOffer` changes behaviour on an offer error (it shouldn't: a failed offer restarts state 1, which re-collects candidates). Note the consumer re-enters state 1 with the same peer connection after some failures; if it has already had servers added, adding again would duplicate them. Guard against that: only call `addICEServers` when the peer connection's current servers don't already include them — simplest is to compare the first server's first URL — or restructure so a re-entry with a used peer connection gets a fresh one. Choose the smaller correct change and explain it in the commit message.

- [ ] **Step 6: Run tests and builds**

Run: `go test -race -count=1 ./clientcore/... ./common/... ./freddie/... && GOOS=js GOARCH=wasm go build ./clientcore/ && go vet ./clientcore/ ./freddie/ ./common/`
Expected: PASS, clean.

- [ ] **Step 7: Commit**

```bash
git add clientcore/
git commit -m "clientcore: add a room pairing's TURN servers before gathering, report relayed connections"
```

---

### Task 5: lantern-cloud: Cloudflare TURN credentials for Freddie

Prerequisite: the unbounded branch from Tasks 1–4 is pushed (as a PR). This task builds against that commit; the lantern-cloud PR bumps to the merged commit before it merges.

**Files (in a new lantern-cloud worktree, branch `fisk/freddie-room-turn` from `origin/main`):**
- Create: `cmd/freddie/turn.go`
- Create: `cmd/freddie/turn_test.go`
- Modify: `cmd/freddie/main.go` (after the ticket keys block)
- Modify: `tf/_modules/services/freddie/src/freddie.hcl` (Vault policy)
- Modify: `go.mod`, `go.sum` (bump `github.com/getlantern/broflake` to the unbounded branch commit)

**Interfaces:**
- Consumes: `freddie.Freddie.RoomICEServers func(context.Context) []webrtc.ICEServer` (Task 2).
- Produces: `type turnServers struct{...}`; `func newTurnServers(source turnKeySource, client *http.Client, endpoint string) *turnServers`; `func (t *turnServers) ICEServers(context.Context) []webrtc.ICEServer`; `func (t *turnServers) refresh(ctx context.Context) error`; `func (t *turnServers) run(ctx context.Context, refreshes metric.Int64Counter)`; `type turnKeySource func(ctx context.Context) (id, token string, err error)`; `func vaultTurnKey(path string) turnKeySource`.

- [ ] **Step 1: Bump broflake**

```bash
go get github.com/getlantern/broflake@<unbounded branch head SHA>
go mod tidy
go build ./cmd/freddie/
```

- [ ] **Step 2: Write failing tests** in `cmd/freddie/turn_test.go`

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeCloudflare(t *testing.T, status int, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/turn/keys/kid/credentials/generate-ice-servers" ||
			r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct{ TTL int `json:"ttl"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TTL != int(turnCredentialTTL/time.Second) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"iceServers":[{"urls":["stun:stun.cloudflare.com:3478","stun:stun.cloudflare.com:53"]},{"urls":["turn:turn.cloudflare.com:3478?transport=udp","turn:turn.cloudflare.com:53?transport=udp","turns:turn.cloudflare.com:443?transport=tcp"],"username":"u","credential":"c"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func staticKey(context.Context) (string, string, error) { return "kid", "tok", nil }

func TestTurnServers_FetchesAndDropsPort53(t *testing.T) {
	var calls atomic.Int32
	cf := fakeCloudflare(t, http.StatusCreated, &calls)
	ts := newTurnServers(staticKey, cf.Client(), cf.URL)
	if got := ts.ICEServers(context.Background()); got != nil {
		t.Fatalf("before the first fetch the hook must return nil, got %+v", got)
	}
	if err := ts.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := ts.ICEServers(context.Background())
	var urls []string
	for _, s := range got {
		urls = append(urls, s.URLs...)
	}
	joined := strings.Join(urls, " ")
	if strings.Contains(joined, ":53") || !strings.Contains(joined, "turns:turn.cloudflare.com:443?transport=tcp") {
		t.Fatalf("urls %q: want port 53 dropped and the TLS relay kept", joined)
	}
	for _, s := range got {
		if strings.HasPrefix(s.URLs[0], "turn") && (s.Username != "u" || s.Credential != "c") {
			t.Fatalf("TURN server lost its credentials: %+v", s)
		}
	}
}

func TestTurnServers_ErrorsKeepLastGood(t *testing.T) {
	var calls atomic.Int32
	cf := fakeCloudflare(t, http.StatusCreated, &calls)
	ts := newTurnServers(staticKey, cf.Client(), cf.URL)
	if err := ts.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts.source = func(context.Context) (string, string, error) { return "", "", errors.New("vault down") }
	if err := ts.refresh(context.Background()); err == nil {
		t.Fatal("a failed key read must be an error")
	}
	if ts.ICEServers(context.Background()) == nil {
		t.Fatal("a failed refresh must keep the last good servers until they expire")
	}
	ts.now = func() time.Time { return time.Now().Add(turnCredentialTTL) }
	if ts.ICEServers(context.Background()) != nil {
		t.Fatal("expired credentials must not be handed out")
	}
}

func TestTurnServers_CloudflareErrorIsError(t *testing.T) {
	var calls atomic.Int32
	cf := fakeCloudflare(t, http.StatusInternalServerError, &calls)
	ts := newTurnServers(staticKey, cf.Client(), cf.URL)
	if err := ts.refresh(context.Background()); err == nil {
		t.Fatal("a non-2xx from Cloudflare must be an error")
	}
	if ts.ICEServers(context.Background()) != nil {
		t.Fatal("no servers without a successful fetch")
	}
}
```

- [ ] **Step 3: Run to see them fail**

Run: `go test ./cmd/freddie/ -run TestTurnServers`
Expected: compile failure.

- [ ] **Step 4: Implement** `cmd/freddie/turn.go`

```go
package main

// Room pairings (helper personal proxies) get Cloudflare TURN relays, so a pairing whose peers can't
// reach each other directly still connects. Freddie stamps these servers on room signaling messages
// (broflake freddie.Freddie.RoomICEServers); this file keeps a fresh set of credentials cached so
// signaling never waits on Cloudflare.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/getlantern/lantern-cloud/auth/vault"
)

const (
	// turnVaultPath holds the Cloudflare Realtime TURN key: turn_key_id and turn_key_api_token.
	turnVaultPath = "secret/data/lantern_cloud/cloudflare_turn"
	turnEndpoint  = "https://rtc.live.cloudflare.com"
	// turnCredentialTTL is how long Cloudflare's credentials last; they're refreshed every
	// turnRefreshEvery, so a set handed out just before a refresh still has half its life.
	turnCredentialTTL = time.Hour
	turnRefreshEvery  = turnCredentialTTL / 2
	turnRetryEvery    = time.Minute
	metricTurnRefresh = "freddie.turn.refresh"
)

type turnKeySource func(ctx context.Context) (id, token string, err error)

// vaultTurnKey reads the TURN key from Vault, cached for the KV lease's TTL.
func vaultTurnKey(path string) turnKeySource {
	lease := vault.NewLease(path, vault.SecretTypeKV)
	return func(ctx context.Context) (string, string, error) {
		var data struct {
			ID    string `json:"turn_key_id"`
			Token string `json:"turn_key_api_token"`
		}
		if err := lease.Fetch(ctx, &data); err != nil {
			return "", "", fmt.Errorf("fetching the TURN key: %w", err)
		}
		if data.ID == "" || data.Token == "" {
			return "", "", errors.New("the TURN key secret is missing turn_key_id or turn_key_api_token")
		}
		return data.ID, data.Token, nil
	}
}

type turnServers struct {
	source   turnKeySource
	client   *http.Client
	endpoint string
	now      func() time.Time

	mu      sync.Mutex
	servers []webrtc.ICEServer
	expires time.Time
}

func newTurnServers(source turnKeySource, client *http.Client, endpoint string) *turnServers {
	return &turnServers{source: source, client: client, endpoint: endpoint, now: time.Now}
}

// ICEServers is freddie.Freddie.RoomICEServers: the cached servers, or nil before the first
// successful fetch or once they've expired. It never blocks on the network.
func (t *turnServers) ICEServers(context.Context) []webrtc.ICEServer {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.servers == nil || !t.now().Before(t.expires) {
		return nil
	}
	return t.servers
}

// refresh fetches new credentials. On failure the last good ones stay until they expire.
func (t *turnServers) refresh(ctx context.Context) error {
	id, token, err := t.source(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]int{"ttl": int(turnCredentialTTL / time.Second)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.endpoint+"/v1/turn/keys/"+id+"/credentials/generate-ice-servers", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	issued := t.now()
	res, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("generating TURN credentials: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("generating TURN credentials: status %d", res.StatusCode)
	}
	var out struct {
		ICEServers []webrtc.ICEServer `json:"iceServers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("decoding TURN credentials: %w", err)
	}
	servers := withoutPort53(out.ICEServers)
	if len(servers) == 0 {
		return errors.New("Cloudflare returned no ICE servers")
	}
	t.mu.Lock()
	t.servers, t.expires = servers, issued.Add(turnCredentialTTL)
	t.mu.Unlock()
	return nil
}

// withoutPort53 drops URLs on port 53, which Cloudflare recommends because browsers block it, and
// any server left with no URLs.
func withoutPort53(in []webrtc.ICEServer) []webrtc.ICEServer {
	var out []webrtc.ICEServer
	for _, s := range in {
		var urls []string
		for _, u := range s.URLs {
			if !strings.Contains(u, ":53?") && !strings.HasSuffix(u, ":53") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			s.URLs = urls
			out = append(out, s)
		}
	}
	return out
}

// run refreshes the credentials every turnRefreshEvery, retrying every turnRetryEvery after a
// failure, until ctx is done. Outcomes are counted, never logged with the key or credentials.
func (t *turnServers) run(ctx context.Context, refreshes metric.Int64Counter) {
	for {
		wait := turnRefreshEvery
		err := t.refresh(ctx)
		outcome := "ok"
		if err != nil {
			outcome, wait = "error", turnRetryEvery
		}
		refreshes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
```

Adapt `run`'s metric plumbing to how `tickets.go` creates its counters (`metricTicketKeyRefresh`): use the same metrics package and pattern, and update `Produces` names only if the existing pattern requires it. Log a refresh error once per failure streak with the repo's `log` package, using only the error text (it contains no secret: the token never appears in errors above; double-check that).

- [ ] **Step 5: Wire it in `main.go`** after the ticket keys block:

```go
	turn := newTurnServers(vaultTurnKey(turnVaultPath), &http.Client{Timeout: 10 * time.Second}, turnEndpoint)
	f.RoomICEServers = turn.ICEServers
	go turn.run(ctx, turnRefreshCounter)
```

where `turnRefreshCounter` is created like the ticket key refresh counter.

- [ ] **Step 6: Vault policy** — in `tf/_modules/services/freddie/src/freddie.hcl`, add after the `front_auth` block, matching its capabilities:

```hcl
path "secret/data/lantern_cloud/cloudflare_turn" {
  capabilities = ["read"]
}
```

- [ ] **Step 7: Run tests, lint, build**

Run: `go test -race -count=1 ./cmd/freddie/... && go vet ./cmd/freddie/ && golangci-lint run --new-from-rev=origin/main ./cmd/freddie/...`
Expected: PASS, clean.

- [ ] **Step 8: Commit**

```bash
git add cmd/freddie/ tf/_modules/services/freddie/src/freddie.hcl go.mod go.sum
git commit -m "freddie: Cloudflare TURN relays for helper room pairings"
```

---

## Rollout (not tasks for the implementer)

1. unbounded PR via /create-pr (Tasks 1–4). Safe alone: no hook, no change.
2. lantern-cloud PR via /create-pr (Task 5), bumped to the merged unbounded commit. The operator applies the Vault policy before Freddie deploys; until then the hook returns nil and the refresh metric shows `error`.
3. Clients get TURN when radiance/lantern pick up the new unbounded.
