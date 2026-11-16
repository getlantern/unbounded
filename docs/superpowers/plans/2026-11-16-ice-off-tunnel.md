# Keep Unbounded's ICE off the VPN tunnel: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No Unbounded ICE socket sends into the VPN's own tunnel, and pion resolves TURN hosts without the tunnel's DNS, for both lantern-box's consumer outbound and radiance's app widget.

**Architecture:** lantern-box's pion `transport.Net` (`rtcNet`) hides the VPN's own interfaces and resolves hostnames through sing-box's default DNS server. lantern-box exports it as `NewProtectedNet`. radiance's widget uses it through a Net that follows the running tunnel. broflake logs gathered candidates as counts by type.

**Tech Stack:** Go, pion webrtc v4 / ice v4 / transport v5, sing-box (getlantern/sing-box-minimal v1.14.2-lantern.3), sing-tun.

**Spec:** `docs/superpowers/specs/2026-11-16-ice-off-tunnel-design.md` (in the unbounded repo, branch `fisk/ice-off-tunnel`)

## Global Constraints

- Nothing logs or returns in an error an IP, a port, a hostname from an ICE server URL, a key or a credential.
- With no VPN running (no sing-box context, or one without the service), every change behaves exactly as today.
- Each repo's change is its own PR, created with `/create-pr`; the order is broflake, lantern-box, radiance.
- After any `go get`, run `go mod tidy` and commit `go.mod` and `go.sum` together in the same commit as the bump.
- Run each repo's linter as CI does (`golangci-lint run --new-from-rev=origin/main ./...` where the repo has `.golangci.yml`) and `go test -race` for touched packages.
- Commits end with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE
  ```

## Rulings against the spec

- **Ruling:** the candidate log counts by type only (`host=2 srflx=1 prflx=0 relay=1`), not by type and network. Relay is the question the log must answer, and pion's `webrtc.ICECandidate` gives the relay's transport to the TURN server no field of its own. Cost if wrong: one more field later.
- **Ruling:** radiance's widget doesn't restart on VPN connect or disconnect. Its Net looks up the running tunnel on every call instead (`tunnelNet`), so a widget started before the VPN still keeps new sockets off the tunnel. Sockets an attempt already opened stay as they were until that attempt ends. Cost if wrong: an attempt that straddles a VPN change fails once and is retried.

## Repos and worktrees

- broflake (module `github.com/getlantern/broflake`): worktree `/Users/afisk/go/src/github.com/getlantern/unbounded-ice-off-tunnel`, branch `fisk/ice-off-tunnel` (already exists, has the spec and this plan).
- lantern-box: `git -C /Users/afisk/go/src/github.com/getlantern/lantern-box fetch origin && git -C /Users/afisk/go/src/github.com/getlantern/lantern-box worktree add -b fisk/ice-off-tunnel /Users/afisk/go/src/github.com/getlantern/lantern-box-ice-off-tunnel origin/main`
- radiance: `git -C /Users/afisk/go/src/github.com/getlantern/radiance fetch origin && git -C /Users/afisk/go/src/github.com/getlantern/radiance worktree add -b fisk/ice-off-tunnel /Users/afisk/go/src/github.com/getlantern/radiance-ice-off-tunnel origin/main`

---

### Task 1: broflake logs candidate counts

**Files:**
- Modify: `clientcore/turn.go` (add `candidateCounts` after `snapshot`)
- Modify: `clientcore/consumer.go:531`, `clientcore/producer.go:418`
- Test: `clientcore/turn_test.go`

**Interfaces:**
- Produces: `func candidateCounts(cands []webrtc.ICECandidate) string` (package-private).

- [ ] **Step 1: Write the failing test** (append to `clientcore/turn_test.go`)

```go
func TestCandidateCounts(t *testing.T) {
	cands := []webrtc.ICECandidate{
		{Typ: webrtc.ICECandidateTypeHost, Address: "192.0.2.1", Port: 1},
		{Typ: webrtc.ICECandidateTypeHost, Address: "192.0.2.2", Port: 2},
		{Typ: webrtc.ICECandidateTypeSrflx, Address: "198.51.100.1", Port: 3},
		{Typ: webrtc.ICECandidateTypeRelay, Address: "203.0.113.1", Port: 4},
	}
	got := candidateCounts(cands)
	if got != "host=2 srflx=1 prflx=0 relay=1" {
		t.Fatalf("candidateCounts = %q", got)
	}
	for _, c := range cands {
		if strings.Contains(got, c.Address) {
			t.Fatalf("counts %q name an address", got)
		}
	}
	if got := candidateCounts(nil); got != "host=0 srflx=0 prflx=0 relay=0" {
		t.Fatalf("candidateCounts(nil) = %q", got)
	}
}
```

Add `"strings"` to the test file's imports if it isn't there.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./clientcore/ -run TestCandidateCounts`
Expected: FAIL, `undefined: candidateCounts`.

- [ ] **Step 3: Implement** (in `clientcore/turn.go`, after `snapshot`; add `"fmt"` to imports)

```go
// candidateCounts is cands as counts by type, in a fixed order and with no addresses, for logs. Every
// type is listed even at zero, so a log always shows whether a TURN server gave a relay candidate.
func candidateCounts(cands []webrtc.ICECandidate) string {
	var host, srflx, prflx, relay int
	for _, c := range cands {
		switch c.Typ {
		case webrtc.ICECandidateTypeHost:
			host++
		case webrtc.ICECandidateTypeSrflx:
			srflx++
		case webrtc.ICECandidateTypePrflx:
			prflx++
		case webrtc.ICECandidateTypeRelay:
			relay++
		}
	}
	return fmt.Sprintf("host=%d srflx=%d prflx=%d relay=%d", host, srflx, prflx, relay)
}
```

Then replace the two log lines:

`clientcore/consumer.go:531`
```go
			slog.Debug("Local candidates", "counts", candidateCounts(candidates))
```
`clientcore/producer.go:418`
```go
			slog.Debug("Local candidates", "counts", candidateCounts(localCandidates))
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./clientcore/ && go vet ./clientcore/ && GOOS=js GOARCH=wasm go vet ./clientcore/`
Expected: PASS, vet clean on both targets.

- [ ] **Step 5: Commit**

```bash
git add clientcore/turn.go clientcore/turn_test.go clientcore/consumer.go clientcore/producer.go
git commit -m "Log gathered ICE candidates as counts by type

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE"
```

---

### Task 2: lantern-box's rtcNet hides the VPN's own interfaces

Work in `/Users/afisk/go/src/github.com/getlantern/lantern-box-ice-off-tunnel`.

**Files:**
- Modify: `protocol/unbounded/net.go`
- Create: `protocol/unbounded/net_test.go`

**Interfaces:**
- Consumes: `newStdNet` (package var in `protocol/unbounded/ratelimit.go`: `var newStdNet = stdnet.NewNet`).
- Produces: `(*rtcNet).Interfaces`, `InterfaceByIndex`, `InterfaceByName` overriding the embedded `*stdnet.Net`; `func (n *rtcNet) myInterfaces() map[string]bool`.

- [ ] **Step 1: Write the failing test** (`protocol/unbounded/net_test.go`)

```go
package unbounded

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/pion/transport/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMonitor reports names as the VPN's own interfaces. Every other method panics, unused.
type fakeMonitor struct {
	tun.DefaultInterfaceMonitor
	names []string
}

func (m *fakeMonitor) MyInterfaces() []string { return m.names }

// fakeNetworkManager gives rtcNet an interface monitor and nothing else.
type fakeNetworkManager struct {
	adapter.NetworkManager
	monitor tun.DefaultInterfaceMonitor
}

func (m *fakeNetworkManager) InterfaceMonitor() tun.DefaultInterfaceMonitor { return m.monitor }

// hostInterface is a real interface of this machine with an address, which the test pretends is
// the tunnel.
func hostInterface(t *testing.T) string {
	t.Helper()
	ifs, err := net.Interfaces()
	require.NoError(t, err)
	for _, i := range ifs {
		if addrs, _ := i.Addrs(); len(addrs) > 0 {
			return i.Name
		}
	}
	t.Skip("no interface with an address")
	return ""
}

func names(ifs []*transport.Interface) []string {
	var out []string
	for _, i := range ifs {
		out = append(out, i.Name)
	}
	return out
}

func TestRTCNetHidesTheVPNsInterfaces(t *testing.T) {
	mine := hostInterface(t)
	ctx := service.ContextWith[adapter.NetworkManager](context.Background(),
		&fakeNetworkManager{monitor: &fakeMonitor{names: []string{mine}}})
	n, err := newRTCNet(ctx, nil, nil)
	require.NoError(t, err)

	ifs, err := n.Interfaces()
	require.NoError(t, err)
	assert.NotContains(t, names(ifs), mine)

	_, err = n.InterfaceByName(mine)
	assert.True(t, errors.Is(err, transport.ErrInterfaceNotFound), "got %v", err)

	host, err := net.InterfaceByName(mine)
	require.NoError(t, err)
	_, err = n.InterfaceByIndex(host.Index)
	assert.True(t, errors.Is(err, transport.ErrInterfaceNotFound), "got %v", err)
}

func TestRTCNetHidesNothingWithoutAMonitor(t *testing.T) {
	mine := hostInterface(t)
	for name, ctx := range map[string]context.Context{
		"no network manager": context.Background(),
		"no monitor": service.ContextWith[adapter.NetworkManager](context.Background(),
			&fakeNetworkManager{}),
	} {
		t.Run(name, func(t *testing.T) {
			n, err := newRTCNet(ctx, nil, nil)
			require.NoError(t, err)
			ifs, err := n.Interfaces()
			require.NoError(t, err)
			assert.Contains(t, names(ifs), mine)
			_, err = n.InterfaceByName(mine)
			assert.NoError(t, err)
		})
	}
}
```

If the module path of sing-tun's package name differs (`tun`), keep the import as `"github.com/sagernet/sing-tun"`; its package name is `tun`.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./protocol/unbounded/ -run 'TestRTCNetHides'`
Expected: FAIL, the hidden interface is still listed.

- [ ] **Step 3: Implement** (in `protocol/unbounded/net.go`; add imports `"github.com/sagernet/sing-box/adapter"` and `"github.com/sagernet/sing/service"`)

```go
// myInterfaces is the set of interfaces the VPN serving n.ctx registered as its own: its tunnel. It
// is read on each call, since the tunnel comes back under a new name after a restart, and is nil
// with no VPN.
func (n *rtcNet) myInterfaces() map[string]bool {
	nm := service.FromContext[adapter.NetworkManager](n.ctx)
	if nm == nil {
		return nil
	}
	monitor := nm.InterfaceMonitor()
	if monitor == nil {
		return nil
	}
	names := monitor.MyInterfaces()
	if len(names) == 0 {
		return nil
	}
	mine := make(map[string]bool, len(names))
	for _, name := range names {
		mine[name] = true
	}
	return mine
}

// Interfaces lists the host's interfaces for pion's host candidates, without the VPN's own. A
// candidate on the tunnel's address sends into the tunnel, even from this process whose other
// sockets skip it, and physnet would pin its socket there: its checks then reach whichever outbound
// the tunnel routes to, which for a personal proxy is the very helper they are trying to reach.
// Like limitedNet's, the list is fresh each call, since this Net outlives network changes.
func (n *rtcNet) Interfaces() ([]*transport.Interface, error) {
	fresh, err := newStdNet()
	if err != nil {
		return nil, err
	}
	ifs, err := fresh.Interfaces()
	if err != nil {
		return nil, err
	}
	mine := n.myInterfaces()
	if mine == nil {
		return ifs, nil
	}
	kept := make([]*transport.Interface, 0, len(ifs))
	for _, i := range ifs {
		if !mine[i.Name] {
			kept = append(kept, i)
		}
	}
	return kept, nil
}

// InterfaceByIndex is Interfaces' lookup by index: the VPN's own interfaces aren't found.
func (n *rtcNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	fresh, err := newStdNet()
	if err != nil {
		return nil, err
	}
	i, err := fresh.InterfaceByIndex(index)
	if err != nil {
		return nil, err
	}
	if n.myInterfaces()[i.Name] {
		return nil, transport.ErrInterfaceNotFound
	}
	return i, nil
}

// InterfaceByName is Interfaces' lookup by name: the VPN's own interfaces aren't found.
func (n *rtcNet) InterfaceByName(name string) (*transport.Interface, error) {
	if n.myInterfaces()[name] {
		return nil, transport.ErrInterfaceNotFound
	}
	fresh, err := newStdNet()
	if err != nil {
		return nil, err
	}
	return fresh.InterfaceByName(name)
}
```

Check that stdnet's `InterfaceByIndex`/`InterfaceByName` return an error wrapping `transport.ErrInterfaceNotFound` for unknown interfaces; the tests above only rely on the hidden case returning it, which this code does directly.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./protocol/unbounded/ && go vet ./protocol/unbounded/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add protocol/unbounded/net.go protocol/unbounded/net_test.go
git commit -m "unbounded: gather no ICE candidate on the VPN's own interface

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE"
```

---

### Task 3: lantern-box's rtcNet resolves through sing-box's DNS, and NewProtectedNet

**Files:**
- Modify: `protocol/unbounded/net.go` (Resolve methods, replace the closing `Resolve*` comment, add `NewProtectedNet`)
- Modify: `protocol/unbounded/net_test.go`

**Interfaces:**
- Consumes: `newRTCNet(ctx, dialer N.Dialer, logger log.ContextLogger) (*rtcNet, error)`.
- Produces: `func NewProtectedNet(ctx context.Context) (transport.Net, error)` (exported, used by radiance Task 4); `(*rtcNet).ResolveIPAddr`, `ResolveUDPAddr`, `ResolveTCPAddr`; `errResolve`.

- [ ] **Step 1: Write the failing tests** (append to `protocol/unbounded/net_test.go`; add imports `"net/netip"`, `"strings"`, `C "github.com/sagernet/sing-box/constant"`)

```go
type fakeTransport struct {
	adapter.DNSTransport
	typ string
}

func (t *fakeTransport) Type() string { return t.typ }

type fakeTransports struct {
	adapter.DNSTransportManager
	def adapter.DNSTransport
}

func (m *fakeTransports) Default() adapter.DNSTransport { return m.def }

// fakeRouter answers every lookup with addrs, or err, and records the transport it was asked to use.
type fakeRouter struct {
	adapter.DNSRouter
	addrs []netip.Addr
	err   error
	used  adapter.DNSTransport
	asked []string
}

func (r *fakeRouter) Lookup(_ context.Context, domain string, o adapter.DNSQueryOptions) ([]netip.Addr, error) {
	r.used = o.Transport
	r.asked = append(r.asked, domain)
	return r.addrs, r.err
}

func dnsCtx(router *fakeRouter, typ string) context.Context {
	ctx := service.ContextWith[adapter.DNSRouter](context.Background(), router)
	return service.ContextWith[adapter.DNSTransportManager](ctx, &fakeTransports{def: &fakeTransport{typ: typ}})
}

func TestRTCNetResolvesThroughSingBox(t *testing.T) {
	router := &fakeRouter{addrs: []netip.Addr{
		netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("192.0.2.7"),
	}}
	n, err := newRTCNet(dnsCtx(router, C.DNSTypeHTTPS), nil, nil)
	require.NoError(t, err)

	udp, err := n.ResolveUDPAddr("udp4", "turn.example:3478")
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.7:3478", udp.String())
	assert.Equal(t, C.DNSTypeHTTPS, router.used.Type(), "the default transport, set explicitly")

	tcp, err := n.ResolveTCPAddr("tcp6", "turn.example:443")
	require.NoError(t, err)
	assert.Equal(t, "[2001:db8::1]:443", tcp.String())

	ip, err := n.ResolveIPAddr("ip", "turn.example")
	require.NoError(t, err)
	assert.Equal(t, "2001:db8::1", ip.String(), "with no family asked, the first answer")
}

func TestRTCNetPassesLiteralsThrough(t *testing.T) {
	router := &fakeRouter{}
	n, err := newRTCNet(dnsCtx(router, C.DNSTypeHTTPS), nil, nil)
	require.NoError(t, err)
	udp, err := n.ResolveUDPAddr("udp4", "192.0.2.9:3478")
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.9:3478", udp.String())
	assert.Empty(t, router.asked)
}

func TestRTCNetFallsBackWithoutSingBoxDNS(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no router":      context.Background(),
		"fake-IP default": dnsCtx(&fakeRouter{err: errors.New("must not be asked")}, C.DNSTypeFakeIP),
	} {
		t.Run(name, func(t *testing.T) {
			n, err := newRTCNet(ctx, nil, nil)
			require.NoError(t, err)
			udp, err := n.ResolveUDPAddr("udp4", "localhost:3478")
			require.NoError(t, err)
			assert.True(t, udp.IP.IsLoopback())
		})
	}
}

func TestRTCNetResolveErrorsNameNoHost(t *testing.T) {
	for name, router := range map[string]*fakeRouter{
		"lookup fails":  {err: errors.New("lookup secret.example: boom")},
		"wrong family": {addrs: []netip.Addr{netip.MustParseAddr("2001:db8::1")}},
	} {
		t.Run(name, func(t *testing.T) {
			n, err := newRTCNet(dnsCtx(router, C.DNSTypeHTTPS), nil, nil)
			require.NoError(t, err)
			_, err = n.ResolveUDPAddr("udp4", "secret.example:3478")
			require.Error(t, err)
			assert.True(t, errors.Is(err, errResolve))
			assert.False(t, strings.Contains(err.Error(), "secret.example"), "error %q names the host", err)
		})
	}
}

func TestNewProtectedNetWithoutAVPN(t *testing.T) {
	n, err := NewProtectedNet(context.Background())
	require.NoError(t, err)
	ifs, err := n.Interfaces()
	require.NoError(t, err)
	assert.NotEmpty(t, ifs)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./protocol/unbounded/ -run 'TestRTCNetResolves|TestRTCNetPasses|TestRTCNetFallsBack|TestRTCNetResolveErrors|TestNewProtectedNet'`
Expected: FAIL to compile, `undefined: errResolve`, `undefined: NewProtectedNet`.

- [ ] **Step 3: Implement** (in `protocol/unbounded/net.go`; add imports `"errors"`, `"fmt"`, `"net/netip"`, `"strconv"`, `"strings"`, `"time"`, `C "github.com/sagernet/sing-box/constant"`, `"github.com/sagernet/sing-box/common/dialer"`, `"github.com/sagernet/sing-box/option"`)

Replace the trailing comment block that begins `// The Resolve* methods fall through to the embedded stdnet.Net` with:

```go
// errResolve is the error a failed lookup returns. It names no host: the hosts are the ICE servers
// Freddie sends, and errors reach logs.
var errResolve = errors.New("unbounded: resolve ICE server host")

// lookupTimeout bounds a lookup, since pion passes no context.
const lookupTimeout = 5 * time.Second

// lookup resolves host through sing-box's default DNS server, and reports whether it could ask
// sing-box at all. This process's sockets skip the VPN's tunnel, but the OS resolver they would use
// points into it, so stdnet's lookups time out while the VPN runs, and a TURN server given by name
// never gets a relay candidate. Naming the transport skips the DNS rules, and with them the fake-IP
// rule, whose addresses are only routable through the tunnel. With no sing-box DNS on ctx, or a
// fake-IP default, it reports false and the caller asks stdnet, as before.
func (n *rtcNet) lookup(host string) ([]netip.Addr, bool, error) {
	router := service.FromContext[adapter.DNSRouter](n.ctx)
	transports := service.FromContext[adapter.DNSTransportManager](n.ctx)
	if router == nil || transports == nil {
		return nil, false, nil
	}
	def := transports.Default()
	if def == nil || def.Type() == C.DNSTypeFakeIP {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(n.ctx, lookupTimeout)
	defer cancel()
	addrs, err := router.Lookup(ctx, host, adapter.DNSQueryOptions{Transport: def})
	if err != nil {
		return nil, true, errResolve
	}
	return addrs, true, nil
}

// pick is the first of addrs in network's family ("4" or "6" suffix), or the first at all.
func pick(network string, addrs []netip.Addr) (netip.Addr, error) {
	for _, a := range addrs {
		a = a.Unmap()
		switch {
		case strings.HasSuffix(network, "4") && !a.Is4():
		case strings.HasSuffix(network, "6") && !a.Is6():
		default:
			return a, nil
		}
	}
	return netip.Addr{}, errResolve
}

// resolve is the address and port for a "host:port" pion gives, through lookup, or ok=false when
// the host is a literal or sing-box can't be asked.
func (n *rtcNet) resolve(network, address string) (netip.Addr, int, bool, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return netip.Addr{}, 0, false, nil
	}
	if _, err := netip.ParseAddr(host); err == nil || host == "" {
		return netip.Addr{}, 0, false, nil
	}
	addrs, ok, err := n.lookup(host)
	if !ok || err != nil {
		return netip.Addr{}, 0, ok, err
	}
	a, err := pick(network, addrs)
	if err != nil {
		return netip.Addr{}, 0, true, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		port, err = net.LookupPort(network, portStr)
		if err != nil {
			return netip.Addr{}, 0, true, errResolve
		}
	}
	return a, port, true, nil
}

func (n *rtcNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	a, port, ok, err := n.resolve(network, address)
	if !ok {
		return n.Net.ResolveUDPAddr(network, address)
	}
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: a.AsSlice(), Port: port, Zone: a.Zone()}, nil
}

func (n *rtcNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	a, port, ok, err := n.resolve(network, address)
	if !ok {
		return n.Net.ResolveTCPAddr(network, address)
	}
	if err != nil {
		return nil, err
	}
	return &net.TCPAddr{IP: a.AsSlice(), Port: port, Zone: a.Zone()}, nil
}

func (n *rtcNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	if _, err := netip.ParseAddr(address); err == nil || address == "" {
		return n.Net.ResolveIPAddr(network, address)
	}
	addrs, ok, err := n.lookup(address)
	if !ok {
		return n.Net.ResolveIPAddr(network, address)
	}
	if err != nil {
		return nil, err
	}
	a, err := pick(network, addrs)
	if err != nil {
		return nil, err
	}
	return &net.IPAddr{IP: a.AsSlice(), Zone: a.Zone()}, nil
}

// NewProtectedNet is a pion transport.Net for WebRTC run in the process that serves ctx's sing-box
// VPN, such as the app's Unbounded widget: it dials through sing-box's default dialer, binds its
// sockets to the physical interface, gathers no candidate on the VPN's own interface, and resolves
// through sing-box's DNS. With a ctx carrying no sing-box services it behaves as pion's own Net.
func NewProtectedNet(ctx context.Context) (transport.Net, error) {
	d, err := dialer.New(ctx, option.DialerOptions{}, false)
	if err != nil {
		return nil, fmt.Errorf("unbounded: build sing-box dialer: %w", err)
	}
	return newRTCNet(ctx, d, nil)
}
```

Note: the test `TestNewProtectedNetWithoutAVPN` passes `context.Background()`. If `dialer.New` requires a NetworkManager on ctx and fails without one, change `NewProtectedNet` to fall back to `newRTCNet(ctx, nil, nil)` when `service.FromContext[adapter.NetworkManager](ctx) == nil`, and say so in its doc comment. `rtcNet.Dial`/`DialTCP`/`DialUDP`/`ListenPacket` with a nil dialer would panic, so in that fallback also make those four methods use `n.Net`'s (stdnet's) when `n.dialer == nil`. Add a test that `Dial("tcp", "127.0.0.1:1")` on that Net returns an error rather than panicking.

- [ ] **Step 4: Run the tests and lint**

Run: `go test -race ./protocol/unbounded/ && go vet ./... && golangci-lint run --new-from-rev=origin/main ./...`
Expected: PASS, lint clean.

- [ ] **Step 5: Commit**

```bash
git add protocol/unbounded/net.go protocol/unbounded/net_test.go
git commit -m "unbounded: resolve ICE server hosts through sing-box's DNS, and export NewProtectedNet

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE"
```

---

### Task 4: radiance's widget uses the protected Net while the VPN runs

Work in `/Users/afisk/go/src/github.com/getlantern/radiance-ice-off-tunnel`.

**Files:**
- Modify: `go.mod`, `go.sum` (lantern-box to the Task 3 commit)
- Modify: `vpn/vpn.go` (add `BoxContext` after `PhysicalInterfaceControl`)
- Create: `unbounded/net.go`, `unbounded/net_test.go`
- Modify: `unbounded/unbounded.go` (manager field `boxCtx`, `SetBoxContext`, `startLocked` sets `rtcOpt.Net`)
- Modify: `backend/radiance.go:469` (wire `SetBoxContext`)
- Test: `unbounded/rooms_test.go` (widget gets the Net)

**Interfaces:**
- Consumes: `lbunbounded.NewProtectedNet(ctx context.Context) (transport.Net, error)` from `github.com/getlantern/lantern-box/protocol/unbounded`.
- Produces: `func (c *VPNClient) BoxContext() context.Context`; `func SetBoxContext(boxCtx func() context.Context)`; `type tunnelNet`; `func newTunnelNet(boxCtx func() context.Context) *tunnelNet`.

- [ ] **Step 1: Bump lantern-box to the branch commit**

```bash
go get github.com/getlantern/lantern-box@$(git -C /Users/afisk/go/src/github.com/getlantern/lantern-box-ice-off-tunnel rev-parse HEAD)
go mod tidy
git diff --stat
```
The branch must be pushed first (`git -C /Users/afisk/go/src/github.com/getlantern/lantern-box-ice-off-tunnel push -u origin fisk/ice-off-tunnel`) so the module proxy can fetch it; that push happens when Task 3's PR is created. Before the radiance PR merges, re-pin to lantern-box's merge commit or release tag.

- [ ] **Step 2: Write the failing tests** (`unbounded/net_test.go`)

```go
package unbounded

import (
	"context"
	"errors"
	"testing"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taggedNet is a Net the test can tell apart.
type taggedNet struct {
	*stdnet.Net
	tag string
}

func newTagged(t *testing.T, tag string) transport.Net {
	t.Helper()
	n, err := stdnet.NewNet()
	require.NoError(t, err)
	return &taggedNet{Net: n, tag: tag}
}

func TestTunnelNetFollowsTheTunnel(t *testing.T) {
	var box context.Context
	built := 0
	tn := newTunnelNet(func() context.Context { return box })
	tn.newProtected = func(ctx context.Context) (transport.Net, error) {
		built++
		return newTagged(t, "protected"), nil
	}
	tn.newStd = func() (transport.Net, error) { return newTagged(t, "std"), nil }

	n, err := tn.current()
	require.NoError(t, err)
	assert.Equal(t, "std", n.(*taggedNet).tag, "no VPN: the host stack")

	box = context.WithValue(context.Background(), struct{}{}, 1)
	n, err = tn.current()
	require.NoError(t, err)
	assert.Equal(t, "protected", n.(*taggedNet).tag)
	_, err = tn.current()
	require.NoError(t, err)
	assert.Equal(t, 1, built, "one protected Net per tunnel")

	box = context.WithValue(context.Background(), struct{}{}, 2)
	_, err = tn.current()
	require.NoError(t, err)
	assert.Equal(t, 2, built, "a new tunnel gets a new Net")

	box = nil
	n, err = tn.current()
	require.NoError(t, err)
	assert.Equal(t, "std", n.(*taggedNet).tag, "VPN off again: the host stack")
}

func TestTunnelNetWithoutASource(t *testing.T) {
	tn := newTunnelNet(nil)
	ifs, err := tn.Interfaces()
	require.NoError(t, err)
	assert.NotEmpty(t, ifs)
}

func TestTunnelNetPassesErrorsOn(t *testing.T) {
	boom := errors.New("boom")
	tn := newTunnelNet(func() context.Context { return context.Background() })
	tn.newProtected = func(context.Context) (transport.Net, error) { return nil, boom }
	_, err := tn.Interfaces()
	assert.ErrorIs(t, err, boom)
}
```

And in `unbounded/rooms_test.go`, after `TestWidgetReportsTheVPNStatus`:

```go
func TestWidgetGetsATunnelNet(t *testing.T) {
	count, opts, _ := recordingWidgets(t)
	SetBoxContext(func() context.Context { return nil })
	t.Cleanup(func() { SetBoxContext(nil) })
	primeManager(t, nil)
	manager.start()
	waitForCount(t, count, 1, 2*time.Second)
	_, ok := opts(0).Net.(*tunnelNet)
	assert.True(t, ok, "the widget's Net is %T", opts(0).Net)
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./unbounded/ -run 'TestTunnelNet|TestWidgetGetsATunnelNet'`
Expected: FAIL to compile, `undefined: newTunnelNet`, `undefined: SetBoxContext`.

- [ ] **Step 4: Implement**

`unbounded/net.go`:

```go
package unbounded

import (
	"context"
	"net"
	"sync"

	lbunbounded "github.com/getlantern/lantern-box/protocol/unbounded"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// tunnelNet is the widget's pion Net. The widget runs in the VPN's own process, whose sockets skip
// the tunnel, but pion would still gather a candidate on the tunnel's interface and resolve TURN
// hosts through the tunnel's DNS. While a tunnel runs, each call goes to lantern-box's protected Net
// for it; with none, to the host stack as before. It looks the tunnel up on every call, so a widget
// started before the VPN connects, or kept after it disconnects, follows it without a restart.
type tunnelNet struct {
	boxCtx       func() context.Context
	newProtected func(context.Context) (transport.Net, error)
	newStd       func() (transport.Net, error)

	mu  sync.Mutex
	ctx context.Context
	net transport.Net
}

var _ transport.Net = (*tunnelNet)(nil)

func newTunnelNet(boxCtx func() context.Context) *tunnelNet {
	return &tunnelNet{
		boxCtx:       boxCtx,
		newProtected: lbunbounded.NewProtectedNet,
		newStd:       func() (transport.Net, error) { return stdnet.NewNet() },
	}
}

// current is the Net for the running tunnel, built once per tunnel, or a fresh host-stack Net with
// none (stdnet captures the interfaces when it is made).
func (t *tunnelNet) current() (transport.Net, error) {
	var ctx context.Context
	if t.boxCtx != nil {
		ctx = t.boxCtx()
	}
	if ctx == nil {
		return t.newStd()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.net != nil && t.ctx == ctx {
		return t.net, nil
	}
	n, err := t.newProtected(ctx)
	if err != nil {
		return nil, err
	}
	t.ctx, t.net = ctx, n
	return n, nil
}

func (t *tunnelNet) ListenPacket(network, address string) (net.PacketConn, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ListenPacket(network, address)
}

func (t *tunnelNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ListenUDP(network, laddr)
}

func (t *tunnelNet) ListenTCP(network string, laddr *net.TCPAddr) (transport.TCPListener, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ListenTCP(network, laddr)
}

func (t *tunnelNet) Dial(network, address string) (net.Conn, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.Dial(network, address)
}

func (t *tunnelNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.DialUDP(network, laddr, raddr)
}

func (t *tunnelNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.DialTCP(network, laddr, raddr)
}

func (t *tunnelNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ResolveIPAddr(network, address)
}

func (t *tunnelNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ResolveUDPAddr(network, address)
}

func (t *tunnelNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.ResolveTCPAddr(network, address)
}

func (t *tunnelNet) Interfaces() ([]*transport.Interface, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.Interfaces()
}

func (t *tunnelNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.InterfaceByIndex(index)
}

func (t *tunnelNet) InterfaceByName(name string) (*transport.Interface, error) {
	n, err := t.current()
	if err != nil {
		return nil, err
	}
	return n.InterfaceByName(name)
}

// CreateDialer and CreateListenConfig can't fail, so with no Net to hand them to they use the host
// stack's.
func (t *tunnelNet) CreateDialer(d *net.Dialer) transport.Dialer {
	if n, err := t.current(); err == nil {
		return n.CreateDialer(d)
	}
	return stdDialer{d}
}

func (t *tunnelNet) CreateListenConfig(lc *net.ListenConfig) transport.ListenConfig {
	if n, err := t.current(); err == nil {
		return n.CreateListenConfig(lc)
	}
	return lc
}

type stdDialer struct{ *net.Dialer }
```

If `*net.ListenConfig` does not satisfy `transport.ListenConfig` (check its method set in `pion/transport/v5/net.go`), return a fresh stdnet's `CreateListenConfig(lc)` instead, building it with `stdnet.NewNet()` and returning `nil` only if that fails; likewise for `CreateDialer` if `stdDialer` doesn't satisfy `transport.Dialer`.

In `unbounded/unbounded.go`:
- Add to `unboundedManager`, after `vpnStatus func() string`:
  ```go
  	// boxCtx returns the running VPN tunnel's sing-box context, or nil with
  	// none. The widget's WebRTC sockets follow it (see tunnelNet). nil: they
  	// use the host stack.
  	boxCtx func() context.Context
  ```
- Add after `SetVPNStatus`:
  ```go
  // SetBoxContext sets where the widget finds the running VPN tunnel's
  // sing-box context, so its WebRTC sockets stay off the tunnel. A widget
  // already running keeps its source until it restarts.
  func SetBoxContext(boxCtx func() context.Context) {
  	manager.mu.Lock()
  	defer manager.mu.Unlock()
  	manager.boxCtx = boxCtx
  }
  ```
- In `startLocked`, next to `vpnStatus := m.vpnStatus` (line ~848), add `boxCtx := m.boxCtx`, and after `rtcOpt.VPNStatus = vpnStatus` (line ~935) add:
  ```go
  		rtcOpt.Net = newTunnelNet(boxCtx)
  ```

In `vpn/vpn.go`, after `PhysicalInterfaceControl`:

```go
// BoxContext returns the running tunnel's sing-box context, or nil with no
// tunnel running. Code that opens sockets in this process for itself reads the
// tunnel's network and DNS services from it.
func (c *VPNClient) BoxContext() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.tunnel == nil {
		return nil
	}
	return c.tunnel.ctx
}
```

In `backend/radiance.go`, after the `unbounded.SetVPNStatus(...)` line (~469):

```go
		unbounded.SetBoxContext(r.vpnClient.BoxContext)
```

Add a `vpn` package test, in `vpn/vpn_test.go`:

```go
func TestBoxContextWithoutATunnel(t *testing.T) {
	c := &VPNClient{}
	assert.Nil(t, c.BoxContext())
}
```
(Construct `VPNClient` the way the neighbouring tests in that file do if a zero value isn't valid there.)

- [ ] **Step 5: Run the tests and lint**

Run: `go test -race ./unbounded/ ./vpn/ ./backend/ && go vet ./... && golangci-lint run --new-from-rev=origin/main ./...`
Expected: PASS, lint clean. The `unbounded` package's tests take the bypass port lock (`internal/testutil.LockBypassPort`) where their neighbours do; follow `rooms_test.go`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum vpn/vpn.go vpn/vpn_test.go unbounded/net.go unbounded/net_test.go unbounded/unbounded.go unbounded/rooms_test.go backend/radiance.go
git commit -m "unbounded: keep the widget's WebRTC sockets and lookups off the VPN tunnel

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01KqE58aYaai3u8CFZe5nXdE"
```

---

## After the tasks

- One `/create-pr` per repo, in order (broflake, lantern-box, radiance), each with the Codex gate and `--copilot-budget 1`.
- End-to-end check (spec, Testing): two macOS machines with these builds, both VPNs on, the consumer in manual mode on the personal proxy. The room pairing connects, and the consumer's `Local candidates` line shows `relay=` above zero.
