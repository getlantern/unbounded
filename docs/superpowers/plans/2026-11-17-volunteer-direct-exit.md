# Volunteer Direct Exit (unbounded) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an app volunteer and a new user's app carry a session over a bare pion ICE agent, with no DTLS and no SCTP, and hand each end a `net.PacketConn` for the embedder to run Hysteria2 on. Browser widgets and old apps keep today's path.

**Architecture:** The spec's rollout runs four repos in order: lantern-cloud, unbounded, lantern-box, then the radiance and lantern bumps. This plan covers unbounded only, which leaves Hysteria2 to the embedder.
- **Producer FSM:** two new states, 6 for the ICE handshake and 7 for serving through `ExitOptions.Serve`. The genesis advertises `DirectExit` while `ExitOptions.Ready` is true.
- **Consumer FSM:** two new states. State 6 is the handshake. State 7 reports an `ExitPath` to `WebRTCOptions.OnExitPath`, then waits for the session to end.
- **Signaling** reuses the offer, answer and ICE message types with new optional fields, so freddie needs no change.

**Tech Stack:**
- Go;
- pion `ice/v4` v4.4.8, `stun/v4`, `webrtc/v4` and `turn/v5`, plus `transport/v5/vnet` for tests;
- testify.

**Spec:** `docs/superpowers/specs/2026-11-17-volunteer-direct-exit-design.md` (commit e891961). Task 1 updates its signaling section to match the ruling below.

**Later plans, written after this one merges:**
- lantern-box: the volunteer's exit hooks, the Hysteria2 service and its inbound, registration, and the user's `hysteria2x` path.
- lantern-cloud: WebRTC-only registration and the config fields.
- radiance and lantern: version bumps.

## Rulings made while planning

- **Signaling reuses message types instead of adding new ones.**
  - Freddie's volunteer socket accepts only genesis and answer types (`freddie/volunteer.go:35-38`), and freddie sets each step's TTL by type (`freddie/freddie.go:1171-1185`). New types would need a freddie release first, and the volunteer socket would refuse them.
  - So the ICE offer is an `OfferMsg` with a new `ICE *ICEOffer` field and an empty SDP.
  - The ICE answer is a `SignalMsgAnswer` whose payload is `ICEAnswerMsg{ICE *ICEAnswer}`.
  - The user's candidates go in a new `ICEMsg.ICECandidates []string` field.
  - The spec's `ICEOfferMsg` and `ICEAnswerMsg` names change to match.
  - If wrong: freddie's ICE-NAT metrics don't classify these sessions, because they see no SDP or `webrtc.ICECandidate`s. Adding that later is a freddie-only change.
- **An ICE offer is decoded without its SDP.** pion marshals a zero `SessionDescription` as `{"type":"unknown","sdp":""}`, and its `UnmarshalJSON` then fails with `unknown`. I checked this against v4.2.25. So `DecodeSignal` reads an offer that has `ICE` set through a wire struct that skips `SDP`. An old producer never receives such an offer.
- **The volunteer's candidates travel in its answer.** Freddie's exchange has three legs: offer, then answer, then the user's ICE. The volunteer can't send a separate candidate message after its answer, so `ICEAnswer.Candidates` carries them, as the SDP answer carries them today.
- **An ICE session that disconnects is over.** Today's states 5 treat `Disconnected` as failure (`consumer.go:794`, `producer.go:776`). The ICE states do the same.

## Global Constraints

- **Never log** IP addresses, ICE candidates, ufrags, pwds, cert pins, passwords, pairing IDs or TURN credentials, at any log level.
  - `ice.UnmarshalCandidate`'s errors quote the raw candidate, so never log or wrap them. Return the sentinel `errBadCandidate`.
- **wasm:** every new file that imports `pion/ice` carries `//go:build !wasm` and has a `//go:build wasm` stub. Check with `GOOS=js GOARCH=wasm go build ./clientcore/ ./common/`.
- **Old peers:** an old consumer or producer must see byte-identical messages.
  - New fields are `omitempty`.
  - A consumer never sends an ICE offer unless the genesis said `DirectExit` and its own `OnExitPath` is set.
  - A producer never sets `DirectExit` unless `Exit.Ready()` is true.
- **Browser widgets are unchanged:** `WebRTCOptions.Exit` stays nil there.
- **Signaling sends:** control-plane sends to `com.tx` go through `sendCtx`. Every state that owns an `*iceSession` puts it at `input[0]` when it returns `input` on cancellation, so `closeWorkerResource` closes it.
- **Dependencies:** no new modules. `pion/ice/v4`, `pion/stun/v4` and `pion/turn/v5` are already required. If `go.mod` changes anyway, run `go mod tidy` and commit `go.mod` and `go.sum` together.
- **Tests:** run `go test ./common/ ./clientcore/` with `-race`, and run `golangci-lint run --new-from-rev=origin/main ./...`.
- **Reviews:** every implementer and reviewer subagent runs on opus.
- **Commits:** end every commit message with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE
  ```

## File structure

| File | Responsibility |
|---|---|
| `common/resource.go` (modify) | Signaling wire types: `GenesisMsg.DirectExit`, `OfferMsg.ICE`, `ICEOffer`, `ICEAnswer`, `ICEAnswerMsg`, `ICEMsg.ICECandidates`, `ConsumerInfo.Exit`, and ICE answer decoding |
| `common/resource_test.go` (modify) | Round trips and old-peer compatibility |
| `clientcore/exit.go` (create) | `ExitOptions`, `ExitPath`, `exitReady`, `exitParams`, `observeSession` |
| `clientcore/settings.go` (modify) | `WebRTCOptions.Exit` and `WebRTCOptions.OnExitPath` |
| `clientcore/jit_egress_consumer.go` (modify) | An exit session never claims an egress slot |
| `clientcore/exit_test.go` (create) | The exit helpers, and the JIT egress ignoring exit sessions |
| `clientcore/ice_session.go` (create, `!wasm`) | `iceSession`: agent, gathering, candidates, connect, packet-conn adapter, end of session |
| `clientcore/ice_session_wasm.go` (create, `wasm`) | Stubs |
| `clientcore/tuning_native.go` (modify) | `WebRTCTuning.iceOptions()` |
| `clientcore/ice_session_test.go` (create, `!wasm`) | vnet harness, punched and relayed sessions, end of session, candidate summary |
| `clientcore/producer.go` (modify) | Genesis `DirectExit`, routing an ICE offer to state 6, states 6 and 7, `observeSession` in state 5 |
| `clientcore/producer_exit_test.go` (create, `!wasm`) | The producer's genesis, routing and refusal |
| `clientcore/consumer.go` (modify) | Collecting `DirectExit` from genesis messages, routing to state 6, states 6 and 7, `postSignalTo` |
| `clientcore/consumer_exit_test.go` (create, `!wasm`) | Consumer routing and the offer's form |
| `clientcore/exit_e2e_test.go` (create, `!wasm`) | Fake freddie, then full FSMs on vnet: punched, relayed, old consumer, flag off |

---

### Task 1: Signaling wire types

**Files:**
- Modify: `common/resource.go`, at the `GenesisMsg` struct near line 103, `OfferMsg` at 112-133, `ICEMsg` at 135-138, `ConsumerInfo` at 72-88, and the `DecodeSignal` answer case at 242-245.
- Modify: `common/resource_test.go`.
- Modify: `docs/superpowers/specs/2026-11-17-volunteer-direct-exit-design.md`, the **Signaling** bullets under "unbounded `clientcore`: ICE sessions".

**Interfaces:**
- Produces:
  - `common.GenesisMsg.DirectExit bool`;
  - `common.OfferMsg.ICE *common.ICEOffer`;
  - `common.ICEOffer{Ufrag, Pwd string}`;
  - `common.ICEAnswer{Ufrag, Pwd string, Candidates []string, CertPin, Password string}`;
  - `common.ICEAnswerMsg{ICE *ICEAnswer}`;
  - `common.ICEMsg.ICECandidates []string`;
  - `common.ConsumerInfo.Exit bool`.
- `DecodeSignal` and `DecodeSignalMsg` return a `common.ICEAnswer` value for an ICE answer, and a `webrtc.SessionDescription` for any other answer, as before.

- [ ] **Step 1: Write the failing tests.** Append to `common/resource_test.go`, adding `encoding/json`, `github.com/pion/webrtc/v4`, `github.com/stretchr/testify/assert` and `github.com/stretchr/testify/require` to its imports if they aren't there:

```go
func TestGenesisMsgDirectExitOmittedWhenFalse(t *testing.T) {
	b, err := json.Marshal(GenesisMsg{})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "DirectExit", "an old producer's genesis must be byte-identical")

	b, err = json.Marshal(GenesisMsg{DirectExit: true})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"DirectExit":true`)

	var g GenesisMsg
	require.NoError(t, json.Unmarshal([]byte(`{"PathAssertion":{}}`), &g))
	assert.False(t, g.DirectExit)
}

func TestOfferMsgICEOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(OfferMsg{Tag: "t"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"ICE"`, "an SDP offer must be byte-identical")

}

// An ICE offer's SDP is the zero SessionDescription, which pion writes with type "unknown" and
// then refuses to read back. DecodeSignal must still read the offer.
func TestDecodeSignalICEOffer(t *testing.T) {
	want := OfferMsg{ICE: &ICEOffer{Ufrag: "u", Pwd: "p"}, Tag: "t", Country: "IR", Pairing: "pair"}
	_, got, err := DecodeSignalMsg(signalBytes(t, SignalMsgOffer, want))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestDecodeSignalSDPOfferUnchanged(t *testing.T) {
	want := OfferMsg{SDP: webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0\r\n"}, Tag: "t"}
	_, got, err := DecodeSignalMsg(signalBytes(t, SignalMsgOffer, want))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func signalBytes(t *testing.T, typ SignalMsgType, payload any) []byte {
	t.Helper()
	p, err := json.Marshal(payload)
	require.NoError(t, err)
	raw, err := json.Marshal(SignalMsg{ReplyTo: "r", Type: typ, Payload: string(p)})
	require.NoError(t, err)
	return raw
}

func TestDecodeSignalICEAnswer(t *testing.T) {
	want := ICEAnswer{
		Ufrag:      "u",
		Pwd:        "p",
		Candidates: []string{"1 1 udp 2130706431 10.0.0.2 5000 typ host"},
		CertPin:    "pin",
		Password:   "pw",
	}
	replyTo, got, err := DecodeSignalMsg(signalBytes(t, SignalMsgAnswer, ICEAnswerMsg{ICE: &want}))
	require.NoError(t, err)
	assert.Equal(t, "r", replyTo)
	assert.Equal(t, want, got)
}

func TestDecodeSignalSDPAnswerUnchanged(t *testing.T) {
	want := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "v=0\r\n"}
	_, got, err := DecodeSignalMsg(signalBytes(t, SignalMsgAnswer, want))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestICEMsgCandidatesOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(ICEMsg{ConsumerSessionID: "s"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "ICECandidates")

	_, got, err := DecodeSignalMsg(signalBytes(t, SignalMsgICE, ICEMsg{ICECandidates: []string{"c"}, ConsumerSessionID: "s"}))
	require.NoError(t, err)
	assert.Equal(t, ICEMsg{ICECandidates: []string{"c"}, ConsumerSessionID: "s"}, got)
}

// A direct exit session's consumer is still a consumer for the UI, so Exit doesn't make
// ConsumerInfo non-nil on its own, any more than Country does.
func TestConsumerInfoExitDoesNotMakeItNonNil(t *testing.T) {
	assert.True(t, ConsumerInfo{Exit: true}.Nil())
	assert.False(t, ConsumerInfo{SessionID: "s", Exit: true}.Nil())
}
```

- [ ] **Step 2: Run them and check they fail.** Run `go test ./common/ -run 'DirectExit|OfferMsgICE|ICEOffer|SDPOffer|ICEAnswer|SDPAnswer|ICEMsgCandidates|ConsumerInfoExit' -v`. Expected: the build fails with errors such as `unknown field DirectExit` and `undefined: ICEOffer`.

- [ ] **Step 3: Add the fields and types.** In `common/resource.go`, make these changes.

  Add a field to `ConsumerInfo` after `Pairing`:

```go
	// Exit says this consumer's session is a direct exit (see GenesisMsg.DirectExit): the
	// producer exits its traffic itself, so no egress connection serves it. Local to the
	// producer, like Pairing.
	Exit bool
```

  Replace `GenesisMsg`:

```go
type GenesisMsg struct {
	PathAssertion PathAssertion
	// DirectExit says the producer can carry this session over ICE alone and exit its traffic
	// itself (see ICEOffer). A consumer that can answers with an ICE offer, and any other with an
	// SDP offer as before. Omitted when false, so other producers' genesis messages are unchanged.
	DirectExit bool `json:",omitempty"`
}
```

  Add a field to `OfferMsg` after `Pairing`:

```go
	// ICE, when set, offers a bare ICE session instead of a peer connection, and SDP is empty.
	// A consumer sends one only in answer to a DirectExit genesis.
	ICE *ICEOffer `json:",omitempty"`
```

  Add these types after `OfferMsg`:

```go
// ICEOffer is a consumer's ICE credentials for a bare ICE session. Never logged.
type ICEOffer struct {
	Ufrag string
	Pwd   string
}

// ICEAnswer is a producer's answer to an ICEOffer. It carries the producer's ICE credentials and
// candidates, and what the consumer needs to use the exit over the session: the SHA-256 pin of
// the exit certificate's public key, and the session's password. Never logged.
type ICEAnswer struct {
	Ufrag      string
	Pwd        string
	Candidates []string
	CertPin    string
	Password   string
}

// ICEAnswerMsg is the payload of a SignalMsgAnswer that answers an ICEOffer. An answer to an SDP
// offer is a bare webrtc.SessionDescription, as before.
type ICEAnswerMsg struct {
	ICE *ICEAnswer
}
```

  Add a field to `ICEMsg`:

```go
type ICEMsg struct {
	Candidates        []webrtc.ICECandidate
	ConsumerSessionID string
	// ICECandidates are a bare ICE session's candidates, as ice.Candidate.Marshal writes them,
	// sent instead of Candidates. Omitted when empty, so other ICE messages are unchanged.
	ICECandidates []string `json:",omitempty"`
}
```

- [ ] **Step 4: Decode the ICE offer and the ICE answer.** Add this type after `ICEAnswerMsg`:

```go
// iceOfferWire reads an OfferMsg without its SDP. An ICE offer's SDP is the zero
// SessionDescription, which pion writes with type "unknown" and won't read back.
type iceOfferWire struct {
	Tag     string
	Country string
	Pairing string
	ICE     *ICEOffer
}
```

  In `DecodeSignal`, replace the `SignalMsgOffer` case:

```go
		case SignalMsgOffer:
			var wire iceOfferWire
			if json.Unmarshal([]byte(msg.Payload), &wire) == nil && wire.ICE != nil {
				return msg, OfferMsg{Tag: wire.Tag, Country: wire.Country, Pairing: wire.Pairing, ICE: wire.ICE}, nil
			}
			var offer OfferMsg
			err := json.Unmarshal([]byte(msg.Payload), &offer)
			return msg, offer, err
```

  Then replace the `SignalMsgAnswer` case:

```go
		case SignalMsgAnswer:
			var iceAnswer ICEAnswerMsg
			if json.Unmarshal([]byte(msg.Payload), &iceAnswer) == nil && iceAnswer.ICE != nil {
				return msg, *iceAnswer.ICE, nil
			}
			var answer webrtc.SessionDescription
			err := json.Unmarshal([]byte(msg.Payload), &answer)
			return msg, answer, err
```

  Then update the `SignalMsg` doc comment's list of payloads so it mentions that an answer may also be an `ICEAnswerMsg`.

- [ ] **Step 5: Run the tests and check they pass.** Run `go test ./common/ -race`. Expected: PASS, including every existing test.

- [ ] **Step 6: Update the spec's signaling bullets.** In the spec, replace the four **Signaling** bullets with:

```markdown
**Signaling.** Freddie's volunteer socket accepts only genesis and answer messages, and freddie times each step by message type. So the ICE session reuses today's types, with new optional fields, and freddie doesn't change:
- `GenesisMsg` gets `DirectExit bool` (`json:",omitempty"`). A volunteer sets it only when `Exit.Ready` (below) is true. Old users' apps ignore it.
- The ICE offer is an `OfferMsg` with `ICE *ICEOffer{Ufrag, Pwd}` and an empty SDP. It keeps `OfferMsg`'s `Tag`, `Country` and `Pairing`.
- The ICE answer is a `SignalMsgAnswer` whose payload is `ICEAnswerMsg{ICE *ICEAnswer{Ufrag, Pwd, Candidates, CertPin, Password}}`. The volunteer's candidates travel in it, as today's answer SDP carries them.
- The user's candidates go in `ICEMsg.ICECandidates []string`, in pion's `ice.Candidate.Marshal` form.
- A new user's app answers a `DirectExit` genesis with an ICE offer, and any other genesis with today's SDP offer. An old volunteer never sets `DirectExit`, so it never receives an ICE offer.
- Rooms, tickets, pairings and freddie's routing don't change. Freddie's ICE-NAT metrics don't classify ICE sessions, since they carry no SDP.
```

  Also replace `ICEOfferMsg{ufrag, pwd}` with `OfferMsg{ICE: ufrag, pwd}`, and `ICEAnswerMsg{ufrag, pwd, cert pin, password}` with `ICEAnswerMsg{ufrag, pwd, candidates, cert pin, password}`, in the spec's mermaid diagram and in the "User" hook bullet.

- [ ] **Step 7: Check the wasm build, then commit.**

```bash
GOOS=js GOARCH=wasm go build ./common/
git add common/resource.go common/resource_test.go docs/superpowers/specs/2026-11-17-volunteer-direct-exit-design.md
git commit -m "common: direct exit signaling fields on today's message types"
```

  Add the commit trailers from Global Constraints to the message.

---

### Task 2: Exit hooks, and keeping exit sessions off the egress

**Files:**
- Create: `clientcore/exit.go`.
- Modify: `clientcore/settings.go`. Add the two fields to `WebRTCOptions`, after `Tuning`.
- Modify: `clientcore/jit_egress_consumer.go:44`.
- Create: `clientcore/exit_test.go`.

**Interfaces:**
- Consumes: `common.ConsumerInfo.Exit` (Task 1).
- Produces:
  - `type ExitOptions struct { Ready func() bool; Params func() (certPin, password string, ok bool); Serve func(ctx context.Context, conn net.PacketConn, password string); OnSession func(directExit bool) }`;
  - `type ExitPath struct { Conn net.PacketConn; CertPin, Password string; Relayed bool }`;
  - `WebRTCOptions.Exit *ExitOptions`;
  - `WebRTCOptions.OnExitPath func(ExitPath)`;
  - `func exitReady(options *WebRTCOptions) bool`;
  - `func exitParams(options *WebRTCOptions) (certPin, password string, ok bool)`;
  - `func observeSession(options *WebRTCOptions, directExit bool)`.

- [ ] **Step 1: Write the failing tests.** Create `clientcore/exit_test.go`:

```go
package clientcore

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/getlantern/broflake/common"
)

func TestExitReady(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	assert.False(t, exitReady(o), "no Exit, as in a browser widget")
	o.Exit = &ExitOptions{}
	assert.False(t, exitReady(o), "no Ready func")
	ready := false
	o.Exit.Ready = func() bool { return ready }
	assert.False(t, exitReady(o))
	ready = true
	assert.True(t, exitReady(o))
}

func TestExitParams(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	_, _, ok := exitParams(o)
	assert.False(t, ok, "no Exit")
	o.Exit = &ExitOptions{Ready: func() bool { return true }}
	_, _, ok = exitParams(o)
	assert.False(t, ok, "no Params func")
	o.Exit.Params = func() (string, string, bool) { return "pin", "pw", true }
	o.Exit.Serve = func(context.Context, net.PacketConn, string) {}
	pin, pw, ok := exitParams(o)
	assert.True(t, ok)
	assert.Equal(t, "pin", pin)
	assert.Equal(t, "pw", pw)
	o.Exit.Serve = nil
	_, _, ok = exitParams(o)
	assert.False(t, ok, "nothing to serve the session with")
}

func TestObserveSession(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	observeSession(o, true) // no Exit: no panic
	var got []bool
	o.Exit = &ExitOptions{OnSession: func(directExit bool) { got = append(got, directExit) }}
	observeSession(o, true)
	observeSession(o, false)
	assert.Equal(t, []bool{true, false}, got)
}

// A direct exit session's ConsumerInfo reaches the JIT egress worker like any other, but the
// worker must not dial the egress for it: the producer exits that session itself.
func TestJITEgressIgnoresExitSessions(t *testing.T) {
	eo := NewDefaultEgressOptions()
	eo.Addr = "http://127.0.0.1:1" // never dialed
	var wg sync.WaitGroup
	fsm := NewJITEgressConsumer(eo, nil, &wg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		next, _ := fsm.state[0](ctx, fsm.com, nil)
		done <- next
	}()
	// State 0 first offers the slot to the producer.
	select {
	case msg := <-fsm.com.tx:
		assert.Equal(t, PathAssertionIPC, msg.IpcType)
	case <-time.After(5 * time.Second):
		t.Fatal("no path assertion")
	}
	fsm.com.rx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{SessionID: "s", Exit: true}}
	select {
	case msg := <-fsm.com.tx:
		t.Fatalf("an exit session claimed the egress slot: %+v", msg)
	case next := <-done:
		t.Fatalf("state 0 went on to state %d for an exit session", next)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-done
}
```

  Before writing this test, read `clientcore/jit_egress_consumer.go:14-80` and `clientcore/settings.go`.
  - **Name check:** confirm `NewDefaultEgressOptions`, the `Addr` field, and `NewJITEgressConsumer(options *EgressOptions, act *activation, wg *sync.WaitGroup)`. If a name differs, use the real one.
  - **Nil activation:** if a nil `*activation` panics in state 0, pass whatever zero value state 0 accepts. Changing the setup that way is fine. The assertions stay as written.

- [ ] **Step 2: Run them and check they fail.** Run `go test ./clientcore/ -run 'TestExitReady|TestExitParams|TestObserveSession|TestJITEgressIgnoresExitSessions' -v`. Expected: the build fails with `undefined: exitReady`.

- [ ] **Step 3: Write `clientcore/exit.go`.**

```go
package clientcore

import (
	"context"
	"net"
)

// ExitOptions let a producer carry sessions over ICE alone and exit their traffic itself, instead
// of relaying it to the egress server. A browser widget, which can't dial destinations, leaves
// them nil. Ready, Params and OnSession run on the signaling path, so they must be cheap and must
// not block.
type ExitOptions struct {
	// Ready reports whether the producer can exit traffic now. While it can, each genesis offers
	// a direct exit (see common.GenesisMsg.DirectExit).
	Ready func() bool
	// Params mints a session's credentials: the SHA-256 pin of the exit certificate's public key,
	// and a password for this session alone. ok is false when the producer can no longer exit.
	Params func() (certPin, password string, ok bool)
	// Serve serves one session on conn, which carries only that session's consumer. It runs on
	// its own goroutine and must return once ctx ends or conn's reads fail.
	Serve func(ctx context.Context, conn net.PacketConn, password string)
	// OnSession, if set, is called as each session connects, with whether it is a direct exit
	// or relays to the egress server as before.
	OnSession func(directExit bool)
}

// ExitPath is a consumer's connected direct exit session. Conn reaches the producer's exit, which
// takes only the certificate CertPin pins and the session's Password. Relayed says whether ICE
// chose a TURN relay. Closing Conn ends the session, and once the session ends, Conn's reads fail.
// Never logged: it carries the session's credentials.
type ExitPath struct {
	Conn     net.PacketConn
	CertPin  string
	Password string
	Relayed  bool
}

// exitReady reports whether a producer can offer a direct exit now.
func exitReady(options *WebRTCOptions) bool {
	return options.Exit != nil && options.Exit.Ready != nil && options.Exit.Ready()
}

// exitParams mints a direct exit session's credentials, reporting false if the producer can't
// serve one.
func exitParams(options *WebRTCOptions) (certPin, password string, ok bool) {
	if options.Exit == nil || options.Exit.Params == nil || options.Exit.Serve == nil {
		return "", "", false
	}
	return options.Exit.Params()
}

// observeSession tells the embedder that a session connected, and whether it is a direct exit.
func observeSession(options *WebRTCOptions, directExit bool) {
	if options.Exit != nil && options.Exit.OnSession != nil {
		options.Exit.OnSession(directExit)
	}
}
```

- [ ] **Step 4: Add the options.** In `clientcore/settings.go`, add these to `WebRTCOptions` after the `Tuning` field:

```go
	// Exit, on a producer, lets it exit sessions itself (see ExitOptions). Nil, the default and
	// always in a browser widget, relays every session to the egress server as before. Set it
	// before NewBroflake.
	Exit *ExitOptions
	// OnExitPath, on a consumer, takes each direct exit session as it connects (see ExitPath). It
	// runs on the worker's goroutine and must not block. While it is nil, the consumer answers
	// every genesis with an SDP offer, as before. Set it before NewBroflake.
	OnExitPath func(ExitPath)
```

- [ ] **Step 5: Keep exit sessions off the egress.** In `clientcore/jit_egress_consumer.go`, in `waitforconsumerloop`, replace the condition at line 44:

```go
				case msg := <-com.rx:
					// A direct exit session's producer exits its traffic itself, so it never claims
					// this slot's egress connection.
					ci, _ := msg.Data.(common.ConsumerInfo)
					if msg.IpcType == ConsumerInfoIPC && !ci.Nil() && !ci.Exit {
```

  Keep the body, and replace `msg.Data.(common.ConsumerInfo)` at the former line 57 with `ci`.

- [ ] **Step 6: Run the tests and check they pass.** Run `go test ./clientcore/ -race -run 'TestExitReady|TestExitParams|TestObserveSession|TestJITEgressIgnoresExitSessions' -v`. Expected: PASS. Then run `go test ./clientcore/ -race`. Expected: PASS.

- [ ] **Step 7: Commit.** Use the message "clientcore: exit hooks for direct exit sessions", with the trailers from Global Constraints.

```bash
GOOS=js GOARCH=wasm go build ./clientcore/
git add clientcore/exit.go clientcore/exit_test.go clientcore/settings.go clientcore/jit_egress_consumer.go
git commit
```

---

### Task 3: The ICE session

**Files:**
- Create: `clientcore/ice_session.go` (`//go:build !wasm`).
- Create: `clientcore/ice_session_wasm.go` (`//go:build wasm`).
- Modify: `clientcore/tuning_native.go`. Add `iceOptions()`.
- Create: `clientcore/ice_session_test.go` (`//go:build !wasm`).

**Interfaces:**
- Consumes: `iceNetOf`, `boundedDialNet` (`turn_native.go:64`), `awaitGathering`, `turnRelayGrace`, `gatherEnd` (`turn.go`), `isPublicIP` (`producer.go`), and `WebRTCTuning.checkingFor`.
- Produces, used by Tasks 4 to 6:
  - `func newICESession(options *WebRTCOptions, controlling bool, servers []webrtc.ICEServer, window time.Duration) (*iceSession, error)`;
  - `(*iceSession).credentials() (ufrag, pwd string, err error)`;
  - `(*iceSession).gather(limit time.Duration) ([]string, gatherEnd, error)`;
  - `(*iceSession).addRemote(raw []string) error`;
  - `(*iceSession).connect(ctx context.Context, ufrag, pwd string) error`;
  - `(*iceSession).packetConn() net.PacketConn`;
  - `(*iceSession).relayed() bool`;
  - `(*iceSession).done() <-chan struct{}`;
  - `(*iceSession).Close() error`;
  - `func summarizeICECandidates(raw []string) (nonHost bool, public net.IP)`;
  - `var errBadCandidate`.
- Test helpers, used by Task 6:
  - `func newVNetICE(t *testing.T, behavior vnet.EndpointDependencyType) (consumer, producer *vnet.Net, stunURL string, turnServer webrtc.ICEServer)`;
  - `func connectICE(t *testing.T, consumerNet, producerNet transport.Net, servers []webrtc.ICEServer, tuning WebRTCTuning) (consumer, producer *iceSession)`.

- [ ] **Step 1: Write the failing tests.** Create `clientcore/ice_session_test.go`:

```go
//go:build !wasm

package clientcore

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newVNetICE builds a virtual internet: a STUN and TURN server at 1.2.3.1, and a consumer and a
// producer, each on its own LAN behind a NAT that maps and filters as behavior says.
// EndpointIndependent NATs let a hole be punched. EndpointAddrPortDependent ones are symmetric,
// so only a relay connects them.
func newVNetICE(t *testing.T, behavior vnet.EndpointDependencyType) (consumer, producer *vnet.Net, stunURL string, turnServer webrtc.ICEServer) {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "1.2.3.0/24", LoggerFactory: lf})
	require.NoError(t, err)

	srvNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"1.2.3.1"}})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(srvNet))

	lan := func(cidr, public, local string) *vnet.Net {
		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          cidr,
			StaticIPs:     []string{public},
			NATType:       &vnet.NATType{MappingBehavior: behavior, FilteringBehavior: behavior},
			LoggerFactory: lf,
		})
		require.NoError(t, err)
		require.NoError(t, wan.AddRouter(r))
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{local}})
		require.NoError(t, err)
		require.NoError(t, r.AddNet(n))
		return n
	}
	consumer = lan("10.0.1.0/24", "1.2.3.10", "10.0.1.2")
	producer = lan("10.0.2.0/24", "1.2.3.20", "10.0.2.2")
	require.NoError(t, wan.Start())
	t.Cleanup(func() { _ = wan.Stop() })

	pc, err := srvNet.ListenPacket("udp4", "1.2.3.1:3478")
	require.NoError(t, err)
	key := turn.GenerateAuthKey("u", "test", "p")
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == "u"
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("1.2.3.1"),
				Address:      "1.2.3.1",
				Net:          srvNet,
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	return consumer, producer, "stun:1.2.3.1:3478",
		webrtc.ICEServer{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"}
}

// connectICE connects a consumer's and a producer's ICE sessions as the FSMs do: credentials and
// candidates swapped, then dial and accept at once.
func connectICE(t *testing.T, consumerNet, producerNet transport.Net, servers []webrtc.ICEServer, tuning WebRTCTuning) (consumer, producer *iceSession) {
	t.Helper()
	newSession := func(n transport.Net, controlling bool) *iceSession {
		o := NewDefaultWebRTCOptions()
		o.Net = n
		o.Tuning = tuning
		s, err := newICESession(o, controlling, servers, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	consumer, producer = newSession(consumerNet, true), newSession(producerNet, false)
	cu, cp, err := consumer.credentials()
	require.NoError(t, err)
	pu, pp, err := producer.credentials()
	require.NoError(t, err)
	pCands, _, err := producer.gather(0)
	require.NoError(t, err)
	cCands, _, err := consumer.gather(0)
	require.NoError(t, err)
	require.NoError(t, consumer.addRemote(pCands))
	require.NoError(t, producer.addRemote(cCands))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	accepted := make(chan error, 1)
	go func() { accepted <- producer.connect(ctx, cu, cp) }()
	require.NoError(t, consumer.connect(ctx, pu, pp))
	require.NoError(t, <-accepted)
	return consumer, producer
}

// exchange checks that a full-size datagram crosses from a to b intact.
func exchange(t *testing.T, a, b net.PacketConn) {
	t.Helper()
	msg := bytes.Repeat([]byte{0xab}, 1200)
	_, err := a.WriteTo(msg, nil)
	require.NoError(t, err)
	require.NoError(t, b.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1500)
	n, from, err := b.ReadFrom(buf)
	require.NoError(t, err)
	require.NotNil(t, from, "a QUIC endpoint keys connections by the address ReadFrom returns")
	assert.Equal(t, msg, buf[:n])
}

func TestICESessionPunched(t *testing.T) {
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, WebRTCTuning{})
	assert.False(t, consumer.relayed())
	exchange(t, consumer.packetConn(), producer.packetConn())
	exchange(t, producer.packetConn(), consumer.packetConn())
}

func TestICESessionRelayed(t *testing.T) {
	cn, pn, stunURL, turnServer := newVNetICE(t, vnet.EndpointAddrPortDependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}, turnServer}, WebRTCTuning{})
	assert.True(t, consumer.relayed())
	exchange(t, consumer.packetConn(), producer.packetConn())
	exchange(t, producer.packetConn(), consumer.packetConn())
}

// When one side goes away, the other's session ends, so its FSM moves on.
func TestICESessionEndsWhenPeerCloses(t *testing.T) {
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	fast := WebRTCTuning{ICEDisconnectedTimeout: time.Second, ICEFailedTimeout: 2 * time.Second, ICEKeepaliveInterval: 200 * time.Millisecond}
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, fast)
	require.NoError(t, producer.Close())
	select {
	case <-producer.done():
	case <-time.After(time.Second):
		t.Fatal("closing a session didn't end it")
	}
	select {
	case <-consumer.done():
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer's session outlived the producer's")
	}
	_, _, err := consumer.packetConn().ReadFrom(make([]byte, 1500))
	assert.Error(t, err, "reads fail once the session ends")
}

func TestSummarizeICECandidates(t *testing.T) {
	host := "1 1 udp 2130706431 10.0.0.2 5000 typ host"
	srflx := "2 1 udp 1694498815 8.8.8.8 6000 typ srflx raddr 10.0.0.2 rport 5000"
	nonHost, public := summarizeICECandidates([]string{host})
	assert.False(t, nonHost)
	assert.Nil(t, public)
	nonHost, public = summarizeICECandidates([]string{host, srflx, "not a candidate"})
	assert.True(t, nonHost)
	assert.Equal(t, "8.8.8.8", public.String())
}

// A candidate that can't be parsed is refused with errBadCandidate, which never quotes it: the
// candidate is the peer's address.
func TestAddRemoteRefusesBadCandidateWithoutQuotingIt(t *testing.T) {
	cn, _, _, _ := newVNetICE(t, vnet.EndpointIndependent)
	o := NewDefaultWebRTCOptions()
	o.Net = cn
	s, err := newICESession(o, true, nil, time.Second)
	require.NoError(t, err)
	defer s.Close()
	err = s.addRemote([]string{"1 1 udp 1 203.0.113.9 notaport typ host"})
	assert.ErrorIs(t, err, errBadCandidate)
	assert.NotContains(t, err.Error(), "203.0.113.9")
}

func TestICEURLsCarryTURNCredentials(t *testing.T) {
	urls, err := iceURLs([]webrtc.ICEServer{
		{URLs: []string{"stun:1.2.3.1:3478"}},
		{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"},
	})
	require.NoError(t, err)
	require.Len(t, urls, 2)
	assert.Empty(t, urls[0].Username)
	assert.Equal(t, "u", urls[1].Username)
	assert.Equal(t, "p", urls[1].Password)
}
```

- [ ] **Step 2: Run them and check they fail.** Run `go test ./clientcore/ -run 'ICESession|SummarizeICE|AddRemoteRefuses|ICEURLs' -v`. Expected: the build fails with `undefined: newICESession`.

- [ ] **Step 3: Add `iceOptions` to `clientcore/tuning_native.go`.** Add `"github.com/pion/ice/v4"` to the imports, then add:

```go
// iceOptions is apply for a bare ICE agent: the ICE fields alone, since it has no DTLS or SCTP.
func (t WebRTCTuning) iceOptions() []ice.AgentOption {
	var opts []ice.AgentOption
	if t.ICEMaxBindingRequests > 0 {
		opts = append(opts, ice.WithMaxBindingRequests(t.ICEMaxBindingRequests))
	}
	if t.STUNGatherTimeout > 0 {
		opts = append(opts, ice.WithSTUNGatherTimeout(t.STUNGatherTimeout))
	}
	if t.ICEDisconnectedTimeout > 0 || t.ICEFailedTimeout > 0 || t.ICEKeepaliveInterval > 0 {
		opts = append(opts,
			ice.WithDisconnectedTimeout(orDefault(t.ICEDisconnectedTimeout, pionICEDisconnectedTimeout)),
			ice.WithFailedTimeout(orDefault(t.ICEFailedTimeout, pionICEFailedTimeout)),
			ice.WithKeepaliveInterval(orDefault(t.ICEKeepaliveInterval, pionICEKeepaliveInterval)),
		)
	}
	if t.SrflxAcceptanceMinWait > 0 {
		opts = append(opts, ice.WithSrflxAcceptanceMinWait(t.SrflxAcceptanceMinWait))
	}
	return opts
}
```

- [ ] **Step 4: Write `clientcore/ice_session.go`.**

```go
//go:build !wasm

package clientcore

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

// errBadCandidate refuses a peer's candidate that can't be parsed. It never quotes the candidate,
// which is the peer's address.
var errBadCandidate = errors.New("bad ICE candidate")

// iceSession is a direct exit session's transport: a bare ICE agent, with no DTLS or SCTP, whose
// conn carries the exit's QUIC datagrams. The consumer controls it and dials, and the producer
// accepts. It punches a hole or relays through TURN as a peer connection's agent would, with the
// same transport, UDP mux, mapped address and tuning.
type iceSession struct {
	agent       *ice.Agent
	controlling bool
	conn        *ice.Conn

	mu        sync.Mutex
	gathered  []string
	complete  chan struct{}
	gatherEnd sync.Once
	relay     chan struct{}
	relayOnce sync.Once
	ended     chan struct{}
	endOnce   sync.Once
}

// newICESession creates a session's agent on options' transport, with servers' STUN and TURN
// URLs. window is how long the session waits to connect, which sizes its checks as checkingFor
// does for a peer connection.
func newICESession(options *WebRTCOptions, controlling bool, servers []webrtc.ICEServer, window time.Duration) (*iceSession, error) {
	urls, err := iceURLs(servers)
	if err != nil {
		return nil, err
	}
	pn := iceNetOf(options)
	n := pn.net
	if n == nil {
		if n, err = stdnet.NewNet(); err != nil {
			return nil, err
		}
	}
	opts := []ice.AgentOption{ice.WithUrls(urls), ice.WithNet(boundedDialNet{n})}
	if pn.mux != nil {
		opts = append(opts, ice.WithUDPMux(pn.mux))
		if pn.externalIP != "" {
			// As applyICENet does for a peer connection: a host candidate on the mapped address,
			// appended so LAN peers keep the local one.
			opts = append(opts, ice.WithAddressRewriteRules(ice.AddressRewriteRule{
				External:        []string{pn.externalIP},
				AsCandidateType: ice.CandidateTypeHost,
				Mode:            ice.AddressRewriteAppend,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			}))
		}
	}
	opts = append(opts, options.Tuning.checkingFor(window).iceOptions()...)
	agent, err := ice.NewAgentWithOptions(opts...)
	if err != nil {
		return nil, err
	}
	s := &iceSession{
		agent:       agent,
		controlling: controlling,
		complete:    make(chan struct{}),
		relay:       make(chan struct{}),
		ended:       make(chan struct{}),
	}
	if err := agent.OnCandidate(s.onCandidate); err != nil {
		_ = agent.Close()
		return nil, err
	}
	if err := agent.OnConnectionStateChange(s.onState); err != nil {
		_ = agent.Close()
		return nil, err
	}
	return s, nil
}

// iceURLs is servers' URLs as the agent takes them, with each TURN server's credentials.
func iceURLs(servers []webrtc.ICEServer) ([]*stun.URI, error) {
	var urls []*stun.URI
	for _, s := range servers {
		for _, raw := range s.URLs {
			u, err := stun.ParseURI(raw)
			if err != nil {
				return nil, err
			}
			if u.Scheme == stun.SchemeTypeTURN || u.Scheme == stun.SchemeTypeTURNS {
				u.Username = s.Username
				if cred, ok := s.Credential.(string); ok {
					u.Password = cred
				}
			}
			urls = append(urls, u)
		}
	}
	return urls, nil
}

func (s *iceSession) onCandidate(c ice.Candidate) {
	if c == nil {
		s.gatherEnd.Do(func() { close(s.complete) })
		return
	}
	s.mu.Lock()
	s.gathered = append(s.gathered, c.Marshal())
	s.mu.Unlock()
	if c.Type() == ice.CandidateTypeRelay {
		s.relayOnce.Do(func() { close(s.relay) })
	}
}

func (s *iceSession) onState(state ice.ConnectionState) {
	slog.Debug("ICE session state change", "state", state.String())
	switch state {
	case ice.ConnectionStateFailed, ice.ConnectionStateDisconnected, ice.ConnectionStateClosed:
		s.endOnce.Do(func() { close(s.ended) })
	}
}

// credentials is the session's own ufrag and pwd, for the other side.
func (s *iceSession) credentials() (ufrag, pwd string, err error) {
	return s.agent.GetLocalUserCredentials()
}

// gather gathers the session's candidates and returns them as ice.Candidate.Marshal writes them.
// It waits as awaitGathering does: with limit set, it goes on shortly after the first relay
// candidate, or at the limit.
func (s *iceSession) gather(limit time.Duration) ([]string, gatherEnd, error) {
	var firstRelay <-chan struct{}
	if limit > 0 {
		firstRelay = s.relay
	}
	if err := s.agent.GatherCandidates(); err != nil {
		return nil, 0, err
	}
	end := awaitGathering(s.complete, firstRelay, turnRelayGrace, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.gathered), end, nil
}

// addRemote adds the other side's candidates.
func (s *iceSession) addRemote(raw []string) error {
	for _, r := range raw {
		c, err := ice.UnmarshalCandidate(r)
		if err != nil {
			return errBadCandidate
		}
		if err := s.agent.AddRemoteCandidate(c); err != nil {
			return err
		}
	}
	return nil
}

// connect dials the other side, or accepts its dial, with its credentials, until ctx ends.
func (s *iceSession) connect(ctx context.Context, ufrag, pwd string) error {
	var conn *ice.Conn
	var err error
	if s.controlling {
		conn, err = s.agent.Dial(ctx, ufrag, pwd)
	} else {
		conn, err = s.agent.Accept(ctx, ufrag, pwd)
	}
	if err != nil {
		return err
	}
	s.conn = conn
	return nil
}

// packetConn is the connected session's conn as the packet conn a QUIC endpoint takes.
func (s *iceSession) packetConn() net.PacketConn {
	return newICEPacketConn(s.conn)
}

// relayed reports whether the selected pair goes through a TURN relay on either side.
func (s *iceSession) relayed() bool {
	pair, err := s.agent.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return false
	}
	return pair.Local.Type() == ice.CandidateTypeRelay || pair.Remote.Type() == ice.CandidateTypeRelay
}

// done is closed when the session fails, disconnects or is closed.
func (s *iceSession) done() <-chan struct{} {
	return s.ended
}

// Close ends the session. It is an io.Closer so closeWorkerResource can close a session a
// stopped worker held.
func (s *iceSession) Close() error {
	return s.agent.Close()
}

// icePacketConn is an ICE conn as a packet conn with one peer. Its addresses are taken once, when
// it is made, so a QUIC endpoint keyed on them sees one stable peer even if ICE changes pairs.
type icePacketConn struct {
	*ice.Conn
	local, peer net.Addr
}

func newICEPacketConn(c *ice.Conn) icePacketConn {
	local, peer := c.LocalAddr(), c.RemoteAddr()
	if local == nil {
		local = &net.UDPAddr{}
	}
	if peer == nil {
		peer = &net.UDPAddr{}
	}
	return icePacketConn{Conn: c, local: local, peer: peer}
}

func (c icePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(p)
	return n, c.peer, err
}

func (c icePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Conn.Write(p)
}

func (c icePacketConn) LocalAddr() net.Addr {
	return c.local
}

// summarizeICECandidates reports whether any of a peer's candidates is server reflexive or
// relayed, which only a STUN or TURN server that answered could give, and the last public address
// among them, for the UI's geolocation. Candidates that can't be parsed are skipped.
func summarizeICECandidates(raw []string) (nonHost bool, public net.IP) {
	for _, r := range raw {
		c, err := ice.UnmarshalCandidate(r)
		if err != nil {
			continue
		}
		if c.Type() != ice.CandidateTypeHost {
			nonHost = true
		}
		if isPublicIP(c.Address()) {
			public = net.ParseIP(c.Address())
		}
	}
	return nonHost, public
}
```

- [ ] **Step 5: Write `clientcore/ice_session_wasm.go`.**

```go
//go:build wasm

package clientcore

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/pion/webrtc/v4"
)

// A browser has no bare ICE agent, and a widget never offers a direct exit (WebRTCOptions.Exit is
// nil there) and never runs a consumer, so these are never reached.
var errICEUnavailable = errors.New("direct exit sessions are native only")

var errBadCandidate = errors.New("bad ICE candidate")

type iceSession struct{}

func newICESession(*WebRTCOptions, bool, []webrtc.ICEServer, time.Duration) (*iceSession, error) {
	return nil, errICEUnavailable
}

func (s *iceSession) credentials() (string, string, error)          { return "", "", errICEUnavailable }
func (s *iceSession) gather(time.Duration) ([]string, gatherEnd, error) { return nil, 0, errICEUnavailable }
func (s *iceSession) addRemote([]string) error                     { return errICEUnavailable }
func (s *iceSession) connect(context.Context, string, string) error { return errICEUnavailable }
func (s *iceSession) packetConn() net.PacketConn                   { return nil }
func (s *iceSession) relayed() bool                                { return false }
func (s *iceSession) done() <-chan struct{}                        { return nil }
func (s *iceSession) Close() error                                 { return nil }

func summarizeICECandidates([]string) (bool, net.IP) { return false, nil }
```

  Run `gofmt -w clientcore/ice_session_wasm.go`.

- [ ] **Step 6: Run the tests and check they pass.** Run `go test ./clientcore/ -race -run 'ICESession|SummarizeICE|AddRemoteRefuses|ICEURLs' -v -count=3`. Expected: PASS all three times.
  - **Relayed case still punched:** if `TestICESessionRelayed` finds `relayed()` false, the vnet NAT isn't symmetric. Check `vnet.NATType` in `$(go list -m -f '{{.Dir}}' github.com/pion/transport/v5)/vnet/nat.go` and set the behavior it uses for symmetric NATs. Don't weaken the assertion.
  - **Punched case reports a relay:** if `TestICESessionPunched` reports `relayed()` true, no TURN server is in that test's servers. Look for an extra relay source.

- [ ] **Step 7: Run the whole package and the wasm build.**

```bash
go test ./clientcore/ -race
GOOS=js GOARCH=wasm go build ./clientcore/
go vet ./clientcore/
```

  Expected: all pass. The wasm build succeeding confirms the stubs match the native signatures.

- [ ] **Step 8: Commit.** Use the message "clientcore: bare ICE sessions for direct exits", with the trailers from Global Constraints.

```bash
git add clientcore/ice_session.go clientcore/ice_session_wasm.go clientcore/ice_session_test.go clientcore/tuning_native.go
git commit
```

---

### Task 4: Producer, offering and serving direct exits

**Files:**
- Modify: `clientcore/producer.go`, in state 2 at lines 293-352 and state 5 after line 713. Add states 6 and 7 after state 5, before the closing `})` of the `[]FSMstate` at line 830.
- Create: `clientcore/producer_exit_test.go` (`//go:build !wasm`).

**Interfaces:**
- Consumes:
  - Task 1's types;
  - Task 2's `exitReady`, `exitParams` and `observeSession`;
  - Task 3's `iceSession` methods and `summarizeICECandidates`;
  - existing `natFailWindow`, `turnGatherWait`, `logGatherEnd`, `offerPairing`, `vpnStatus`, `signaler.signal`, `common.Outcome*` and `common.PathRelay`/`PathDirect`.
- Produces:
  - **State 2** returns `6, []interface{}{replyTo string, offer common.OfferMsg, servers []webrtc.ICEServer, roomTURN bool}` for an offer with `ICE` set.
  - **State 6** returns `7, []interface{}{s *iceSession, offer common.OfferMsg, consumerSessionID string, remoteAddr net.IP, password string}`, or `0`.
  - **State 7** sends `common.ConsumerInfo{..., Exit: true}`, runs `options.Exit.Serve`, and then returns `0`.

- [ ] **Step 1: Write the failing tests.** Create `clientcore/producer_exit_test.go`:

```go
//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// producerState2 runs producer state 2 against a Freddie that answers each genesis with reply,
// and returns the next state, its input, and the genesis message the producer sent.
func producerState2(t *testing.T, o *WebRTCOptions, reply []byte) (int, []interface{}, common.GenesisMsg, *webrtc.PeerConnection) {
	t.Helper()
	var sent common.GenesisMsg
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.NoError(t, json.Unmarshal([]byte(r.Form.Get("data")), &sent))
		_, _ = w.Write(reply)
	}))
	t.Cleanup(srv.Close)
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 10 * time.Millisecond
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:1.2.3.1:3478"}}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	pa := common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}
	next, input := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc, pa, make(chan *webrtc.DataChannel, 1), make(chan webrtc.PeerConnectionState, 16), make(chan struct{}, 1),
	})
	return next, input, sent, pc
}

func readyExit() *ExitOptions {
	return &ExitOptions{
		Ready:  func() bool { return true },
		Params: func() (string, string, bool) { return "pin", "pw", true },
		Serve:  func(context.Context, net.PacketConn, string) {},
	}
}

func offerReply(t *testing.T, offer common.OfferMsg, servers []webrtc.ICEServer) []byte {
	t.Helper()
	p, err := json.Marshal(offer)
	require.NoError(t, err)
	b, err := json.Marshal(common.SignalMsg{ReplyTo: "offer-id", Type: common.SignalMsgOffer, Payload: string(p), ICEServers: servers})
	require.NoError(t, err)
	return b
}

func TestProducerGenesisOffersDirectExitOnlyWhenReady(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	_, _, sent, _ := producerState2(t, o, nil)
	assert.False(t, sent.DirectExit, "a producer without Exit, such as a widget")

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	o.Exit.Ready = func() bool { return false }
	_, _, sent, _ = producerState2(t, o, nil)
	assert.False(t, sent.DirectExit, "a producer that can't exit now")

	o = NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	_, _, sent, _ = producerState2(t, o, nil)
	assert.True(t, sent.DirectExit)
}

func TestProducerRoutesICEOfferToDirectExit(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	turnServers := []webrtc.ICEServer{{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"}}
	offer := common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}, Tag: "tag"}
	next, input, _, pc := producerState2(t, o, offerReply(t, offer, turnServers))
	require.Equal(t, 6, next)
	assert.Equal(t, "offer-id", input[0])
	assert.Equal(t, offer, input[1])
	servers := input[2].([]webrtc.ICEServer)
	require.Len(t, servers, 2, "the STUN cohort and the room's TURN server")
	assert.Equal(t, "stun:1.2.3.1:3478", servers[0].URLs[0])
	assert.Equal(t, true, input[3])
	assert.Equal(t, webrtc.PeerConnectionStateClosed, pc.ConnectionState(), "a direct exit needs no peer connection")
}

func TestProducerRoutesSDPOfferAsBefore(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.Exit = readyExit()
	next, _, _, _ := producerState2(t, o, offerReply(t, common.OfferMsg{Tag: "tag"}, nil))
	assert.Equal(t, 3, next, "an old consumer's SDP offer gets today's path")
}

// An ICE offer when the producer can no longer exit is dropped without an answer, so the consumer
// moves on to another genesis.
func TestProducerDropsICEOfferWhenItCantExit(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1) }))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Exit = readyExit()
	o.Exit.Params = func() (string, string, bool) { return "", "", false }
	fsm := newProducerWebRTC(o, nil, false, newVolunteerSignaler(o))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{
		"offer-id", common.OfferMsg{ICE: &common.ICEOffer{Ufrag: "u", Pwd: "p"}}, []webrtc.ICEServer(nil), false,
	})
	assert.Equal(t, 0, next)
	assert.Zero(t, posts.Load(), "no answer is sent")
}
```

- [ ] **Step 2: Run them and check they fail.** Run `go test ./clientcore/ -run 'TestProducer(GenesisOffers|RoutesICE|RoutesSDP|DropsICE)' -v`. Expected: FAIL.
  - `TestProducerGenesisOffersDirectExitOnlyWhenReady` fails on `assert.True(t, sent.DirectExit)`.
  - `TestProducerRoutesICEOfferToDirectExit` gets state 3, not 6.
  - `TestProducerDropsICEOfferWhenItCantExit` panics with an index out of range on `fsm.state[6]`.

  If the genesis test fails earlier because state 2 goes back to state 0 on `preflight.expired`, a fresh `preflightState` counts as expired. In that case:
  - Give the test's FSM a pass by running `fsm.state[0]` first with `o.STUNBatch` returning `[]string{"stun:1.2.3.1:3478"}` and `o.Net` set to a vnet from `newVNetICE`.
  - Or, simpler: read `preflightState.expired` and set its fields from the test, since the test is in the same package.

  Don't change `preflightState`.

- [ ] **Step 3: Offer a direct exit in the genesis.** In state 2, replace the genesis construction:

```go
			// Construct a genesis message. While the producer can exit traffic itself, it offers
			// a direct exit, which a consumer that can take one answers with an ICE offer.
			g, err := json.Marshal(common.GenesisMsg{PathAssertion: pa, DirectExit: exitReady(options)})
```

- [ ] **Step 4: Route an ICE offer to state 6.** In state 2, replace the final `return 3, ...` and its TODO comment:

```go
			// An ICE offer takes a direct exit, which needs no peer connection. The session gets the
			// peer connection's STUN servers, from state 0, and Freddie's TURN servers for a room.
			if o, ok := offer.(common.OfferMsg); ok && o.ICE != nil {
				servers := append(peerConnection.GetConfiguration().ICEServers, offerMsg.ICEServers...)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 6, []interface{}{replyTo, o, servers, len(offerMsg.ICEServers) > 0}
			}

			// TODO: here we assume we've received a valid offer SDP, we also need to handle invalid case
			return 3, []interface{}{peerConnection, replyTo, offer, connectionEstablished, connectionChange, connectionClosed, offerMsg.ICEServers}
```

- [ ] **Step 5: Report the relayed session.** In state 5, right after the `ConsumerInfoIPC` send succeeds, add:

```go
			// An exit-capable producer counts the sessions that still relay to the egress server.
			observeSession(options, false)
```

- [ ] **Step 6: Add state 6, the producer's ICE handshake.** Add this after state 5's `}),`:

```go
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 6: a direct exit's ICE handshake
			// input[0]: string (replyTo)
			// input[1]: common.OfferMsg (with ICE)
			// input[2]: []webrtc.ICEServer (the STUN cohort, and a room pairing's TURN relays)
			// input[3]: bool (whether input[2] has a room pairing's TURN relays)
			replyTo := input[0].(string)
			offer := input[1].(common.OfferMsg)
			servers := input[2].([]webrtc.ICEServer)
			roomTURN := input[3].(bool)
			slog.Debug("Producer state 6, direct exit handshake...")
			last.begin(replyTo, vpnStatus(options))
			last.natFail = natFailWindow(options.NATFailTimeout, roomTURN)

			// The producer may have stopped being able to exit since its genesis. The offer then
			// goes unanswered, and the consumer moves on.
			certPin, password, ok := exitParams(options)
			if !ok {
				slog.Debug("Can't exit now, dropping the direct exit offer")
				return 0, []interface{}{}
			}

			s, err := newICESession(options, false, servers, last.natFail)
			if err != nil {
				slog.Debug("Error creating ICE session", "error", err)
				return 0, []interface{}{}
			}
			ufrag, pwd, err := s.credentials()
			if err != nil {
				slog.Debug("Error reading ICE credentials", "error", err)
				s.Close()
				return 0, []interface{}{}
			}

			var gatherLimit time.Duration
			if roomTURN {
				gatherLimit = turnGatherWait
			}
			gatherStart := time.Now()
			candidates, end, err := s.gather(gatherLimit)
			if err != nil {
				slog.Debug("Error gathering ICE candidates", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			logGatherEnd(end, time.Since(gatherStart))

			// As in state 3: a cohort that gave only host candidates is dropped, and without a
			// mapped address there is nothing to offer.
			if nonHost, _ := summarizeICECandidates(candidates); !nonHost {
				if !cohortDropped {
					scache.drop()
					preflight.invalidate()
					cohortDropped = true
					slog.Debug("Dropped the current STUN cohort (reason: ICE failed)")
				}
				if !iceNetOf(options).mapped() {
					slog.Debug("ICE failed to gather any non-host candidates, aborting!")
					last.record(common.OutcomeNoLocalSrflx, nil)
					s.Close()
					return 0, []interface{}{}
				}
			}

			a, err := json.Marshal(common.ICEAnswerMsg{ICE: &common.ICEAnswer{
				Ufrag:      ufrag,
				Pwd:        pwd,
				Candidates: candidates,
				CertPin:    certPin,
				Password:   password,
			}})
			if err != nil {
				slog.Debug("Error marshaling JSON", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			form := url.Values{
				"data":    {string(a)},
				"send-to": {replyTo},
				"type":    {strconv.Itoa(int(common.SignalMsgAnswer))},
			}
			statusCode, iceBytes, err := signaler.signal(ctx, form)
			if err != nil {
				slog.Debug("Couldn't signal the direct exit answer", "error", err)
				sleepOrDone(ctx, options.ErrorBackoff)
				s.Close()
				return 0, []interface{}{}
			}
			switch statusCode {
			case http.StatusOK:
			case http.StatusNotFound:
				slog.Debug("Signaling partner hung up, aborting!")
				last.record(common.OutcomePartnerHungUp, nil)
				s.Close()
				return 0, []interface{}{}
			default:
				slog.Debug("Unexpected http status code", "status_code", statusCode)
				sleepOrDone(ctx, options.ErrorBackoff)
				s.Close()
				return 0, []interface{}{}
			}
			if len(iceBytes) == 0 {
				slog.Debug("No ICE candidates from signaling partner!")
				last.record(common.OutcomeNoConsumerICE, nil)
				s.Close()
				return 0, []interface{}{}
			}

			// Never logged: the message carries the consumer's addresses.
			_, msg, err := common.DecodeSignalMsg(iceBytes)
			iceMsg, ok := msg.(common.ICEMsg)
			if err != nil || !ok || iceMsg.ConsumerSessionID == "" || len(iceMsg.ICECandidates) == 0 {
				slog.Debug("Bad ICE message from signaling partner, aborting!")
				last.record(common.OutcomeBadConsumerICE, nil)
				s.Close()
				return 0, []interface{}{}
			}
			remoteNonHost, remoteAddr := summarizeICECandidates(iceMsg.ICECandidates)
			if !remoteNonHost && remoteAddr == nil {
				slog.Debug("Signaling partner sent only host type ICE candidates, aborting!")
				last.record(common.OutcomeConsumerHostOnly, nil)
				s.Close()
				return 0, []interface{}{}
			}
			if err := s.addRemote(iceMsg.ICECandidates); err != nil {
				slog.Debug("Error adding ICE candidates", "error", err)
				last.record(common.OutcomeBadConsumerICE, nil)
				s.Close()
				return 0, []interface{}{}
			}

			connectCtx, cancel := context.WithTimeout(ctx, last.natFail)
			err = s.connect(connectCtx, offer.ICE.Ufrag, offer.ICE.Pwd)
			cancel()
			if err != nil {
				s.Close()
				if ctx.Err() != nil {
					return 0, []interface{}{}
				}
				slog.Debug("NAT traversal timeout, aborting!", "nat_fail_window", last.natFail)
				last.record(common.OutcomeNATTimeout, nil)
				return 0, []interface{}{}
			}
			slog.Debug("A direct exit session has been established!", "relayed", s.relayed())
			last.record(common.OutcomeConnected, nil)
			last.path = common.PathDirect
			if s.relayed() {
				last.path = common.PathRelay
			}
			return 7, []interface{}{s, offer, iceMsg.ConsumerSessionID, remoteAddr, password}
		}),
```

- [ ] **Step 7: Add state 7, serving the session.** Add this after state 6:

```go
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 7: a direct exit session, served until it ends
			// input[0]: *iceSession
			// input[1]: common.OfferMsg
			// input[2]: string (consumer session ID)
			// input[3]: net.IP
			// input[4]: string (the session's password)
			s := input[0].(*iceSession)
			offer := input[1].(common.OfferMsg)
			consumerSessionID := input[2].(string)
			remoteAddr := input[3].(net.IP)
			password := input[4].(string)
			slog.Debug("Producer state 7...")

			// Exit marks the session as one the egress never serves (see jit_egress_consumer.go).
			if !sendCtx(ctx, com.tx, IPCMsg{
				IpcType: ConsumerInfoIPC,
				Data: common.ConsumerInfo{
					Addr:      remoteAddr,
					Tag:       offer.Tag,
					SessionID: consumerSessionID,
					Country:   offer.Country,
					Pairing:   offerPairing(options, offer.Pairing),
					Exit:      true,
				},
			}) {
				return 0, input
			}
			observeSession(options, true)

			serveCtx, cancel := context.WithCancel(ctx)
			served := make(chan struct{})
			go func() {
				defer close(served)
				options.Exit.Serve(serveCtx, s.packetConn(), password)
			}()

		serveloop:
			for {
				select {
				case <-s.done():
					slog.Debug("Direct exit session ended, resetting!")
					break serveloop
				case <-served:
					slog.Debug("Direct exit service stopped, resetting!")
					break serveloop
				case <-com.rx:
					// Nothing upstream serves this slot while it exits itself, so whatever reaches
					// it is dropped.
				case <-ctx.Done():
					break serveloop
				}
			}
			cancel()
			s.Close() // TODO: there's an err we should handle here
			<-served

			// We've reset this slot, so announce the nil connectivity situation
			if !sendCtx(ctx, com.tx, IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{}}) {
				return 0, []interface{}{}
			}
			return 0, []interface{}{}
		}),
```

- [ ] **Step 8: Run the tests and check they pass.** Run `go test ./clientcore/ -race -run 'TestProducer' -v`. Expected: PASS. Then run `go test ./clientcore/ -race`, `go vet ./clientcore/` and `GOOS=js GOARCH=wasm go build ./clientcore/`. Expected: all pass.

- [ ] **Step 9: Commit.** Use the message "clientcore: producers offer and serve direct exits", with the trailers from Global Constraints.

```bash
git add clientcore/producer.go clientcore/producer_exit_test.go
git commit
```

---

### Task 5: Consumer, taking direct exits

**Files:**
- Modify: `clientcore/consumer.go`.
  - **State 1:** lines 275-372, the genesis collection and the pick.
  - **States 6 and 7:** new, after state 5, before the `})` at line 837.
  - **Helper:** `postSignalTo`, after `subscribeRequest`.
- Create: `clientcore/consumer_exit_test.go` (`//go:build !wasm`).

**Interfaces:**
- Consumes:
  - Tasks 1 to 3;
  - existing `postSignal` (`volunteer_socket.go:268`), `rooms`, `pairing`, `report`, `scache`, `cohortDropped`, `natFailWindow`, `turnGatherWait` and `logGatherEnd`.
- Produces:
  - **State 1** returns `6, []interface{}{replyTo string, servers []webrtc.ICEServer, roomTURN bool}` for a `DirectExit` genesis when `options.OnExitPath != nil`.
  - **State 6** returns `7, []interface{}{s *iceSession, certPin string, password string}`, or `0`.
  - **State 7** calls `options.OnExitPath(ExitPath{...})` and returns `0` when the session ends.
  - `func postSignalTo(ctx context.Context, options *WebRTCOptions, sendTo string, typ common.SignalMsgType, data []byte) (int, []byte, error)`.

- [ ] **Step 1: Write the failing tests.** Create `clientcore/consumer_exit_test.go`. It uses `consumerState1` and `streamingFreddie` from `turn_test.go` and `newTestPeerConnection` from `rooms_test.go`, all in the same package.

```go
//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

func genesisSignal(t *testing.T, replyTo string, directExit bool) common.SignalMsg {
	t.Helper()
	p, err := json.Marshal(common.GenesisMsg{DirectExit: directExit})
	require.NoError(t, err)
	return common.SignalMsg{ReplyTo: replyTo, Type: common.SignalMsgGenesis, Payload: string(p)}
}

func TestConsumerAnswersDirectExitGenesisWithICE(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", true))
	o.OnExitPath = func(ExitPath) {}
	pc, _ := newTestPeerConnection(t)
	next, input := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	require.Equal(t, 6, next)
	assert.Equal(t, "g", input[0])
	assert.Equal(t, false, input[2], "a pool genesis has no TURN servers")
	assert.Equal(t, webrtc.PeerConnectionStateClosed, pc.ConnectionState())
}

func TestConsumerWithoutExitPathOffersSDP(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", true))
	pc, _ := newTestPeerConnection(t)
	next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	assert.Equal(t, 2, next, "an embedder that can't take a direct exit gets today's path")
}

func TestConsumerOffersSDPToOldProducers(t *testing.T) {
	o := streamingFreddie(t, genesisSignal(t, "g", false))
	o.OnExitPath = func(ExitPath) {}
	pc, _ := newTestPeerConnection(t)
	next, _ := consumerState1(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), pc)
	assert.Equal(t, 2, next)
}

// State 6 offers the consumer's ICE credentials as an OfferMsg with ICE set and no SDP, keeping the
// consumer's tag and country. A Freddie that refuses it sends the worker back to state 0.
func TestConsumerICEOffer(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		form = r.Form
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Tag = "tag"
	o.ConsumerCountry = "IR"
	o.OnExitPath = func(ExitPath) {}
	fsm := newConsumerWebRTC(o, nil, newRoomPreference(time.Now))
	next, _ := fsm.state[6](context.Background(), fsm.com, []interface{}{"g", []webrtc.ICEServer(nil), false})
	assert.Equal(t, 0, next)
	require.NotNil(t, form)
	assert.Equal(t, "g", form["send-to"][0])
	assert.Equal(t, "1", form["type"][0], "SignalMsgOffer")
	// Read as the producer reads it: an ICE offer's zero SDP doesn't survive a plain Unmarshal.
	raw, err := json.Marshal(common.SignalMsg{ReplyTo: "c", Type: common.SignalMsgOffer, Payload: form["data"][0]})
	require.NoError(t, err)
	_, decoded, err := common.DecodeSignalMsg(raw)
	require.NoError(t, err)
	offer := decoded.(common.OfferMsg)
	require.NotNil(t, offer.ICE)
	assert.NotEmpty(t, offer.ICE.Ufrag)
	assert.NotEmpty(t, offer.ICE.Pwd)
	assert.Empty(t, offer.SDP.SDP)
	assert.Equal(t, "tag", offer.Tag)
	assert.Equal(t, "IR", offer.Country)
}
```

  Check that `strconv.Itoa(int(common.SignalMsgOffer))` is `"1"`. `SignalMsgGenesis` is iota 0, so `SignalMsgOffer` is 1.

- [ ] **Step 2: Run them and check they fail.** Run `go test ./clientcore/ -run 'TestConsumer(AnswersDirectExit|WithoutExitPath|OffersSDPToOld|ICEOffer)' -v`. Expected: `TestConsumerAnswersDirectExitGenesisWithICE` gets 2 instead of 6, and `TestConsumerICEOffer` panics with an index out of range.

- [ ] **Step 3: Collect `DirectExit` in state 1.** Next to `roomICE`'s declaration, add:

```go
			// The genesis messages that offered a direct exit, by ReplyTo.
			directExits := map[string]bool{}
```

  Change the decode line to keep the payload:

```go
					msg, payload, err := common.DecodeSignal(rawMsg)
```

  Then, right after `genesisCandidates = append(genesisCandidates, msg.ReplyTo)`, add:

```go
					if g, ok := payload.(common.GenesisMsg); ok && g.DirectExit {
						directExits[msg.ReplyTo] = true
					}
```

- [ ] **Step 4: Route a direct exit genesis to state 6.** In state 1, right after `pairing = roomPairings[replyTo]`, add:

```go
			// A producer that offers a direct exit gets an ICE offer instead, when our embedder can
			// take one. The session needs no peer connection. It gets the peer connection's STUN
			// servers, from state 0, and the room's TURN servers.
			if directExits[replyTo] && options.OnExitPath != nil {
				servers := append(peerConnection.GetConfiguration().ICEServers, roomICE[replyTo]...)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 6, []interface{}{replyTo, servers, len(roomICE[replyTo]) > 0}
			}
```

- [ ] **Step 5: Add `postSignalTo`.** Add it after `subscribeRequest`:

```go
// postSignalTo POSTs a consumer's signaling message of type typ to sendTo, and returns Freddie's
// status and reply.
func postSignalTo(ctx context.Context, options *WebRTCOptions, sendTo string, typ common.SignalMsgType, data []byte) (int, []byte, error) {
	return postSignal(ctx, options, url.Values{
		"data":    {string(data)},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(typ))},
	})
}
```

- [ ] **Step 6: Add state 6, the consumer's ICE handshake.** Add this after state 5's `}),`:

```go
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 6: a direct exit's ICE handshake
			// input[0]: string (reply-to UUID)
			// input[1]: []webrtc.ICEServer (the STUN cohort, and a room pairing's TURN relays)
			// input[2]: bool (whether input[1] has a room pairing's TURN relays)
			replyTo := input[0].(string)
			servers := input[1].([]webrtc.ICEServer)
			roomTURN := input[2].(bool)
			slog.Debug("Consumer state 6, direct exit handshake...")

			// As in state 2: until Freddie accepts the offer, the attempt hasn't tested its producer.
			offerAccepted := false
			defer func() {
				if !offerAccepted {
					rooms.abandoned()
				}
			}()

			natFail := natFailWindow(options.NATFailTimeout, roomTURN)
			s, err := newICESession(options, true, servers, natFail)
			if err != nil {
				slog.Debug("Error creating ICE session", "error", err)
				return 0, []interface{}{}
			}
			ufrag, pwd, err := s.credentials()
			if err != nil {
				slog.Debug("Error reading ICE credentials", "error", err)
				s.Close()
				return 0, []interface{}{}
			}

			offerJSON, err := json.Marshal(common.OfferMsg{
				ICE:     &common.ICEOffer{Ufrag: ufrag, Pwd: pwd},
				Tag:     options.Tag,
				Country: options.ConsumerCountry,
				Pairing: pairing,
			})
			if err != nil {
				slog.Debug("Error marshaling JSON", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			status, answerBytes, err := postSignalTo(ctx, options, replyTo, common.SignalMsgOffer, offerJSON)
			if err != nil {
				slog.Debug("Couldn't signal the direct exit offer", "error", err)
				sleepOrDone(ctx, options.ErrorBackoff)
				s.Close()
				return 0, []interface{}{}
			}
			switch status {
			case http.StatusOK:
				offerAccepted = true
			case http.StatusNotFound:
				slog.Debug("Too late for genesis message", "status_code", status)
				s.Close()
				return 0, []interface{}{}
			default:
				slog.Debug("Unexpected http status code", "status_code", status)
				sleepOrDone(ctx, options.ErrorBackoff)
				s.Close()
				return 0, []interface{}{}
			}
			if len(answerBytes) == 0 {
				slog.Debug("No answer for our direct exit offer!")
				s.Close()
				return 0, []interface{}{}
			}

			// Never logged: the answer carries the session's credentials and the producer's
			// addresses.
			answerReplyTo, msg, err := common.DecodeSignalMsg(answerBytes)
			answer, ok := msg.(common.ICEAnswer)
			if err != nil || !ok {
				slog.Debug("Signal message is not a direct exit answer")
				s.Close()
				return 0, []interface{}{}
			}

			var gatherLimit time.Duration
			if roomTURN {
				gatherLimit = turnGatherWait
			}
			gatherStart := time.Now()
			candidates, end, err := s.gather(gatherLimit)
			if err != nil {
				slog.Debug("Error gathering ICE candidates", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			logGatherEnd(end, time.Since(gatherStart))
			if nonHost, _ := summarizeICECandidates(candidates); !nonHost {
				if !cohortDropped {
					scache.drop()
					cohortDropped = true
					slog.Debug("Dropped the current STUN cohort (reason: ICE failed)")
				}
				if !iceNetOf(options).mapped() {
					slog.Debug("ICE failed to gather any non-host candidates, aborting!")
					s.Close()
					return 0, []interface{}{}
				}
			}

			iceJSON, err := json.Marshal(common.ICEMsg{ICECandidates: candidates, ConsumerSessionID: options.ConsumerSessionID})
			if err != nil {
				slog.Debug("Error marshaling JSON", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			status, _, err = postSignalTo(ctx, options, answerReplyTo, common.SignalMsgICE, iceJSON)
			if err != nil {
				slog.Debug("Couldn't signal ICE candidates", "error", err)
				sleepOrDone(ctx, options.ErrorBackoff)
				s.Close()
				return 0, []interface{}{}
			}
			if status != http.StatusOK {
				slog.Debug("Signaling partner hung up, aborting!", "status_code", status)
				s.Close()
				return 0, []interface{}{}
			}

			// The producer has our candidates now, so its checks start alongside ours.
			if err := s.addRemote(answer.Candidates); err != nil {
				slog.Debug("Error adding ICE candidates", "error", err)
				s.Close()
				return 0, []interface{}{}
			}
			connectCtx, cancel := context.WithTimeout(ctx, natFail)
			err = s.connect(connectCtx, answer.Ufrag, answer.Pwd)
			cancel()
			if err != nil {
				slog.Debug("NAT failure, aborting!", "nat_fail_window", natFail)
				s.Close()
				return 0, []interface{}{}
			}
			slog.Debug("A direct exit session has been established!", "relayed", s.relayed())
			return 7, []interface{}{s, answer.CertPin, answer.Password}
		}),
```

- [ ] **Step 7: Add state 7, handing the session to the embedder.** Add this after state 6:

```go
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 7: a direct exit session, handed to the embedder until it ends
			// input[0]: *iceSession
			// input[1]: string (the exit's certificate pin)
			// input[2]: string (the session's password)
			s := input[0].(*iceSession)
			certPin := input[1].(string)
			password := input[2].(string)
			slog.Debug("Consumer state 7...")
			rooms.succeeded()
			report(true, pairing)
			defer report(false, "")

			options.OnExitPath(ExitPath{Conn: s.packetConn(), CertPin: certPin, Password: password, Relayed: s.relayed()})

		exitloop:
			for {
				select {
				case <-s.done():
					slog.Debug("Direct exit session ended, resetting!")
					break exitloop
				case <-com.rx:
					// The QUIC layer has no peer on this path, so whatever it sends here is dropped.
				case <-ctx.Done():
					break exitloop
				}
			}
			s.Close() // TODO: there's an err we should handle here
			return 0, []interface{}{}
		}),
```

- [ ] **Step 8: Run the tests and check they pass.** Run `go test ./clientcore/ -race -run 'TestConsumer' -v`. Expected: PASS, including every existing consumer test. Then run `go test ./clientcore/ -race`, `go vet ./clientcore/` and `GOOS=js GOARCH=wasm go build ./clientcore/`. Expected: all pass.

- [ ] **Step 9: Commit.** Use the message "clientcore: consumers take direct exits from producers that offer them", with the trailers from Global Constraints.

```bash
git add clientcore/consumer.go clientcore/consumer_exit_test.go
git commit
```

---

### Task 6: End to end, with full FSMs over a virtual internet

**Files:**
- Create: `clientcore/exit_e2e_test.go` (`//go:build !wasm`).

**Interfaces:**
- Consumes:
  - Task 3's `newVNetICE`;
  - `NewProducerWebRTC` and `NewConsumerWebRTC`;
  - `WorkerFSM.com`, `.Start()` and `.Stop()`;
  - `ConnectivityCheckIPC` and `PathAssertionIPC`;
  - `common.SignalSendToGenesis`.
- Produces: tests only.

- [ ] **Step 1: Write the tests.** Create `clientcore/exit_e2e_test.go`:

```go
//go:build !wasm

package clientcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// fakeFreddie relays signaling as Freddie does.
//   - A genesis POST is streamed to the next subscriber and waits for an offer.
//   - An ICE message is delivered and answered at once.
//   - Every other message is delivered to the request it replies to, then waits for that
//     request's reply.
//
// With turn set, it delivers each genesis as a room's and stamps turn on it and on the offer it
// delivers, as Freddie does for a pairing.
type fakeFreddie struct {
	mu      sync.Mutex
	next    int
	waiting map[string]chan string
	geneses chan string
	turn    []webrtc.ICEServer
}

func newFakeFreddie(t *testing.T, turnServers []webrtc.ICEServer) string {
	t.Helper()
	f := &fakeFreddie{waiting: map[string]chan string{}, geneses: make(chan string, 64), turn: turnServers}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeFreddie) await() (string, chan string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := strconv.Itoa(f.next)
	ch := make(chan string, 1)
	f.waiting[id] = ch
	return id, ch
}

func (f *fakeFreddie) deliver(to, msg string) bool {
	f.mu.Lock()
	ch, ok := f.waiting[to]
	delete(f.waiting, to)
	f.mu.Unlock()
	if ok {
		ch <- msg
	}
	return ok
}

func (f *fakeFreddie) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		select {
		case g := <-f.geneses:
			fmt.Fprintln(w, g)
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	typ, _ := strconv.Atoi(r.Form.Get("type"))
	id, reply := f.await()
	msg := common.SignalMsg{ReplyTo: id, Type: common.SignalMsgType(typ), Payload: r.Form.Get("data")}
	if r.Form.Get("send-to") == common.SignalSendToGenesis {
		msg.Room = len(f.turn) > 0
		msg.ICEServers = f.turn
		b, _ := json.Marshal(msg)
		f.geneses <- string(b)
	} else {
		if msg.Type == common.SignalMsgOffer {
			msg.ICEServers = f.turn
		}
		b, _ := json.Marshal(msg)
		if !f.deliver(r.Form.Get("send-to"), string(b)) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if msg.Type == common.SignalMsgICE {
			return
		}
	}
	select {
	case m := <-reply:
		_, _ = io.WriteString(w, m)
	case <-time.After(10 * time.Second):
	case <-r.Context().Done():
	}
}

// e2eOptions are a peer's options on n, against Freddie at srv, with the vnet's STUN server.
func e2eOptions(srv string, n transport.Net, stunURL string) *WebRTCOptions {
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv
	o.Net = n
	o.STUNBatch = func(uint32) ([]string, error) { return []string{stunURL}, nil }
	o.STUNBatchSize = 1
	o.Patience = 50 * time.Millisecond
	o.ErrorBackoff = 50 * time.Millisecond
	o.NATFailTimeout = 10 * time.Second
	return o
}

// echoExit is an exit that sends every datagram back, and records each session's kind.
func echoExit(ready bool, sessions chan<- bool) *ExitOptions {
	return &ExitOptions{
		Ready:  func() bool { return ready },
		Params: func() (string, string, bool) { return "pin", "password", ready },
		Serve: func(ctx context.Context, conn net.PacketConn, password string) {
			buf := make([]byte, 1500)
			for {
				n, addr, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				if _, err := conn.WriteTo(buf[:n], addr); err != nil {
					return
				}
			}
		},
		OnSession: func(directExit bool) { sessions <- directExit },
	}
}

// startProducer runs a producer whose upstream always has connectivity to share.
func startProducer(t *testing.T, o *WebRTCOptions) {
	t.Helper()
	p := NewProducerWebRTC(o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case msg := <-p.com.tx:
				if msg.IpcType == ConnectivityCheckIPC {
					p.com.rx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	p.Start()
	t.Cleanup(func() { p.Stop(); cancel() })
}

func startConsumer(t *testing.T, o *WebRTCOptions) {
	t.Helper()
	c := NewConsumerWebRTC(o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-c.com.tx:
			case <-ctx.Done():
				return
			}
		}
	}()
	c.Start()
	t.Cleanup(func() { c.Stop(); cancel() })
}

func TestDirectExitEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior vnet.EndpointDependencyType
		turn     bool
	}{
		{"punched", vnet.EndpointIndependent, false},
		{"relayed", vnet.EndpointAddrPortDependent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn, pn, stunURL, turnServer := newVNetICE(t, tc.behavior)
			var turnServers []webrtc.ICEServer
			if tc.turn {
				turnServers = []webrtc.ICEServer{turnServer}
			}
			srv := newFakeFreddie(t, turnServers)

			sessions := make(chan bool, 4)
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(true, sessions)
			startProducer(t, po)

			paths := make(chan ExitPath, 1)
			co := e2eOptions(srv, cn, stunURL)
			co.OnExitPath = func(p ExitPath) { paths <- p }
			startConsumer(t, co)

			var path ExitPath
			select {
			case path = <-paths:
			case <-time.After(45 * time.Second):
				t.Fatal("no direct exit session")
			}
			assert.True(t, <-sessions, "the producer counts a direct exit")
			assert.Equal(t, "pin", path.CertPin)
			assert.Equal(t, "password", path.Password)
			assert.Equal(t, tc.turn, path.Relayed)

			msg := bytes.Repeat([]byte{0xcd}, 1200)
			_, err := path.Conn.WriteTo(msg, nil)
			require.NoError(t, err)
			require.NoError(t, path.Conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			buf := make([]byte, 1500)
			n, _, err := path.Conn.ReadFrom(buf)
			require.NoError(t, err)
			assert.Equal(t, msg, buf[:n], "the exit echoed the datagram intact")
		})
	}
}

// A consumer whose embedder can't take a direct exit, or a producer that can't offer one, gets
// today's peer connection.
func TestDirectExitFallsBackToWebRTC(t *testing.T) {
	for _, tc := range []struct {
		name          string
		producerReady bool
		consumerTakes bool
	}{
		{"old consumer", true, false},
		{"flag off", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
			srv := newFakeFreddie(t, nil)

			sessions := make(chan bool, 4)
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(tc.producerReady, sessions)
			startProducer(t, po)

			co := e2eOptions(srv, cn, stunURL)
			if tc.consumerTakes {
				co.OnExitPath = func(ExitPath) { t.Error("a direct exit was taken") }
			}
			startConsumer(t, co)

			select {
			case directExit := <-sessions:
				assert.False(t, directExit)
			case <-time.After(45 * time.Second):
				t.Fatal("no session")
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests.** Run `go test ./clientcore/ -race -run 'TestDirectExit' -v -count=1`. Expected: PASS for all four subtests. Then run `-count=3` to check they're stable.
  - **If a subtest times out,** run it with `BROFLAKE_LOG_LEVEL` or the package's debug logging on (see `common/log.go`) to see which state stalls. Fix the production code if it's at fault.
  - **Keep the timeouts and assertions as written.** Before changing a test, rule out these causes:
    - the producer stuck in state 0's STUN preflight on the vnet;
    - the consumer's `ConsumerSessionID`;
    - the fake freddie's `Room` stamping in the relayed case.

- [ ] **Step 3: Run everything once more.**

```bash
go test ./common/ ./clientcore/ -race
go vet ./...
GOOS=js GOARCH=wasm go build ./clientcore/ ./common/
golangci-lint run --new-from-rev=origin/main ./...
```

  Expected: all pass, with 0 lint issues.

- [ ] **Step 4: Commit.** Use the message "clientcore: direct exit end-to-end tests over a virtual internet", with the trailers from Global Constraints.

```bash
git add clientcore/exit_e2e_test.go
git commit
```

---

## After this plan

- Open the PR with `/create-pr`, then run `/review-pr` with Copilot budget 1. The user merges.
- After the merge, write the lantern-box plan. The spec's lantern-box section consumes `ExitOptions`, `ExitPath` and `OnExitPath` exactly as Task 2 defines them.
