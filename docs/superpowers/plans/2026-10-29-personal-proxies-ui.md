# Personal Proxies UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move helper invites into the server list as "personal proxies": a selectable `personal-proxies` group in radiance, and the app's server list, VPN tab, Manage Private Locations and Action Mode reworked per Derek's design with the spec's deviations.

**Architecture:** radiance builds a url-test group tagged `personal-proxies` over every `helper-` outbound and offers it in the manual selector only while it has a member; the backend answers `GetServerByTag("personal-proxies")` with a stand-in server so the app's existing select/connect/fallback path works unchanged, and every redaction path treats the constant tag like a `helper-` tag. The app adds `ServerLocationType.personalProxy`, a Personal proxies section on the Private Locations tab, a VPN-tab banner, a third Manage tab that replaces GetHelp (with a local-only `rename_helper` op in lantern-core), and a permanent "Have an invite link?" row on Action Mode.

**Tech Stack:** Go (radiance, lantern-box v0.0.177 adapter/groups, sing-box-minimal), Flutter/Dart (Riverpod + riverpod_generator, auto_route, i18n_extension with gettext `.po`), lantern-core Go (helper engine).

**Spec:** `docs/superpowers/specs/2026-10-29-personal-proxies-ui-design.md` (binding). Background: `docs/superpowers/specs/2026-10-20-helper-invites-design.md`.

## Global Constraints

Spec decisions (outcomes, verbatim):

| Topic | Decision |
|---|---|
| Invite format | Links and QR codes, as shipped. The 43-character secret is never typed |
| Naming | "Personal proxy" for the person in a censored country; the volunteer keeps "helping" wording |
| Inviter entry points | Server list menu and a VPN tab banner; the Settings entry is removed |
| Selecting | One selectable "Your personal proxies" row; each friend listed under it for information only |
| Approval | #9229's approve sheet, after comparing the four-digit code |
| Managing | A third "Personal Proxies" tab in Manage Private Locations |
| Volunteer side | #9229's "You're helping" section, plus a permanent "Have an invite link?" row |
| Auto-enable Action Mode | Unchanged |

Exact UI strings (English, `assets/locales/en.po`): "Invite a Personal Proxy", "Manage Private Locations", "Private Locations", "Personal proxies" (server-list section header), "Your personal proxies" (selectable row), "Personal Proxies" (Manage tab), "Get a personal proxy" / "A friend abroad can keep you connected." (VPN-tab banner), "Personal proxy" (VPN tab Selected Location row), "Have an invite link?" (Action Mode row), "Rename", "Remove", "Cancel", "Invite another". Existing keys reused: `rename` ("Rename"), `remove` ("Remove"), `cancel` ("Cancel"). New keys are added to `assets/locales/en.po` only (other `.po` files are Transifex-managed), in the "Helper invites" block, with a `#` comment when a placeholder or context needs one.

Other rules every task inherits:

- The group tag is the constant `personal-proxies` (`vpn.PersonalProxiesTag` in radiance, `personalProxiesTag` in Dart). It is selectable by tag through the same path the app uses for a private server, and is never listed among servers (never in `AllServers`, never in the app's server lists).
- The group exists only while at least one helper outbound (`lbA.IsHelperTag`) does. It must never be started empty: lantern-box v0.0.177 `urlTestGroup.Start` returns before setting `pauseMgr` when it has no members, and a later `Add` then panics in `checkLoop` (`pause.RegisterTicker(nil, …)`). radiance therefore creates the group only with members and removes it from the outbound manager when its last member goes.
- Privacy: never export `helper-` tags or `personal-proxies`. Every radiance path that applies `RedactHelperTags`/`IsHelperTag` for export also maps `personal-proxies` to `"helper"`, and the issue-report archive filter treats it like a `helper-` tag. The app's Dart port (`lib/core/utils/helper_tags.dart`) stays in step.
- Stealth: nothing new is reachable when `AppBuildInfo.enableHelperInvites` is false. Inviter UI is gated by `helperInvitesAvailableProvider` (which includes the build const) or by the helpers view, which a stealth build never loads (`HelpersNotifier._buildEnabled`); volunteer UI by `AppBuildInfo.enableHelperInvites`.
- Never unlock the keystore to decide visibility: use `refreshIfUsed`, `viewLoaded` and the `helper_invites_used` mark, never `refresh` or `ensureUnlocked`, outside a user action.
- No lantern-cloud changes. No lantern-box change. No change to Action Mode auto-enable.
- The invite share message keeps its "help me stay online" wording (`invite_share_message` is unchanged).
- Use opus for every implementer and reviewer subagent.
- Every PR goes through `/create-pr` (the skills plugin's Codex-gated flow); never bare `gh pr create`.
- Dependency bumps: `go get …@<sha>` then `go mod tidy`; commit `go.mod` and `go.sum` together in the bump commit.
- Flutter tests need an empty `app.env` at the app repo root (gitignored): run `touch app.env` once before `flutter test`.
- After changing any `@riverpod`/`@RoutePage` annotation run `make gen` (`dart run build_runner build --delete-conflicting-outputs`) and commit the regenerated `*.g.dart` / `lib/core/router/router.gr.dart`.
- radiance Go tests: `go test -race -tags with_clash_api <pkgs>`. App Go tests: `CGO_ENABLED=1 go test -race ./lantern-core/helper/...` from the app repo root.

Repos and branches:

- radiance: `/Users/afisk/go/src/github.com/getlantern/radiance-personal-proxies` (branch `fisk/personal-proxies` off `origin/main`). Tasks 1–3.
- app: `/Users/afisk/go/src/github.com/getlantern/lantern-helper-app` (branch `fisk/personal-proxies` off `origin/main`, contains #9229). Tasks 4–11.

## File Structure

radiance:

- `internal/redact/redact.go` — owns `PersonalProxiesTag` and `Tag()`; `Helper`/`NamesHelper`/`HelperBytes` cover the constant.
- `log/redact.go`, `telemetry/connections.go`, `issue/archive.go` — callers switch to the shared helpers.
- `vpn/boxoptions.go` — `PersonalProxiesTag`, `helperTags`, `personalProxiesOutbound`; `buildOptions` adds the group when helpers exist.
- `vpn/tunnel.go` — `syncPersonalProxiesLocked` keeps the group and the selector in step on every add/remove/update; `selectOutbound` refuses the tag when the group is absent.
- `vpn/personal_proxies_test.go` (new) — live-tunnel tests.
- `backend/radiance.go` — `serverByTag` / `personalProxiesServer` stand-in used by every selection path.
- `cmd/lantern/monitor.go` — `isGroupTag` includes the group.

app:

- `lantern-core/helper/{state,ops,engine}.go` — `Nickname` on pairings and the `rename_helper` op.
- `lib/core/common/app_eum.dart`, `lib/core/models/server_location.dart`, `lib/core/utils/helper_tags.dart` — the new location type, its `ServerLocation`, the Dart tag and redaction.
- `lib/features/helpers/personal_proxy_invite.dart` (new) — `startPersonalProxyInvite`, shared by menu, banner and Manage tab.
- `lib/features/vpn/personal_proxies_section.dart` (new) — the server-list section.
- `lib/features/helpers/personal_proxies_tab.dart` (new) — the Manage tab (replaces `get_help.dart`, deleted).
- `lib/features/helpers/personal_proxy_banner.dart` (new) — the VPN-tab banner.
- `lib/features/helpers/provider/helpers_notifier.dart` — `renameHelper`, `personalProxiesTabVisibleProvider` (renamed from `helperEntryVisibleProvider`), `personalProxyBannerVisibleProvider`.

---

### Task 1: radiance — redact `personal-proxies` wherever helper tags are redacted

**Files:**
- Modify: `internal/redact/redact.go:16-71` (add const and `Tag`, widen `NamesHelper`, `Helper`, `HelperBytes`)
- Modify: `log/redact.go:10-35` (fast path)
- Modify: `telemetry/connections.go:13,171-187` (use `redact.Tag`), `telemetry/otel.go:303-306` (comment)
- Modify: `issue/archive.go:122-149` (`stubHelpers` uses `isHelperTag`)
- Test: `internal/redact/redact_test.go`, `log/redact_test.go`, `telemetry/redact_test.go`, `issue/redact_test.go`

**Interfaces:**
- Produces: `redact.PersonalProxiesTag = "personal-proxies"` (const); `func redact.Tag(s string) string` (helper tags and the group tag → `"helper"`, no address redaction); `redact.NamesHelper(s)` is true for any string containing `personal-proxies`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/redact/redact_test.go`:

```go
func TestHelperRedactsThePersonalProxiesTag(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"group logger", "outbound/mutableurltest[personal-proxies]: dial tcp 203.0.113.7:443: refused",
			"outbound/mutableurltest[helper]: dial tcp <redacted>: refused"},
		{"selected outbound", `selected outbound="personal-proxies"`, `selected outbound="helper"`},
		{"urltest attribute", "urltest_group=personal-proxies", "urltest_group=helper"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Helper(tc.in))
			assert.Equal(t, []byte(tc.want), HelperBytes([]byte(tc.in)))
		})
	}
	assert.True(t, NamesHelper(PersonalProxiesTag))
}

func TestTagRedactsHelperAndGroupTags(t *testing.T) {
	assert.Equal(t, "helper", Tag(PersonalProxiesTag))
	assert.Equal(t, "helper", Tag(tag))
	assert.Equal(t, "final => helper", Tag("final => personal-proxies"))
	assert.Equal(t, "lantern-1", Tag("lantern-1"))
	assert.Equal(t, "dial 203.0.113.7", Tag("dial 203.0.113.7"), "Tag leaves addresses alone")
}
```

Append to `log/redact_test.go`:

```go
func TestRedactingWriterRedactsThePersonalProxiesTag(t *testing.T) {
	var buf bytes.Buffer
	w := newRedactingWriter(&buf)
	in := []byte("outbound/mutableurltest[personal-proxies]: dial tcp 203.0.113.7:443\n")
	n, err := w.Write(in)
	require.NoError(t, err)
	assert.Equal(t, len(in), n)
	assert.Equal(t, "outbound/mutableurltest[helper]: dial tcp <redacted>\n", buf.String())
}
```

Append to `telemetry/redact_test.go`:

```go
func TestConnMetricAttributesRedactThePersonalProxiesTag(t *testing.T) {
	set := newConnAttributeSet(vpn.ConnAttrs{
		FromOutbound: "personal-proxies",
		Outbound:     helperTag,
		Rule:         "final => personal-proxies",
		ChainList:    []string{"manual", "personal-proxies", helperTag},
	})
	for _, kv := range set.ToSlice() {
		assert.NotContains(t, kv.Value.Emit(), "personal-proxies", string(kv.Key))
		assert.NotContains(t, kv.Value.Emit(), helperTag, string(kv.Key))
	}
	chain, _ := set.Value("chain_list")
	assert.Equal(t, []string{"manual", "helper", "helper"}, chain.AsStringSlice())
}

func TestSpanExportRedactsThePersonalProxiesTag(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(newRedactingExporter(mem)))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "url test")
	span.SetAttributes(attribute.String("tag", "personal-proxies"), attribute.String("peer", "198.51.100.9:443"))
	span.SetStatus(codes.Error, "outbound/mutableurltest[personal-proxies]: dial tcp 203.0.113.7:443")
	span.End()

	s := mem.GetSpans()[0]
	for _, kv := range s.Attributes {
		assert.NotContains(t, kv.Value.Emit(), "personal-proxies", string(kv.Key))
		assert.NotContains(t, kv.Value.Emit(), "198.51.100.9", string(kv.Key), "a span naming the group loses every address")
	}
	assert.Equal(t, "outbound/mutableurltest[helper]: dial tcp <redacted>", s.Status.Description)
}
```

Append to `issue/redact_test.go`:

```go
// The group's tag marks a device as having personal proxies, so issue reports
// treat it like a helper- tag: in logs, in debug-box-options.json and in the
// persisted selection.
func TestIssueArchiveRedactsThePersonalProxiesTag(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lantern.log"),
		[]byte("outbound/mutableurltest[personal-proxies]: dial tcp 203.0.113.7:443\n"), 0644))
	opts := filepath.Join(dir, "debug-box-options.json")
	require.NoError(t, os.WriteFile(opts, []byte(`{"outbounds":[
 {"type":"mutableurltest","tag":"personal-proxies","outbounds":["helper-0123456789ab"]},
 {"type":"mutableselector","tag":"manual","outbounds":["lantern-1","personal-proxies"]}]}`), 0644))
	settingsFile := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(settingsFile,
		[]byte(`{"selected_server":{"tag":"personal-proxies","type":"mutableurltest","isLantern":false}}`), 0644))

	result, err := buildIssueArchive(log.NoOpLogger(), dir, []string{opts, settingsFile}, 1024*1024)
	require.NoError(t, err)
	entries := readZipEntries(t, result)
	require.Len(t, entries, 3)
	for _, e := range entries {
		assert.NotContains(t, e.content, "personal-proxies", e.name)
		assert.NotContains(t, e.content, "helper-0123456789ab", e.name)
		assert.NotContains(t, e.content, "203.0.113.7", e.name)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race -tags with_clash_api ./internal/redact/ ./log/ ./telemetry/ ./issue/ -run 'PersonalProxies|TagRedacts'`
Expected: FAIL to compile (`undefined: PersonalProxiesTag`, `undefined: Tag`).

- [ ] **Step 3: Implement**

In `internal/redact/redact.go`, after `const Redacted`:

```go
// PersonalProxiesTag is the tag of radiance's url-test group over a paired
// inviter's helper outbounds (vpn.PersonalProxiesTag). It marks a device as
// having personal proxies wherever outbound tags are recorded, so it is
// exported only as "helper", like a helper tag.
const PersonalProxiesTag = "personal-proxies"

// Tag returns s with every helper tag and PersonalProxiesTag replaced by
// "helper". Unlike Helper it leaves addresses alone, for values that are only
// outbound tags or rules, such as metric attributes.
func Tag(s string) string {
	return strings.ReplaceAll(lbA.RedactHelperTags(s), PersonalProxiesTag, "helper")
}
```

Replace the last line of `Helper` (`return lbA.RedactHelperTags(strings.Join(lines, ""))`) with:

```go
	return Tag(strings.Join(lines, ""))
```

Replace `NamesHelper`:

```go
// NamesHelper reports whether s may name a helper outbound: by its tag, by
// the group over them (PersonalProxiesTag), or by the "helper" a redaction
// left in its place.
func NamesHelper(s string) bool {
	return strings.Contains(s, PersonalProxiesTag) ||
		(strings.Contains(s, "helper") && helperNamed.MatchString(s))
}
```

Replace the guard in `HelperBytes`:

```go
	if !bytes.Contains(b, []byte("helper")) && !bytes.Contains(b, []byte(PersonalProxiesTag)) {
		return b
	}
```

In `log/redact.go`, add `var personalProxiesWord = []byte(redact.PersonalProxiesTag)` beside `helperWord`, and change the fast path in `Write` to:

```go
	if !bytes.Contains(p, helperWord) && !bytes.Contains(p, personalProxiesWord) {
		return r.w.Write(p)
	}
```

In `telemetry/connections.go`, replace the four `lbA.RedactHelperTags(` calls in `newConnAttributeSet` with `redact.Tag(`, update its comment to "helper- outbound tags and the personal-proxies group are client-side pseudonyms that must not be exported; they are all counted as "helper".", drop the now-unused `lbA` import and add `"github.com/getlantern/radiance/internal/redact"`. In `telemetry/otel.go:305` change "must go through lbA.RedactHelperTags, since helper-" to "must go through redact.Tag, since helper-".

In `issue/archive.go`, add below `helperStub`:

```go
// isHelperTag reports whether tag names a helper outbound or the group over
// them; both mark the device as having personal proxies.
func isHelperTag(tag string) bool {
	return lbA.IsHelperTag(tag) || tag == redact.PersonalProxiesTag
}
```

and replace the three `lbA.IsHelperTag(` calls in `stubHelpers` (lines 126, 131, 144) with `isHelperTag(`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race -tags with_clash_api ./internal/redact/ ./log/ ./telemetry/ ./issue/`
Expected: PASS (all existing redaction tests too).

- [ ] **Step 5: Commit**

```bash
git add internal/redact log telemetry issue
git commit -m "redact: treat the personal-proxies group tag like a helper tag"
```

---

### Task 2: radiance — the `personal-proxies` group in the tunnel

**Files:**
- Modify: `vpn/boxoptions.go:35-51` (const, reserved tags), `vpn/boxoptions.go:391-475` (`buildOptions`), add helpers after `selectorOutbound` (`:575-583`)
- Modify: `vpn/tunnel.go:321-338` (`newMutableGroupManager`), `:497-509` (`selectOutbound`), `:592-596` (`addOutbounds`), `:762-800` (`removeOutbounds*`), `:802-860` (`updateOutbounds`); add `syncPersonalProxiesLocked`, `personalProxiesGroup`, `offersPersonalProxies`, `ErrPersonalProxiesUnavailable`
- Modify: `cmd/lantern/monitor.go:646-651` (`isGroupTag`)
- Test: `vpn/boxoptions_test.go`, `vpn/personal_proxies_test.go` (new)

**Interfaces:**
- Consumes: `redact.PersonalProxiesTag` (Task 1).
- Produces: `vpn.PersonalProxiesTag` (= `redact.PersonalProxiesTag`); `vpn.ErrPersonalProxiesUnavailable`; `func helperTags(tags []string) []string`; `func personalProxiesOutbound(helpers []string) O.Outbound`; `(*tunnel).syncPersonalProxiesLocked() error`. `VPNClient.SelectServer(vpn.PersonalProxiesTag)` returns an error wrapping `ErrPersonalProxiesUnavailable` when the group does not exist.

- [ ] **Step 1: Write the failing buildOptions tests**

Append to `vpn/boxoptions_test.go`:

```go
func helperOutbound(tag string) O.Outbound {
	return O.Outbound{Type: C.TypeDirect, Tag: tag, Options: &O.DirectOutboundOptions{}}
}

func TestBuildOptions_PersonalProxies(t *testing.T) {
	const h1, h2 = "helper-0123456789ab", "helper-ba9876543210"
	withHelpers := func(t *testing.T) O.Options {
		cfg := testConfig(t)
		cfg.Options.Outbounds = append(cfg.Options.Outbounds, helperOutbound(h1), helperOutbound(h2))
		return cfg.Options
	}

	t.Run("groups every helper outbound and is offered by the manual selector", func(t *testing.T) {
		opts, err := buildOptions(BoxOptions{BasePath: t.TempDir(), Options: withHelpers(t)})
		require.NoError(t, err)
		group := findOutbound(opts.Outbounds, PersonalProxiesTag)
		require.NotNil(t, group)
		assert.Equal(t, lbC.TypeMutableURLTest, group.Type)
		assert.Equal(t, []string{h1, h2}, group.Options.(*lbO.MutableURLTestOutboundOptions).Outbounds)
		manual := findOutbound(opts.Outbounds, ManualSelectTag).Options.(*lbO.MutableSelectorOutboundOptions)
		assert.Contains(t, manual.Outbounds, PersonalProxiesTag)
		auto := findOutbound(opts.Outbounds, AutoSelectTag).Options.(*lbO.MutableAutoSelectOutboundOptions)
		assert.NotContains(t, auto.Outbounds, PersonalProxiesTag, "Smart Location uses the helpers directly")
		assert.Contains(t, auto.Outbounds, h1)
	})

	t.Run("does not exist without helper outbounds", func(t *testing.T) {
		options, _ := testBoxOptions(t)
		opts, err := buildOptions(BoxOptions{BasePath: t.TempDir(), Options: options})
		require.NoError(t, err)
		assert.Nil(t, findOutbound(opts.Outbounds, PersonalProxiesTag), "a url-test group must never start empty")
		manual := findOutbound(opts.Outbounds, ManualSelectTag).Options.(*lbO.MutableSelectorOutboundOptions)
		assert.NotContains(t, manual.Outbounds, PersonalProxiesTag)
	})

	t.Run("can be the initial selection while helpers exist", func(t *testing.T) {
		opts, err := buildOptions(BoxOptions{BasePath: t.TempDir(), Options: withHelpers(t), InitialServer: PersonalProxiesTag})
		require.NoError(t, err)
		manual := findOutbound(opts.Outbounds, ManualSelectTag).Options.(*lbO.MutableSelectorOutboundOptions)
		assert.Equal(t, PersonalProxiesTag, manual.Outbounds[0])
		assert.Equal(t, ManualSelectTag, opts.Experimental.ClashAPI.DefaultMode)

		options, _ := testBoxOptions(t)
		_, err = buildOptions(BoxOptions{BasePath: t.TempDir(), Options: options, InitialServer: PersonalProxiesTag})
		require.Error(t, err)
	})

	t.Run("is reserved", func(t *testing.T) {
		assert.Contains(t, ReservedTags(), PersonalProxiesTag)
	})
}
```

- [ ] **Step 2: Write the failing live-tunnel tests**

Create `vpn/personal_proxies_test.go`:

```go
package vpn

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	O "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	lbA "github.com/getlantern/lantern-box/adapter"
	lbC "github.com/getlantern/lantern-box/constant"
	lbO "github.com/getlantern/lantern-box/option"

	"github.com/getlantern/radiance/servers"
)

const (
	testHelper1 = "helper-0123456789ab"
	testHelper2 = "helper-ba9876543210"
)

func directServer(tag string) *servers.Server {
	return &servers.Server{Tag: tag, Type: C.TypeDirect, IsLantern: true,
		Options: O.Outbound{Tag: tag, Type: C.TypeDirect, Options: &O.DirectOutboundOptions{}}}
}

// startPersonalProxiesTestTunnel starts a tunnel with lantern-1 and helpers,
// built as buildOptions builds it: the group exists only when helpers do.
func startPersonalProxiesTestTunnel(t *testing.T, helpers ...string) *tunnel {
	t.Helper()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(probe.Close)
	members := append([]string{"lantern-1"}, helpers...)
	outbounds := []O.Outbound{{Type: C.TypeDirect, Tag: "lantern-1", Options: &O.DirectOutboundOptions{}}}
	for _, h := range helpers {
		outbounds = append(outbounds, O.Outbound{Type: C.TypeDirect, Tag: h, Options: &O.DirectOutboundOptions{}})
	}
	selectable := slices.Clone(members)
	if len(helpers) > 0 {
		outbounds = append(outbounds, O.Outbound{Type: lbC.TypeMutableURLTest, Tag: PersonalProxiesTag,
			Options: &lbO.MutableURLTestOutboundOptions{Outbounds: helpers, URL: probe.URL}})
		selectable = append(selectable, PersonalProxiesTag)
	}
	outbounds = append(outbounds,
		O.Outbound{Type: lbC.TypeMutableSelector, Tag: ManualSelectTag,
			Options: &lbO.MutableSelectorOutboundOptions{Outbounds: selectable}},
		O.Outbound{Type: lbC.TypeMutableAutoSelect, Tag: AutoSelectTag,
			Options: &lbO.MutableAutoSelectOutboundOptions{Outbounds: members, URL: probe.URL}},
	)
	options := O.Options{
		Log:       &O.LogOptions{Disabled: true},
		Outbounds: outbounds,
		Route:     &O.RouteOptions{Rules: []O.Rule{selectModeRule(AutoSelectTag), selectModeRule(ManualSelectTag)}},
		Experimental: &O.ExperimentalOptions{
			ClashAPI:  &O.ClashAPIOptions{ModeList: []string{ManualSelectTag, AutoSelectTag}, DefaultMode: AutoSelectTag},
			CacheFile: &O.CacheFileOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache.db")},
		},
	}
	tun := &tunnel{dataPath: t.TempDir()}
	t.Cleanup(func() { assert.NoError(t, tun.close()) })
	require.NoError(t, tun.init(t.Context(), options, nil))
	require.NoError(t, tun.boxInstance.Start())
	tun.optsMap = makeOutboundOptsMap(tun.ctx, options)
	tun.nonSelectableTags = makeNonSelectableTagSet(nil, options)
	tun.outboundMgr = service.FromContext[adapter.OutboundManager](tun.ctx)
	clash := service.FromContext[adapter.ClashServer](tun.ctx).(*clashServer)
	tun.clashServer = clash
	mutGrpMgr, err := newMutableGroupManager(tun.ctx, tun.logFactory.NewLogger("groupsManager"), clash.connTracker)
	require.NoError(t, err)
	tun.mutGrpMgr = mutGrpMgr
	t.Cleanup(mutGrpMgr.Close)
	return tun
}

func selectorMembers(t *testing.T, tun *tunnel, tag string) []string {
	t.Helper()
	g, ok := tun.mutGrpMgr.OutboundGroup(tag)
	require.True(t, ok, tag)
	return slices.Clone(g.All())
}

// personalProxiesMembers returns the group's members, or nil when the group
// does not exist.
func personalProxiesMembers(tun *tunnel) []string {
	g := tun.personalProxiesGroup()
	if g == nil {
		return nil
	}
	return slices.Clone(g.All())
}

func TestPersonalProxiesGroupFollowsHelperOutbounds(t *testing.T) {
	tun := startPersonalProxiesTestTunnel(t)
	assert.Nil(t, tun.personalProxiesGroup(), "no helpers, no group")
	assert.NotContains(t, selectorMembers(t, tun, ManualSelectTag), PersonalProxiesTag)
	require.ErrorIs(t, tun.selectOutbound(PersonalProxiesTag), ErrPersonalProxiesUnavailable)

	require.NoError(t, tun.addOutbounds(servers.ServerList{Servers: []*servers.Server{
		directServer(testHelper1), directServer(testHelper2), directServer("lantern-2"),
	}}))
	assert.ElementsMatch(t, []string{testHelper1, testHelper2}, personalProxiesMembers(tun))
	for _, tag := range personalProxiesMembers(tun) {
		assert.True(t, lbA.IsHelperTag(tag), "only helper outbounds: %s", tag)
	}
	assert.Contains(t, selectorMembers(t, tun, ManualSelectTag), PersonalProxiesTag)
	assert.NotContains(t, selectorMembers(t, tun, AutoSelectTag), PersonalProxiesTag)
	assert.Contains(t, selectorMembers(t, tun, AutoSelectTag), testHelper1, "Smart Location keeps the helpers")

	require.NoError(t, tun.selectOutbound(PersonalProxiesTag))
	sel, _ := tun.mutGrpMgr.OutboundGroup(ManualSelectTag)
	assert.Equal(t, PersonalProxiesTag, sel.Now())

	// A config without testHelper1 keeps the group and the selection.
	require.NoError(t, tun.updateOutbounds(servers.ServerList{Servers: []*servers.Server{
		directServer("lantern-1"), directServer(testHelper2),
	}}))
	assert.Equal(t, []string{testHelper2}, personalProxiesMembers(tun))
	assert.Equal(t, PersonalProxiesTag, sel.Now(), "an update must not withdraw the selected group")

	// The last helper goes: so do the group and its selector entry.
	require.NoError(t, tun.updateOutbounds(servers.ServerList{Servers: []*servers.Server{directServer("lantern-1")}}))
	assert.Nil(t, tun.personalProxiesGroup())
	assert.NotContains(t, selectorMembers(t, tun, ManualSelectTag), PersonalProxiesTag)
	require.ErrorIs(t, tun.selectOutbound(PersonalProxiesTag), ErrPersonalProxiesUnavailable)

	// And it comes back, never having started empty.
	require.NoError(t, tun.addOutbounds(servers.ServerList{Servers: []*servers.Server{directServer(testHelper1)}}))
	assert.Equal(t, []string{testHelper1}, personalProxiesMembers(tun))
	assert.Contains(t, selectorMembers(t, tun, ManualSelectTag), PersonalProxiesTag)
}

func TestPersonalProxiesGroupBuiltAtStartIsManagedLikeARuntimeOne(t *testing.T) {
	tun := startPersonalProxiesTestTunnel(t, testHelper1)
	_, registered := tun.mutGrpMgr.OutboundGroup(PersonalProxiesTag)
	assert.False(t, registered, "the group is the tunnel's, not the group manager's")
	require.NoError(t, tun.selectOutbound(PersonalProxiesTag))

	require.NoError(t, tun.removeOutbounds([]string{testHelper1}))
	assert.Nil(t, tun.personalProxiesGroup())
	assert.NotContains(t, selectorMembers(t, tun, ManualSelectTag), PersonalProxiesTag)
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -race -tags with_clash_api ./vpn/ -run 'PersonalProxies'`
Expected: FAIL to compile (`undefined: PersonalProxiesTag`, `personalProxiesGroup`, `ErrPersonalProxiesUnavailable`).

- [ ] **Step 4: Implement in `vpn/boxoptions.go`**

Add to the const block after `ManualSelectTag` (import `"github.com/getlantern/radiance/internal/redact"` and `"github.com/sagernet/sing/common/json/badoption"`; `lbA` is already imported):

```go
	// PersonalProxiesTag is the url-test group over every helper outbound
	// (lbA.IsHelperTag). The tunnel creates it with its first member and
	// removes it with its last, and the manual selector offers it while it
	// exists. It is never a managed server, so it is never listed.
	PersonalProxiesTag = redact.PersonalProxiesTag
```

Change `reservedTags` to:

```go
var reservedTags = []string{AutoSelectTag, ManualSelectTag, PersonalProxiesTag, "direct", "block"}
```

In `buildOptions`, replace from `tags := mergeAndCollectTags(...)` through the `selectorOutbound` append with:

```go
	tags := mergeAndCollectTags(&opts, &bOptions.Options, bOptions.NonSelectableOutbounds)
	helpers := helperTags(tags)
	// The manual selector also offers the personal proxies group, which
	// exists only while a helper outbound does: a url-test group must never
	// start empty.
	selectable := tags
	if len(helpers) > 0 {
		selectable = append(slices.Clone(tags), PersonalProxiesTag)
	}

	initial := bOptions.InitialServer
	if initial == "" || initial == AutoSelectTag {
		opts.Experimental.ClashAPI.DefaultMode = AutoSelectTag
	} else {
		// The manual selector defaults to its first tag, so place initial at index 0.
		i := slices.Index(selectable, initial)
		if i == -1 {
			return O.Options{}, fmt.Errorf("initial server tag %q not found in outbounds or endpoints", initial)
		}
		selectable[0], selectable[i] = selectable[i], selectable[0]
		opts.Experimental.ClashAPI.DefaultMode = ManualSelectTag
	}

	if tunHasIPv6(opts) {
		opts.Route.Rules = append(opts.Route.Rules, rejectIPv6Rule())
	}

	// add mode selector outbounds and rules
	opts.Outbounds = append(opts.Outbounds, urlTestOutbound(AutoSelectTag, tags, bOptions.BanditURLOverrides))
	if len(helpers) > 0 {
		opts.Outbounds = append(opts.Outbounds, personalProxiesOutbound(helpers))
	}
	opts.Outbounds = append(opts.Outbounds, selectorOutbound(ManualSelectTag, selectable))
```

Add after `selectorOutbound`:

```go
// helperTags returns the tags in tags that are helper outbounds.
func helperTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		if lbA.IsHelperTag(tag) {
			out = append(out, tag)
		}
	}
	return out
}

// personalProxiesOutbound is the url-test group over helpers, which must not
// be empty (see PersonalProxiesTag).
func personalProxiesOutbound(helpers []string) O.Outbound {
	return O.Outbound{
		Type: lbC.TypeMutableURLTest,
		Tag:  PersonalProxiesTag,
		Options: &lbO.MutableURLTestOutboundOptions{
			Outbounds:   helpers,
			URL:         "https://google.com/generate_204",
			Interval:    badoption.Duration(urlTestInterval),
			IdleTimeout: badoption.Duration(urlTestIdleTimeout),
		},
	}
}
```

- [ ] **Step 5: Implement in `vpn/tunnel.go`**

In `newMutableGroupManager`, keep the group out of the group manager (its map is fixed at construction, and the tunnel removes and recreates the group):

```go
	for _, out := range oMgr.Outbounds() {
		if g, isMutGroup := out.(lbA.MutableOutboundGroup); isMutGroup && g.Tag() != PersonalProxiesTag {
			mutGroups = append(mutGroups, g)
		}
	}
```

Replace `selectOutbound`:

```go
// ErrPersonalProxiesUnavailable is returned for a selection of
// PersonalProxiesTag while the device has no helper outbound.
var ErrPersonalProxiesUnavailable = errors.New("personal proxies unavailable")

func (t *tunnel) selectOutbound(tag string) error {
	// Checked before the mode switch, so a refused selection changes nothing.
	if tag == PersonalProxiesTag && !t.offersPersonalProxies() {
		return ErrPersonalProxiesUnavailable
	}
	if err := t.selectMode(ManualSelectTag); err != nil {
		return err
	}

	outboundMgr := service.FromContext[adapter.OutboundManager](t.ctx)
	outbound, loaded := outboundMgr.Outbound(ManualSelectTag)
	if !loaded {
		return fmt.Errorf("manual select group not found")
	}
	outbound.(Selector).SelectOutbound(tag)
	return nil
}

// offersPersonalProxies reports whether the manual selector offers the
// personal proxies group, which it does exactly while the group exists.
func (t *tunnel) offersPersonalProxies() bool {
	if t.mutGrpMgr == nil {
		return false
	}
	selector, ok := t.mutGrpMgr.OutboundGroup(ManualSelectTag)
	return ok && slices.Contains(selector.All(), PersonalProxiesTag)
}

// personalProxiesGroup returns the personal proxies group, or nil while it
// does not exist.
func (t *tunnel) personalProxiesGroup() lbA.MutableOutboundGroup {
	if t.outboundMgr == nil {
		return nil
	}
	out, ok := t.outboundMgr.Outbound(PersonalProxiesTag)
	if !ok {
		return nil
	}
	g, _ := out.(lbA.MutableOutboundGroup)
	return g
}

// syncPersonalProxiesLocked makes the personal proxies group hold exactly the
// helper outbounds the manual selector offers. It creates the group with its
// first member and removes it with its last (a url-test group must never be
// empty), and keeps the selector offering it exactly while it exists. The
// caller must hold t.outboundMu.
func (t *tunnel) syncPersonalProxiesLocked() error {
	if t.mutGrpMgr == nil || t.outboundMgr == nil {
		return nil
	}
	selector, ok := t.mutGrpMgr.OutboundGroup(ManualSelectTag)
	if !ok {
		return errors.New("manual select group not found")
	}
	helpers := helperTags(selector.All())
	group := t.personalProxiesGroup()
	switch {
	case len(helpers) == 0 && group == nil:
		return nil
	case len(helpers) == 0:
		// Withdrawn before it closes, so the selector never holds a closed group.
		var errs []error
		if _, err := selector.Remove(PersonalProxiesTag); err != nil {
			errs = append(errs, err)
		}
		if err := t.outboundMgr.Remove(PersonalProxiesTag); err != nil {
			errs = append(errs, fmt.Errorf("remove personal proxies group: %w", err))
		}
		return errors.Join(errs...)
	case group == nil:
		out := personalProxiesOutbound(helpers)
		router := service.FromContext[adapter.Router](t.ctx)
		logger := t.logFactory.NewLogger("outbound/" + out.Tag + "[" + out.Type + "]")
		if err := t.outboundMgr.Create(t.ctx, router, logger, out.Tag, out.Type, out.Options); err != nil {
			return fmt.Errorf("create personal proxies group: %w", err)
		}
	default:
		var stale []string
		for _, tag := range group.All() {
			if !slices.Contains(helpers, tag) {
				stale = append(stale, tag)
			}
		}
		if len(stale) > 0 {
			if _, err := group.Remove(stale...); err != nil {
				return fmt.Errorf("update personal proxies group: %w", err)
			}
		}
		if _, err := group.Add(helpers...); err != nil {
			return fmt.Errorf("update personal proxies group: %w", err)
		}
	}
	if !slices.Contains(selector.All(), PersonalProxiesTag) {
		if _, err := selector.Add(PersonalProxiesTag); err != nil {
			return fmt.Errorf("offer personal proxies group: %w", err)
		}
	}
	return nil
}
```

Change `addOutbounds` and `removeOutbounds` to sync after the locked work:

```go
func (t *tunnel) addOutbounds(list servers.ServerList) error {
	t.outboundMu.Lock()
	defer t.outboundMu.Unlock()
	err := t.addOutboundsLocked(list)
	if errors.Is(err, errLibboxClosed) {
		return err
	}
	return errors.Join(err, t.syncPersonalProxiesLocked())
}

func (t *tunnel) removeOutbounds(tags []string) error {
	t.outboundMu.Lock()
	defer t.outboundMu.Unlock()
	err := t.removeOutboundsLocked(tags)
	if errors.Is(err, errLibboxClosed) {
		return err
	}
	return errors.Join(err, t.syncPersonalProxiesLocked())
}
```

In `removeOutboundsLocked`, at the top of the `for _, tag := range tags` loop add:

```go
		if tag == PersonalProxiesTag {
			continue // the tunnel's own group: syncPersonalProxiesLocked manages it
		}
```

In `updateOutbounds`, replace the final `return errors.Join(errs...)` with:

```go
	if err := t.syncPersonalProxiesLocked(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
```

In `cmd/lantern/monitor.go`, `isGroupTag` becomes:

```go
	return tag == vpn.AutoSelectTag || tag == vpn.ManualSelectTag || tag == vpn.PersonalProxiesTag
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race -tags with_clash_api ./vpn/ ./cmd/... -run 'PersonalProxies|BuildOptions|TunnelClientInfo|LiveTunnel'`
Expected: PASS. Then the full packages: `go test -race -tags with_clash_api ./vpn/ ./cmd/...` — PASS.

If `outboundMgr.Create` for the group fails because a member is not yet in the outbound manager, the add order in `addOutboundsLocked` is wrong: sync must run after `addOutboundsLocked` returns, as written above.

- [ ] **Step 7: Commit**

```bash
git add vpn cmd/lantern/monitor.go
git commit -m "vpn: personal-proxies url-test group over helper outbounds"
```

---

### Task 3: radiance — select, persist and connect by `personal-proxies`

**Files:**
- Modify: `backend/radiance.go:956-958` (`GetServerByTag`), `:1160-1173` (`clearSelectedIfMissing`), `:1334-1361` (`ConnectVPN`), `:1399-1408` (`connectable`), `:1564-1590` (`persistSelection`), `:1601-1631` (`SelectedServer`); add `serverByTag`, `personalProxiesServer`
- Test: `backend/radiance_test.go`

**Interfaces:**
- Consumes: `vpn.PersonalProxiesTag`, `vpn.ErrPersonalProxiesUnavailable` (Task 2).
- Produces: `LocalBackend.GetServerByTag(vpn.PersonalProxiesTag)` returns `&servers.Server{Tag: "personal-proxies", Type: "mutableurltest", IsLantern: false}` while the manager holds any server whose tag satisfies `lbA.IsHelperTag`, else not found. `AllServers` never includes it. IPC `GetServerByTag`/`SelectServer`/`ConnectVPN`/`SelectedServer` inherit this (no IPC change).

- [ ] **Step 1: Write the failing tests**

Append to `backend/radiance_test.go` (imports `settings`, `servers`, `option`, `log`, `config`, `vpn` are already present in this file):

```go
func newPersonalProxiesBackend(t *testing.T) *LocalBackend {
	t.Helper()
	dataDir := t.TempDir()
	settings.Reset()
	t.Cleanup(settings.Reset)
	require.NoError(t, settings.InitSettings(dataDir))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srvMgr, err := servers.NewManager(dataDir, log.NoOpLogger())
	require.NoError(t, err)
	return &LocalBackend{
		ctx:         ctx,
		confHandler: config.NewConfigHandler(ctx, config.Options{DataPath: dataDir, Logger: log.NoOpLogger()}),
		srvManager:  srvMgr,
		vpnClient:   vpn.NewVPNClient(dataDir, log.NoOpLogger(), nil),
	}
}

func addDirectServer(t *testing.T, r *LocalBackend, tag string) {
	t.Helper()
	require.NoError(t, r.srvManager.AddServers(servers.ServerList{Servers: []*servers.Server{{
		Tag: tag, Type: "direct", IsLantern: true,
		Options: option.Outbound{Tag: tag, Type: "direct", Options: &option.DirectOutboundOptions{}},
	}}}, false))
}

func TestPersonalProxiesServerExistsWhileAHelperDoes(t *testing.T) {
	r := newPersonalProxiesBackend(t)
	addDirectServer(t, r, "lantern-1")
	_, found := r.GetServerByTag(vpn.PersonalProxiesTag)
	assert.False(t, found, "no helper outbound, no group")
	assert.False(t, r.connectable(vpn.PersonalProxiesTag))

	addDirectServer(t, r, "helper-0123456789ab")
	srv, found := r.GetServerByTag(vpn.PersonalProxiesTag)
	require.True(t, found)
	assert.Equal(t, vpn.PersonalProxiesTag, srv.Tag)
	assert.False(t, srv.IsLantern)
	assert.Nil(t, srv.Options)
	assert.True(t, r.connectable(vpn.PersonalProxiesTag))
	for _, s := range r.AllServers() {
		assert.NotEqual(t, vpn.PersonalProxiesTag, s.Tag, "never listed among servers")
	}
}

func TestPersonalProxiesSelectionLastsWhileAHelperDoes(t *testing.T) {
	r := newPersonalProxiesBackend(t)
	addDirectServer(t, r, "helper-0123456789ab")
	r.persistSelection(vpn.PersonalProxiesTag)
	assert.False(t, settings.GetBool(settings.AutoConnectKey))

	sel, exists, err := r.SelectedServer()
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, vpn.PersonalProxiesTag, sel.Tag)

	r.clearSelectedIfMissing()
	assert.False(t, settings.GetBool(settings.AutoConnectKey), "kept while a helper remains")

	_, err = r.srvManager.RemoveServers([]string{"helper-0123456789ab"})
	require.NoError(t, err)
	r.clearSelectedIfMissing()
	assert.True(t, settings.GetBool(settings.AutoConnectKey), "falls back to Smart Location")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race -tags with_clash_api ./backend/ -run PersonalProxies`
Expected: FAIL (`found` false after adding the helper; `persistSelection` logs "no server found for tag").

- [ ] **Step 3: Implement**

In `backend/radiance.go` (add import `lbC "github.com/getlantern/lantern-box/constant"` beside `lbA`), replace `GetServerByTag` and add the helpers below it:

```go
func (r *LocalBackend) GetServerByTag(tag string) (*servers.Server, bool) {
	return r.serverByTag(tag)
}

// serverByTag is the manager's lookup plus the personal proxies group, which
// is selectable by tag but never a managed server, so never listed.
func (r *LocalBackend) serverByTag(tag string) (*servers.Server, bool) {
	if tag == vpn.PersonalProxiesTag {
		return r.personalProxiesServer()
	}
	return r.srvManager.GetServerByTag(tag)
}

// personalProxiesServer stands in for the tunnel's group over helper
// outbounds. Like the group, it exists while a helper outbound does.
func (r *LocalBackend) personalProxiesServer() (*servers.Server, bool) {
	for _, srv := range r.srvManager.AllServers() {
		if lbA.IsHelperTag(srv.Tag) {
			return &servers.Server{Tag: vpn.PersonalProxiesTag, Type: lbC.TypeMutableURLTest}, true
		}
	}
	return nil, false
}
```

Replace `r.srvManager.GetServerByTag(` with `r.serverByTag(` in exactly these places: `clearSelectedIfMissing` (line ~1165), `ConnectVPN` (~1350), `connectable` (~1404), `persistSelection` (~1576), and the three calls in `SelectedServer` (~1607, ~1616, ~1625). Leave `updateServers`/eviction and `GetServerByTag`'s other callers alone.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race -tags with_clash_api ./backend/ ./ipc/`
Expected: PASS.

- [ ] **Step 5: Run the whole module and commit**

Run: `go test -race -tags with_clash_api ./...`
Expected: PASS.

```bash
git add backend
git commit -m "backend: select and persist the personal-proxies group by tag"
```

Then open the radiance PR with `/create-pr` (title "Personal proxies: a selectable group over helper outbounds"). Record the PR URL for Task 11.

---

### Task 4: lantern-core — local nickname for a personal proxy (`rename_helper`)

**Files:**
- Modify: `lantern-core/helper/state.go:72-82` (`pairing.Nickname`)
- Modify: `lantern-core/helper/ops.go:138-146` (`view` shows the nickname), `:599-613` (`endAllHelpers` blanks it), add `renameHelper` after `endPairing` (`:573-592`)
- Modify: `lantern-core/helper/engine.go:104-124` (dispatch)
- Modify: `lib/features/helpers/provider/helpers_notifier.dart:356-361` (add `renameHelper` after `endPairing`)
- Test: `lantern-core/helper/engine_test.go`, `test/features/helpers/helpers_notifier_test.dart`

**Interfaces:**
- Produces: engine op `rename_helper` with args `{"pairing_id": string, "name": string}`; returns the view. Only inviter-role pairings (the user's personal proxies) can be renamed; others answer `not_found`. A blank name clears the nickname; an invalid one answers `bad_name`. The view's `helpers[].name` is the nickname when set, else the name the friend's app sent. Dart: `Future<HelperResult> HelpersNotifier.renameHelper(String pairingId, String name)`.

- [ ] **Step 1: Write the failing Go test**

Append to `lantern-core/helper/engine_test.go`:

```go
func TestRenameHelperIsLocalAndSurvivesRestarts(t *testing.T) {
	cloud := newFakeCloud()
	saraDir := t.TempDir()
	sara := newTestEngine(t, cloud, "sara", saraDir)
	ali := pair(t, cloud, sara, t.TempDir())
	id := list(call(t, sara, "view", nil), "helpers")[0]["pairing_id"]

	before := relayCalls(cloud)
	v := call(t, sara, "rename_helper", map[string]any{"pairing_id": id, "name": "  Cousin Ali "})
	assert.Equal(t, "Cousin Ali", list(v, "helpers")[0]["name"])
	assert.Equal(t, before, relayCalls(cloud), "a nickname never leaves the device")

	again := newTestEngine(t, cloud, "sara", saraDir)
	assert.Equal(t, "Cousin Ali", list(call(t, again, "refresh", nil), "helpers")[0]["name"],
		"kept across restarts and refreshes")

	v = call(t, again, "rename_helper", map[string]any{"pairing_id": id, "name": " "})
	assert.Equal(t, "Ali", list(v, "helpers")[0]["name"], "a blank name restores the friend's")

	assert.Equal(t, CodeBadName, callErr(t, again, "rename_helper", map[string]any{"pairing_id": id, "name": "a‮b"}))
	assert.Equal(t, CodeNotFound, callErr(t, again, "rename_helper", map[string]any{"pairing_id": "nope", "name": "X"}))
	helping := list(call(t, ali, "refresh", nil), "helping")[0]["pairing_id"]
	assert.Equal(t, CodeNotFound, callErr(t, ali, "rename_helper", map[string]any{"pairing_id": helping, "name": "X"}),
		"only the user's own personal proxies are renamed")
}

func TestEndAllHelpersErasesNicknames(t *testing.T) {
	cloud := newFakeCloud()
	dir := t.TempDir()
	sara := newTestEngine(t, cloud, "sara", dir)
	pair(t, cloud, sara, t.TempDir())
	id := list(call(t, sara, "view", nil), "helpers")[0]["pairing_id"]
	call(t, sara, "rename_helper", map[string]any{"pairing_id": id, "name": "Cousin"})
	// Offline, as in TestEndAllHelpersOfflineWipesNames: the names (and now
	// nicknames) go before any request.
	cloud.down = true
	assert.Equal(t, CodeUnavailable, callErr(t, sara, "end_all_helpers", nil))
	for _, p := range newTestEngine(t, cloud, "sara", dir).st.Pairings {
		assert.Empty(t, p.Nickname)
		assert.Empty(t, p.Name)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./lantern-core/helper/ -run 'Rename|ErasesNicknames'`
Expected: FAIL (`internal` error code for the unknown op; `Nickname` undefined).

- [ ] **Step 3: Implement**

`state.go`, in `pairing` after `Name`:

```go
	// Nickname is the name this user gave the pairing on this device. It is
	// shown instead of Name and never leaves the device. Inviter side only.
	Nickname string `json:"nickname,omitempty"`
```

`ops.go`, in `view()` replace the pairing loop body's first line with:

```go
		pv := PairingView{PairingID: p.ID, Name: p.Name, SinceUnix: p.Since.Unix()}
		if p.Nickname != "" {
			pv.Name = p.Nickname
		}
```

`ops.go`, in `endAllHelpers` change the first loop to blank both:

```go
	for _, p := range e.st.Pairings {
		if p.Role == RoleInviter {
			p.Name = ""
			p.Nickname = ""
		}
	}
```

`ops.go`, after `endPairing`:

```go
// renameHelper sets the nickname this device shows for one of its personal
// proxies, or clears it when the name is blank. Nothing is sent.
func (e *Engine) renameHelper(args []byte) (any, error) {
	var a struct {
		PairingID string `json:"pairing_id"`
		Name      string `json:"name"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	p := e.pairing(a.PairingID)
	if p == nil || p.Role != RoleInviter {
		return nil, errCode(CodeNotFound)
	}
	nickname := ""
	if strings.TrimSpace(a.Name) != "" {
		name, err := helperinvite.CleanName(a.Name)
		if err != nil {
			return nil, errCode(CodeBadName)
		}
		nickname = name
	}
	p.Nickname = nickname
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.view(), nil
}
```

(add `"strings"` to `ops.go` imports.)

`engine.go`, in the `switch op` after `end_all_helpers`:

```go
	case "rename_helper":
		out, err = e.renameHelper(args)
```

- [ ] **Step 4: Run the Go tests**

Run: `CGO_ENABLED=1 go test -race ./lantern-core/helper/...`
Expected: PASS.

- [ ] **Step 5: Write the failing Dart test**

In `test/features/helpers/helpers_notifier_test.dart`, add inside `main()` after the `'a change to who helps this device refetches config once'` group:

```dart
  test('renameHelper sends rename_helper and applies the view', () async {
    svc.answers = {
      'rename_helper': _view(
        helpers: '[{"pairing_id":"p1","name":"Cousin","since_unix":1}]',
      ),
    };
    final c = container();
    final r = await c
        .read(helpersProvider.notifier)
        .renameHelper('p1', 'Cousin');
    expect(r.ok, isTrue);
    expect(svc.calls, ['rename_helper']);
    expect(c.read(helpersProvider).view.helpers.single.name, 'Cousin');
    await Future<void>.delayed(Duration.zero);
    expect(svc.configRequests, 0, reason: 'a nickname changes no route');
  });
```

Run: `touch app.env && flutter test test/features/helpers/helpers_notifier_test.dart`
Expected: FAIL (`renameHelper` not defined).

- [ ] **Step 6: Implement in Dart**

In `helpers_notifier.dart`, after `endPairing`:

```dart
  /// Sets the name this device shows for one of its personal proxies, or
  /// restores the friend's own when [name] is blank. It stays on this device.
  Future<HelperResult> renameHelper(String pairingId, String name) async =>
      _apply(
        await _call('rename_helper', {'pairing_id': pairingId, 'name': name}),
      );
```

Run: `flutter test test/features/helpers/helpers_notifier_test.dart` — PASS.

- [ ] **Step 7: Commit**

```bash
git add lantern-core/helper lib/features/helpers/provider/helpers_notifier.dart test/features/helpers/helpers_notifier_test.dart
git commit -m "helper: local-only nickname for a personal proxy (rename_helper)"
```

---

### Task 5: App — `ServerLocationType.personalProxy`, its location and the Selected Location row

**Files:**
- Modify: `lib/core/common/app_eum.dart:14-29`
- Modify: `lib/core/utils/helper_tags.dart:25-44` (tag const, redaction)
- Modify: `lib/core/models/server_location.dart:41-93` (add `ServerLocation.personalProxy` and `ServerLocation.selected`)
- Modify: `lib/lantern/lantern_ffi_service.dart:1660-1667`, `lib/lantern/lantern_platform_service.dart:1663-1670` (use `ServerLocation.selected`)
- Modify: `lib/features/vpn/location_setting.dart:21-47`
- Modify: `lib/features/vpn/provider/vpn_notifier.dart:218-226` (fallback also switches the display to Smart Location for a personal proxy)
- Modify: `assets/locales/en.po` (add `personal_proxy`)
- Test: `test/core/models/server_location_helper_test.dart`, `test/core/models/available_servers_helper_test.dart`, `test/features/vpn/location_setting_test.dart` (new)

**Interfaces:**
- Produces: `ServerLocationType.personalProxy` (name `'personalProxy'`, parsed by `toServerLocationType`); `const personalProxiesTag = 'personal-proxies'` in `lib/core/utils/helper_tags.dart`; `factory ServerLocation.personalProxy()` (`serverName: personalProxiesTag`, `serverType: 'personalProxy'`, empty country/city/protocol, `displayName: 'personal_proxy'.i18n`); `factory ServerLocation.selected(Server server)` (maps a radiance selected server to its location, `personal-proxies` → `ServerLocation.personalProxy()`). `redactHelperTags` also maps `personal-proxies` to `helper`.

- [ ] **Step 1: Write the failing tests**

Append to `test/core/models/server_location_helper_test.dart` (add `import 'package:lantern/core/utils/helper_tags.dart';`):

```dart
  test('the personal proxies location shows no place', () {
    final loc = ServerLocation.personalProxy();
    expect(loc.serverType, ServerLocationType.personalProxy.name);
    expect(loc.serverName, personalProxiesTag);
    expect(loc.serverType.toServerLocationType, ServerLocationType.personalProxy);
    expect(loc.country, isEmpty);
    expect(loc.city, isEmpty);
    expect(loc.countryCode, isEmpty);
    expect(loc.protocol, isEmpty);
  });

  test('a selected server maps to its location type', () {
    Server s(String tag, bool lantern) => Server.fromJson({
      'tag': tag,
      'type': 'mutableurltest',
      'isLantern': lantern,
      'location': {'country': '', 'city': '', 'country_code': ''},
    });
    expect(
      ServerLocation.selected(s(personalProxiesTag, false)).serverType,
      ServerLocationType.personalProxy.name,
    );
    expect(
      ServerLocation.selected(s('lantern-1', true)).serverType,
      ServerLocationType.lanternLocation.name,
    );
    expect(
      ServerLocation.selected(s('my-vps', false)).serverType,
      ServerLocationType.privateServer.name,
    );
  });
```

Append to `test/core/models/available_servers_helper_test.dart`, inside `main()`:

```dart
  test('redactHelperTags redacts the personal proxies group tag', () {
    expect(
      redactHelperTags('Connecting to server: personalProxy with tag: personal-proxies'),
      'Connecting to server: personalProxy with tag: helper',
    );
    expect(
      redactHelperTags('outbound/mutableurltest[personal-proxies]: dial tcp 203.0.113.7:443'),
      'outbound/mutableurltest[helper]: dial tcp <redacted>',
    );
  });
```

Create `test/features/vpn/location_setting_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/core/models/server_location.dart';
import 'package:lantern/features/vpn/location_setting.dart';
import 'package:lantern/features/vpn/provider/server_location_notifier.dart';

void main() {
  testWidgets('a personal proxy shows as "Personal proxy" with no place', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          serverLocationProvider.overrideWithValue(
            ServerLocation.personalProxy(),
          ),
        ],
        child: ScreenUtilInit(
          designSize: const Size(393, 852),
          child: MaterialApp(
            theme: AppTheme.appTheme(),
            home: const Scaffold(body: LocationSetting()),
          ),
        ),
      ),
    );
    await tester.pump();
    expect(find.text('selected_location'.i18n), findsOneWidget);
    expect(find.text('personal_proxy'.i18n), findsOneWidget);
    expect(find.byType(Flag), findsNothing);
  });
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/core/models/server_location_helper_test.dart test/core/models/available_servers_helper_test.dart test/features/vpn/location_setting_test.dart`
Expected: FAIL to compile (`personalProxy` not a member, `personalProxiesTag` undefined).

- [ ] **Step 3: Implement**

`app_eum.dart`:

```dart
enum ServerLocationType { auto, privateServer, lanternLocation, personalProxy }
```

and in `toServerLocationType` add before `default`:

```dart
      case 'personalProxy':
        return ServerLocationType.personalProxy;
```

`helper_tags.dart`, after `isHelperTag`:

```dart
/// radiance's url-test group over every helper outbound (vpn.PersonalProxiesTag).
/// Selecting it connects through the user's personal proxies. It marks the
/// device as having them, so it is redacted like a helper tag.
const personalProxiesTag = 'personal-proxies';
```

and change `_namesHelper` and the last line of `redactHelperTags`:

```dart
bool _namesHelper(String s) =>
    s.contains(personalProxiesTag) ||
    (s.contains('helper') && _helperNamed.hasMatch(s));
```

```dart
  return redacted
      .replaceAll(_helperTag, 'helper')
      .replaceAll(personalProxiesTag, 'helper');
```

`server_location.dart`, after `ServerLocation.fromServer`:

```dart
  /// The user's personal proxies, selected as one location. Where the friends
  /// are is never shown.
  factory ServerLocation.personalProxy() => ServerLocation(
    serverName: personalProxiesTag,
    serverType: ServerLocationType.personalProxy.name,
    displayName: 'personal_proxy'.i18n,
  );

  /// The location of the server radiance reports as selected.
  factory ServerLocation.selected(Server server) {
    if (server.tag == personalProxiesTag) return ServerLocation.personalProxy();
    return ServerLocation.fromServer(server: server).copyWith(
      serverType: server.isLantern
          ? ServerLocationType.lanternLocation.name
          : ServerLocationType.privateServer.name,
    );
  }
```

In both `lantern_ffi_service.dart` and `lantern_platform_service.dart` `getSelectedServerLocation`, replace the block from `final isLantern = server.isLantern;` through the `Right(...)` with:

```dart
      return Right(ServerLocation.selected(server));
```

`location_setting.dart`, add to the switch:

```dart
      case ServerLocationType.personalProxy:
        title = 'selected_location'.i18n;
        value = 'personal_proxy'.i18n;
        break;
```

`vpn_notifier.dart`, in `startVPN` replace the `!tagAvailable` branch with:

```dart
    if (!tagAvailable) {
      appLogger.debug(
        'Server tag "$tag" not available, falling back to auto VPN',
      );
      if (type == ServerLocationType.personalProxy) {
        // The group is gone with the last pairing: show Smart Location, which
        // is what connects.
        await ref.read(serverLocationProvider.notifier).switchToAuto();
      }
      return lantern.startVPN();
    }
```

`en.po`, in the helper-invites block:

```
# The VPN tab's selected location while connected through the user's personal proxies.
msgid "personal_proxy"
msgstr "Personal proxy"
```

- [ ] **Step 4: Run the tests and analyzer**

Run: `flutter test test/core/models/ test/features/vpn/ && flutter analyze`
Expected: PASS, no analyzer issues (every `switch` on `ServerLocationType` is exhaustive).

- [ ] **Step 5: Commit**

```bash
git add lib/core lib/lantern lib/features/vpn assets/locales/en.po test/core test/features/vpn
git commit -m "vpn: personal proxy location type and Selected Location row"
```

---

### Task 6: App — "Invite a Personal Proxy" and "Manage Private Locations" in the server-list menu

**Files:**
- Create: `lib/features/helpers/personal_proxy_invite.dart`
- Modify: `lib/features/helpers/invite_share.dart:72,80,108` (titles)
- Modify: `lib/features/vpn/server_selection.dart:287-322` (menu → `PrivateLocationsMenu`)
- Modify: `lib/features/private_server/manage_private_server.dart:37,41,54`, `lib/features/setting/vpn_setting.dart:152` (`manage_private_locations`)
- Modify: `assets/locales/en.po` (add `invite_personal_proxy`, `manage_private_locations`)
- Test: `test/features/vpn/private_locations_menu_test.dart` (new), `test/features/helpers/inviter_screens_test.dart`

**Interfaces:**
- Consumes: `helperInvitesAvailableProvider`, `HelpersNotifier.createInvite`, `askHelperName`, `helperErrorText`.
- Produces: `Future<void> startPersonalProxyInvite(BuildContext context, WidgetRef ref)` (asks a name titled "Invite a Personal Proxy", creates the invite, pushes `InviteShare(inviteId:)`); `class PrivateLocationsMenu extends ConsumerWidget` with `required VoidCallback onInvitePersonalProxy`; tile keys `server_selection.invite_personal_proxy` (new), `server_selection.manage_private_servers` (kept).

- [ ] **Step 1: Write the failing tests**

Create `test/features/vpn/private_locations_menu_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';
import 'package:lantern/features/vpn/server_selection.dart';

Future<int Function()> _pump(WidgetTester tester, {required bool invites}) async {
  var invited = 0;
  await tester.pumpWidget(
    ProviderScope(
      overrides: [helperInvitesAvailableProvider.overrideWithValue(invites)],
      child: ScreenUtilInit(
        designSize: const Size(393, 852),
        child: MaterialApp(
          theme: AppTheme.appTheme(),
          home: Scaffold(
            body: PrivateLocationsMenu(onInvitePersonalProxy: () => invited++),
          ),
        ),
      ),
    ),
  );
  await tester.pump();
  return () => invited;
}

void main() {
  testWidgets('offers "Invite a Personal Proxy" while invites are available', (
    tester,
  ) async {
    final invited = await _pump(tester, invites: true);
    expect(find.text('invite_personal_proxy'.i18n), findsOneWidget);
    await tester.tap(find.byKey(const Key('server_selection.invite_personal_proxy')));
    expect(invited(), 1);
  });

  testWidgets('no invite entry without helper invites (flag off or stealth)', (
    tester,
  ) async {
    await _pump(tester, invites: false);
    expect(find.byKey(const Key('server_selection.invite_personal_proxy')), findsNothing);
  });

  testWidgets('"Manage Private Locations" replaces "Manage Private Servers"', (
    tester,
  ) async {
    await _pump(tester, invites: false);
    expect(find.text('manage_private_locations'.i18n), findsOneWidget);
    expect(find.text('manage_private_servers'.i18n), findsNothing);
  });
}
```

In `test/features/helpers/inviter_screens_test.dart`, add `import 'package:lantern/features/helpers/personal_proxy_invite.dart';` and, inside the `group('with sl', …)`, add:

```dart
    testWidgets('startPersonalProxyInvite names the invite, then opens it', (
      tester,
    ) async {
      final router = _Router();
      sl.registerSingleton<AppRouter>(router);
      final fake = _Helpers(const HelpersState());
      await _pump(
        tester,
        fake,
        Consumer(
          builder: (context, ref, _) => TextButton(
            onPressed: () => startPersonalProxyInvite(context, ref),
            child: const Text('go'),
          ),
        ),
      );
      await tester.tap(find.text('go'));
      await tester.pumpAndSettle();
      expect(find.text('invite_personal_proxy'.i18n), findsOneWidget);
      await tester.enterText(find.byType(TextFormField), 'Sara');
      await tester.tap(find.text('continue'.i18n));
      await tester.pumpAndSettle();
      expect(fake.created, ['Sara']);
      expect((router.pushed.single.args as InviteShareArgs).inviteId, 'i9');
    });
```

and in the existing `'the invite screen shows a pending invite as a QR code'` test add `expect(find.text('invite_personal_proxy'.i18n), findsOneWidget);` after the QR expectation.

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/features/vpn/private_locations_menu_test.dart test/features/helpers/inviter_screens_test.dart`
Expected: FAIL to compile (`PrivateLocationsMenu`, `startPersonalProxyInvite` undefined).

- [ ] **Step 3: Implement**

Create `lib/features/helpers/personal_proxy_invite.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/approve_helper_sheet.dart';
import 'package:lantern/features/helpers/helper_errors.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// "Invite a Personal Proxy": asks the name the friend knows the user by,
/// creates the invite and opens it to share. The server-list menu, the VPN
/// tab banner and the Manage tab all start here.
Future<void> startPersonalProxyInvite(BuildContext context, WidgetRef ref) async {
  // Held rather than read after an await: the caller may be gone by then.
  final notifier = ref.read(helpersProvider.notifier);
  final name = await askHelperName(
    context,
    initial: ref.read(helpersProvider).view.myName,
    title: 'invite_personal_proxy'.i18n,
  );
  if (name == null || !context.mounted) return;
  final r = await notifier.createInvite(name);
  if (!context.mounted) return;
  final id = r.data['invite_id'];
  if (!r.ok || id is! String) {
    context.showSnackBarError(helperErrorText(r.error ?? 'unavailable'));
    return;
  }
  appRouter.push(InviteShare(inviteId: id));
}
```

`invite_share.dart`: change the three `title: 'invite_share_title'.i18n` to `title: 'invite_personal_proxy'.i18n`.

`server_selection.dart`: replace the body of `onOpenMoreOptions` with:

```dart
  void onOpenMoreOptions() {
    showAppBottomSheet(
      context: context,
      title: 'private_server_options'.i18n,
      scrollControlDisabledMaxHeightRatio: .4,
      builder: (sheetContext, scrollController) => PrivateLocationsMenu(
        onInvitePersonalProxy: () {
          Navigator.of(sheetContext).pop();
          startPersonalProxyInvite(context, ref);
        },
      ),
    );
  }
```

and add, after `_ServerSelectionState`:

```dart
/// The server list's menu: set up, join, invite a personal proxy, manage.
class PrivateLocationsMenu extends ConsumerWidget {
  const PrivateLocationsMenu({super.key, required this.onInvitePersonalProxy});

  final VoidCallback onInvitePersonalProxy;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final invites = ref.watch(helperInvitesAvailableProvider);
    return ListView(
      padding: EdgeInsets.zero,
      shrinkWrap: true,
      children: [
        AppTile(
          tileKey: const Key('server_selection.setup_private_server'),
          label: 'setup_private_server'.i18n,
          onPressed: () => context.pushRoute(PrivateServerSetup()),
        ),
        const DividerSpace(padding: EdgeInsets.zero),
        AppTile(
          tileKey: const Key('server_selection.join_private_server'),
          label: 'join_a_private_server'.i18n,
          onPressed: () => context.pushRoute(JoinPrivateServer()),
        ),
        if (invites) ...[
          const DividerSpace(padding: EdgeInsets.zero),
          AppTile(
            tileKey: const Key('server_selection.invite_personal_proxy'),
            label: 'invite_personal_proxy'.i18n,
            onPressed: onInvitePersonalProxy,
          ),
        ],
        const DividerSpace(padding: EdgeInsets.zero),
        AppTile(
          tileKey: const Key('server_selection.manage_private_servers'),
          label: 'manage_private_locations'.i18n,
          onPressed: () => context.pushRoute(ManagePrivateServer()),
        ),
      ],
    );
  }
}
```

(imports: `package:lantern/features/helpers/personal_proxy_invite.dart`, `package:lantern/features/helpers/provider/helpers_notifier.dart`.)

Replace `'manage_private_servers'.i18n` with `'manage_private_locations'.i18n` in `manage_private_server.dart` (3 places) and `vpn_setting.dart:152`.

`en.po`:

```
# The screen and menu entry for the user's private servers and personal proxies.
msgid "manage_private_locations"
msgstr "Manage Private Locations"

# Starts an invite to a friend abroad who will become the user's personal proxy.
msgid "invite_personal_proxy"
msgstr "Invite a Personal Proxy"
```

Then `grep -rn "'manage_private_servers'\|'invite_share_title'" lib test`; if nothing references a key, delete its `msgid`/`msgstr` pair from `en.po`.

- [ ] **Step 4: Run tests and analyzer**

Run: `flutter test test/features/vpn/ test/features/helpers/ && flutter analyze`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/features assets/locales/en.po test/features
git commit -m "vpn: Invite a Personal Proxy and Manage Private Locations in the server list menu"
```

---

### Task 7: App — "Private Locations" tab with the Personal proxies section

**Files:**
- Create: `lib/features/vpn/personal_proxies_section.dart`
- Modify: `lib/features/vpn/server_selection.dart:36-46` (refresh the helpers view), `:85-87` (show tabs with proxies), `:154-158` (tab label), `:740-927` (`PrivateServerLocationListView`)
- Modify: `assets/locales/en.po` (add `private_locations`, `personal_proxies`, `your_personal_proxies`)
- Test: `test/features/vpn/personal_proxies_section_test.dart` (new)

**Interfaces:**
- Consumes: `helpersProvider` (`view.helpers`: `HelperPairingView{pairingId,name,online}`), `ServerLocation.personalProxy()`, `personalProxiesTag`, `ServerLocationType.personalProxy` (Task 5), `VpnNotifier.connectToServer`.
- Produces: `class PersonalProxiesSection extends ConsumerWidget` with `required bool selected`, `required VoidCallback onSelect`; keys `server_selection.personal_proxies` (selectable row), `server_selection.personal_proxy.<pairingId>` (friend rows). Renders nothing when `view.helpers` is empty.

- [ ] **Step 1: Write the failing tests**

Create `test/features/vpn/personal_proxies_section_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';
import 'package:lantern/features/vpn/personal_proxies_section.dart';

class _Helpers extends HelpersNotifier {
  _Helpers(this.initial);
  final HelpersState initial;
  @override
  HelpersState build() => initial;
  @override
  Future<void> refreshIfUsed() async {}
}

HelpersState _with(List<HelperPairingView> helpers) =>
    HelpersState(viewLoaded: true, view: HelpersView(unlocked: true, helpers: helpers));

Future<List<int>> _pump(
  WidgetTester tester,
  HelpersState state, {
  bool selected = false,
}) async {
  final taps = <int>[];
  await tester.pumpWidget(
    ProviderScope(
      overrides: [helpersProvider.overrideWith(() => _Helpers(state))],
      child: ScreenUtilInit(
        designSize: const Size(393, 852),
        child: MaterialApp(
          theme: AppTheme.appTheme(),
          home: Scaffold(
            body: PersonalProxiesSection(
              selected: selected,
              onSelect: () => taps.add(1),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.pump();
  return taps;
}

void main() {
  const ali = HelperPairingView(pairingId: 'p1', name: 'Ali', online: true);
  const bo = HelperPairingView(pairingId: 'p2', name: 'Bo');

  testWidgets('one selectable row, then each friend for information', (
    tester,
  ) async {
    final taps = await _pump(tester, _with([ali, bo]));
    expect(find.text('personal_proxies'.i18n), findsOneWidget);
    expect(find.text('your_personal_proxies'.i18n), findsOneWidget);
    expect(find.text('Ali'), findsOneWidget);
    expect(find.text('Bo'), findsOneWidget);
    expect(find.text('helper_online'.i18n), findsOneWidget);
    expect(find.text('helper_offline'.i18n), findsOneWidget);

    await tester.tap(find.byKey(const Key('server_selection.personal_proxy.p1')));
    expect(taps, isEmpty, reason: 'friend rows are not tappable');
    await tester.tap(find.byKey(const Key('server_selection.personal_proxies')));
    expect(taps, hasLength(1));
  });

  testWidgets('the row is disabled while no friend is online', (tester) async {
    final taps = await _pump(tester, _with([bo]));
    await tester.tap(find.byKey(const Key('server_selection.personal_proxies')));
    expect(taps, isEmpty);
  });

  testWidgets('absent without pairings', (tester) async {
    await _pump(tester, _with(const []));
    expect(find.text('personal_proxies'.i18n), findsNothing);
    expect(find.byKey(const Key('server_selection.personal_proxies')), findsNothing);
  });

  testWidgets('selecting the selected row does nothing', (tester) async {
    final taps = await _pump(tester, _with([ali]), selected: true);
    await tester.tap(find.byKey(const Key('server_selection.personal_proxies')));
    expect(taps, isEmpty);
  });
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/features/vpn/personal_proxies_section_test.dart`
Expected: FAIL to compile (`personal_proxies_section.dart` missing).

- [ ] **Step 3: Implement the section**

Create `lib/features/vpn/personal_proxies_section.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// The Private Locations tab's "Personal proxies": one selectable row that
/// connects through every friend at once, then each friend by name with an
/// online or offline label, for information only. Nothing shows without a
/// pairing; the row is disabled while no friend is online.
class PersonalProxiesSection extends ConsumerWidget {
  const PersonalProxiesSection({
    super.key,
    required this.selected,
    required this.onSelect,
  });

  final bool selected;
  final VoidCallback onSelect;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final helpers = ref.watch(helpersProvider.select((s) => s.view.helpers));
    if (helpers.isEmpty) return const SizedBox.shrink();
    final anyOnline = helpers.any((h) => h.online);
    final textTheme = Theme.of(context).textTheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(left: 16.0),
          child: HeaderText('personal_proxies'.i18n),
        ),
        const SizedBox(height: 8),
        AppCard(
          padding: EdgeInsets.zero,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              AppTile(
                tileKey: const Key('server_selection.personal_proxies'),
                icon: AppImagePaths.location,
                label: 'your_personal_proxies'.i18n,
                enabled: anyOnline,
                onPressed: anyOnline && !selected ? onSelect : null,
                trailing: selected ? AppImage(path: AppImagePaths.blot) : null,
              ),
              for (final h in helpers) ...[
                const DividerSpace(),
                _FriendRow(helper: h, textTheme: textTheme),
              ],
            ],
          ),
        ),
        const SizedBox(height: defaultSize),
      ],
    );
  }
}

class _FriendRow extends StatelessWidget {
  const _FriendRow({required this.helper, required this.textTheme});

  final HelperPairingView helper;
  final TextTheme textTheme;

  @override
  Widget build(BuildContext context) => AppTile(
    tileKey: Key('server_selection.personal_proxy.${helper.pairingId}'),
    label: helper.name.isEmpty ? 'helper_unnamed'.i18n : helper.name,
    subtitle: Row(
      children: [
        Icon(
          Icons.circle,
          size: 10,
          color: helper.online ? context.statusSuccessBgDot : context.textTertiary,
        ),
        const SizedBox(width: 6),
        Text(
          helper.online ? 'helper_online'.i18n : 'helper_offline'.i18n,
          style: textTheme.labelMedium!.copyWith(color: context.textTertiary),
        ),
      ],
    ),
  );
}
```

- [ ] **Step 4: Wire it into the server list**

In `server_selection.dart`:

1. In `initState`'s post-frame callback, after the available-servers refresh, add `unawaited(ref.read(helpersProvider.notifier).refreshIfUsed());` (never `refresh`: opening the list must not touch the keystore).
2. Replace `final isPrivateServerFound = availableServers.requireValue.hasUserServers;` with:

```dart
    final hasPersonalProxies = ref.watch(
      helpersProvider.select((s) => s.view.helpers.isNotEmpty),
    );
    final isPrivateServerFound =
        availableServers.requireValue.hasUserServers || hasPersonalProxies;
```

3. Change the second tab's text to `Text('private_locations'.i18n)` (keep its key `server_selection.private_servers_tab`).
4. In `_PrivateServerLocationListViewState.build`, replace everything from `final userLocations = …` to the end of `build` with:

```dart
    final userLocations = availableServers.requireValue.userServers;
    final selectedTag = selected.serverName;
    final personalSelected =
        selected.serverType.toServerLocationType == ServerLocationType.personalProxy;
    final hasPersonalProxies = ref.watch(
      helpersProvider.select((s) => s.view.helpers.isNotEmpty),
    );

    if (userLocations.isEmpty && !hasPersonalProxies) {
      return Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Text(
            'no_private_server_setup_yet'.i18n,
            textAlign: TextAlign.center,
            style: _textTheme!.titleSmall!.copyWith(color: AppColors.gray8),
          ),
          const SizedBox(height: 16),
          PrimaryButton(
            label: 'setup_private_server'.i18n,
            onPressed: () => context.pushRoute(VPNSetting()),
          ),
        ],
      );
    }

    return ListView(
      padding: EdgeInsets.zero,
      children: [
        const SizedBox(height: 16),
        PersonalProxiesSection(
          selected: personalSelected,
          onSelect: () => _select(
            ServerLocationType.personalProxy,
            personalProxiesTag,
            ServerLocation.personalProxy(),
          ),
        ),
        if (userLocations.isNotEmpty) ...[
          Padding(
            padding: const EdgeInsets.only(left: 16.0),
            child: HeaderText('your_server'.i18n),
          ),
          const SizedBox(height: 8),
          AppCard(
            padding: EdgeInsets.zero,
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                for (final (i, server) in userLocations.indexed) ...[
                  if (i > 0) const DividerSpace(),
                  _privateServerTile(server, selectedTag == server.tag),
                ],
              ],
            ),
          ),
        ],
      ],
    );
  }

  Widget _privateServerTile(Server server, bool isSelected) => AppTile(
    tileKey: Key('server_selection.private_server.${server.tag}'),
    onPressed: () {
      if (isSelected) {
        appLogger.debug('Already selected this server');
        context.showSnackBar('server_already_selected'.i18n);
        return;
      }
      onPrivateServerSelected(server);
    },
    icon: Flag(countryCode: server.location.countryCode, size: const Size(40, 28)),
    label: server.tag,
    subtitle: Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Text(
        '${server.location.city} - ${server.type}',
        style: _textTheme!.labelMedium!.copyWith(color: context.textTertiary),
      ),
    ),
    trailing: isSelected ? AppImage(path: AppImagePaths.blot) : null,
  );
```

5. Replace `onPrivateServerSelected` with a thin wrapper over a shared `_select` (same behavior as today for private servers):

```dart
  Future<void> onPrivateServerSelected(Server server) => _select(
    ServerLocationType.privateServer,
    server.tag,
    ServerLocation(
      serverType: ServerLocationType.privateServer.name,
      serverName: server.tag,
      country: server.location.country,
      city: server.location.city,
      countryCode: server.location.countryCode,
      protocol: server.type,
    ),
    connectedMessage: 'connected_to_private_server'.i18n,
  );

  /// Connects to [tag] and, once connected, makes [location] the selection.
  Future<void> _select(
    ServerLocationType type,
    String tag,
    ServerLocation location, {
    String? connectedMessage,
  }) async {
    context.showLoadingDialog();
    void selected() {
      context.hideLoadingDialog();
      if (connectedMessage != null) context.showSnackBar(connectedMessage);
      ref.read(serverLocationProvider.notifier).updateServerLocation(location);
      appRouter.popUntilRoot();
    }

    final result = await ref.read(vpnProvider.notifier).connectToServer(type, tag);
    result.fold(
      (failure) {
        context.hideLoadingDialog();
        if (failure is! VpnConflictFailure) {
          context.showSnackBar(failure.localizedErrorMessage);
          return;
        }
        AppDialog.vpnConflictDialog(
          context: context,
          onConnectAnyway: () async {
            appRouter.maybePop();
            final retry = await ref
                .read(vpnProvider.notifier)
                .connectToServer(type, tag, skipConflictCheck: true);
            retry.fold((failure) {
              context.hideLoadingDialog();
              context.showSnackBar(failure.localizedErrorMessage);
            }, (_) => selected());
          },
        );
      },
      (_) => selected(),
    );
  }
```

(imports: `personal_proxies_section.dart`, `helpers_notifier.dart`, `package:lantern/core/utils/helper_tags.dart`.)

`en.po`:

```
# The server list's tab for private servers and personal proxies.
msgid "private_locations"
msgstr "Private Locations"

# Section header in Private Locations: friends abroad who route this user's traffic.
msgid "personal_proxies"
msgstr "Personal proxies"

# The selectable row that connects through all of the user's personal proxies.
msgid "your_personal_proxies"
msgstr "Your personal proxies"
```

- [ ] **Step 5: Run tests and analyzer**

Run: `flutter test test/features/vpn/ && flutter analyze`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/features/vpn assets/locales/en.po test/features/vpn
git commit -m "vpn: Private Locations tab with a selectable Personal proxies row"
```

---

### Task 8: App — "Personal Proxies" tab in Manage Private Locations; remove GetHelp and the Settings entry

**Files:**
- Create: `lib/features/helpers/personal_proxies_tab.dart`
- Delete: `lib/features/helpers/get_help.dart`
- Modify: `lib/features/private_server/manage_private_server.dart:52-100` (third tab)
- Modify: `lib/features/helpers/provider/helpers_notifier.dart:24-37` (rename `helperEntryVisible` → `personalProxiesTabVisible`)
- Modify: `lib/features/helpers/approve_helper_sheet.dart:118-185` (`askHelperName` optional `label`)
- Modify: `lib/features/setting/setting.dart:13,28,79,185-193,360-362` (remove the entry)
- Modify: `lib/core/router/router.dart:40-43` (remove the `/get-help` route); regenerate `router.gr.dart`, `helpers_notifier.g.dart`
- Modify: `lib/features/helpers/helping_section.dart:60` (comment no longer names GetHelp)
- Modify: `assets/locales/en.po` (add `personal_proxies_tab`, `invite_another`, `personal_proxy_nickname_label`; delete keys left unused)
- Test: `test/features/helpers/inviter_screens_test.dart`, `test/features/helpers/helpers_notifier_test.dart`, `test/features/private_server/manage_private_server_test.dart` (new)

**Interfaces:**
- Consumes: `HelpersNotifier.renameHelper` (Task 4), `startPersonalProxyInvite` (Task 6), `HelperKeystoreResetNotice` (`helping_section.dart:183`), `showApproveHelperSheet`, `HelpersNotifier.{endPairing,forgetInvite,endAllHelpers,refreshIfUsed,acknowledgeKeystoreReset}`.
- Produces: `personalProxiesTabVisibleProvider` (same rule as the old `helperEntryVisibleProvider`); `class PersonalProxiesTab extends HookConsumerWidget`; keys `personal_proxies.rename.<pairingId>`, `personal_proxies.remove.<pairingId>`, `personal_proxies.cancel.<inviteId>`, `personal_proxies.invite`, `personal_proxies.remove_all`, `manage.personal_proxies_tab`; `askHelperName(context, {required initial, required title, String? label})`.

- [ ] **Step 1: Write the failing tests**

In `test/features/helpers/helpers_notifier_test.dart`, rename the group `'helperEntryVisibleProvider with the flag off'` to `'personalProxiesTabVisibleProvider with the flag off'` and replace every `helperEntryVisibleProvider` in it with `personalProxiesTabVisibleProvider` (the rule is unchanged, so the bodies stay).

In `test/features/helpers/inviter_screens_test.dart`:
- replace `import 'package:lantern/features/helpers/get_help.dart';` with `import 'package:lantern/features/helpers/personal_proxies_tab.dart';` and the `hide GetHelp, InviteShare, Setting` with `hide InviteShare, Setting`;
- replace every `const GetHelp()` with `const PersonalProxiesTab()` and `find.byType(GetHelp)` with `find.byType(PersonalProxiesTab)`;
- in `'inviting asks for a name, then opens the new invite by ID'`, tap `const Key('personal_proxies.invite')` instead of `helpers.invite`;
- in `'removing a helper ends that pairing'`, tap `find.byKey(const Key('personal_proxies.remove.p1'))` instead of the icon;
- in `'remove all still happens when the screen closes at once'`, tap `find.byKey(const Key('personal_proxies.remove_all'))`;
- delete `'one helper: status shown and a second helper suggested'`'s nudge expectation (the nudge text is gone) and keep its status expectations;
- replace `'the Settings tile follows helperInvitesAvailableProvider'` with:

```dart
    testWidgets('Settings no longer offers "Get help from a friend"', (
      tester,
    ) async {
      sl.registerSingleton<LocalStorageService>(_Storage(used: true));
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            helperInvitesAvailableProvider.overrideWithValue(true),
            isUserProProvider.overrideWithValue(true),
            isUserExpiredProvider.overrideWithValue(false),
            userEmailProvider.overrideWithValue(''),
            appSettingProvider.overrideWithValue(const AppSetting()),
            countryCodeProvider.overrideWithValue('US'),
            featureFlagProvider.overrideWithValue(const {}),
            homeProvider.overrideWith(_Home.new),
          ],
          child: ScreenUtilInit(
            designSize: const Size(393, 852),
            child: MaterialApp(theme: AppTheme.appTheme(), home: const Setting()),
          ),
        ),
      );
      await tester.pump();
      expect(find.byKey(const Key('setting.get_help_tile')), findsNothing);
    });
```

and add these tests at the end of `main()`:

```dart
  testWidgets('rename stores a nickname for that pairing', (tester) async {
    final fake = _Helpers(_oneHelper);
    await _pump(tester, fake, const PersonalProxiesTab());
    await tester.tap(find.byKey(const Key('personal_proxies.rename.p1')));
    await tester.pumpAndSettle();
    expect(find.text('personal_proxy_nickname_label'.i18n), findsOneWidget);
    await tester.enterText(find.byType(TextFormField), 'Cousin');
    await tester.tap(find.text('continue'.i18n));
    await tester.pumpAndSettle();
    expect(fake.renamed, [('p1', 'Cousin')]);
  });

  testWidgets('a pending invite can be cancelled', (tester) async {
    final fake = _Helpers(
      HelpersState(
        view: HelpersView(
          unlocked: true,
          invites: [
            HelperInviteView(id: 'i2', state: 'pending', expiresUnix: _now() + 3600),
          ],
        ),
      ),
    );
    await _pump(tester, fake, const PersonalProxiesTab());
    await tester.tap(find.byKey(const Key('personal_proxies.cancel.i2')));
    await tester.pump();
    expect(fake.forgotten, ['i2']);
  });

  testWidgets('the tab polls for online status only while shown', (tester) async {
    final fake = _Helpers(_oneHelper);
    await _pump(tester, fake, const PersonalProxiesTab());
    final first = fake.refreshes;
    await tester.pump(const Duration(seconds: 61));
    expect(fake.refreshes, first + 1);
    await tester.pumpWidget(const SizedBox());
    await tester.pump(const Duration(seconds: 61));
    expect(fake.refreshes, first + 1);
  });
```

and extend `_Helpers` with:

```dart
  final renamed = <(String, String)>[];
  final forgotten = <String>[];

  @override
  Future<HelperResult> renameHelper(String pairingId, String name) async {
    renamed.add((pairingId, name));
    return HelperResult.parse('{}');
  }

  @override
  Future<HelperResult> forgetInvite(String inviteId) async {
    forgotten.add(inviteId);
    return HelperResult.parse('{}');
  }
```

Create `test/features/private_server/manage_private_server_test.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_screenutil/flutter_screenutil.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart' hide ManagePrivateServer;
import 'package:lantern/core/models/available_servers.dart';
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';
import 'package:lantern/features/private_server/manage_private_server.dart';
import 'package:lantern/features/vpn/provider/available_servers_notifier.dart';

class _Servers extends AvailableServersNotifier {
  @override
  Future<AvailableServers> build() async => AvailableServers(const []);
}

class _Helpers extends HelpersNotifier {
  @override
  HelpersState build() => const HelpersState(
    viewLoaded: true,
    view: HelpersView(
      unlocked: true,
      helpers: [HelperPairingView(pairingId: 'p1', name: 'Ali', online: true)],
    ),
  );
  @override
  Future<void> refreshIfUsed() async {}
}

Future<void> _pump(WidgetTester tester, {required bool tab}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        availableServersProvider.overrideWith(_Servers.new),
        helpersProvider.overrideWith(_Helpers.new),
        personalProxiesTabVisibleProvider.overrideWithValue(tab),
      ],
      child: ScreenUtilInit(
        designSize: const Size(393, 852),
        child: MaterialApp(theme: AppTheme.appTheme(), home: const ManagePrivateServer()),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('a third Personal Proxies tab while there is something to manage', (
    tester,
  ) async {
    await _pump(tester, tab: true);
    expect(find.text('manage_private_locations'.i18n), findsOneWidget);
    await tester.tap(find.byKey(const Key('manage.personal_proxies_tab')));
    await tester.pumpAndSettle();
    expect(find.text('Ali'), findsOneWidget);
    expect(find.byKey(const Key('personal_proxies.invite')), findsOneWidget);
  });

  testWidgets('two tabs otherwise', (tester) async {
    await _pump(tester, tab: false);
    expect(find.byKey(const Key('manage.personal_proxies_tab')), findsNothing);
  });
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/features/helpers/ test/features/private_server/`
Expected: FAIL to compile (`personal_proxies_tab.dart`, `personalProxiesTabVisibleProvider` missing).

- [ ] **Step 3: Implement**

`helpers_notifier.dart` — rename and re-document the provider:

```dart
/// Whether Manage Private Locations shows its "Personal Proxies" tab: while
/// helper invites are available, and after the flag goes off while this
/// device still has personal proxies or invites to manage, or may have them:
/// it used helper invites and no view has loaded yet. Reads only the
/// notifier's state and the used mark in local storage, never the keystore.
@riverpod
bool personalProxiesTabVisible(Ref ref) {
  if (!HelpersNotifier._buildEnabled) return false;
  if (ref.watch(helperInvitesAvailableProvider)) return true;
  final s = ref.watch(helpersProvider);
  if (!s.viewLoaded && ref.read(helpersProvider.notifier).used) return true;
  return s.view.helpers.isNotEmpty || s.view.invites.any((i) => !i.expired);
}
```

`approve_helper_sheet.dart` — add `String? label` to `askHelperName` and pass it to `_HelperNameField` (new field `final String? label;`), whose `AppTextField` uses `label: label ?? 'helpers_your_name_label'.i18n`.

Create `lib/features/helpers/personal_proxies_tab.dart` by moving `get_help.dart`'s body into a tab (no `BaseScreen`, no intro or nudge text), with these differences: rows show Rename and Remove; pending invites carry a Cancel button; the primary button is "Invite another" and calls `startPersonalProxyInvite`; the keystore notice reuses `HelperKeystoreResetNotice`:

```dart
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_hooks/flutter_hooks.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/approve_helper_sheet.dart';
import 'package:lantern/features/helpers/helper_errors.dart';
import 'package:lantern/features/helpers/helping_section.dart'
    show HelperKeystoreResetNotice;
import 'package:lantern/features/helpers/models/helpers_view.dart';
import 'package:lantern/features/helpers/personal_proxy_invite.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// Manage Private Locations' "Personal Proxies": each friend with Rename and
/// Remove, invites waiting on a friend (Cancel) or on the user (compare
/// codes), Invite another, and remove all.
class PersonalProxiesTab extends HookConsumerWidget {
  const PersonalProxiesTab({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(helpersProvider);
    final view = state.view;
    final notifier = ref.read(helpersProvider.notifier);
    final textTheme = Theme.of(context).textTheme;

    useEffect(() {
      // refreshIfUsed, not refresh: showing this tab must not touch the
      // keystore. Online status changes while it is shown.
      unawaited(notifier.refreshIfUsed());
      final timer = Timer.periodic(const Duration(seconds: 60), (_) {
        if (WidgetsBinding.instance.lifecycleState != AppLifecycleState.resumed) {
          return;
        }
        unawaited(notifier.refreshIfUsed());
      });
      return timer.cancel;
    }, const []);

    final claimed = view.invites.where((i) => i.claimed).toList();
    final pending = view.invites.where((i) => !i.claimed && !i.expired).toList();

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: defaultSize),
      children: [
        if (state.keystoreReset) ...[
          HelperKeystoreResetNotice(notifier: notifier),
          const SizedBox(height: defaultSize),
        ],
        for (final inv in claimed) ...[
          AppCard(
            padding: EdgeInsets.zero,
            child: AppTile(
              tileKey: Key('helpers.claimed.${inv.id}'),
              label: 'invite_claimed_title'.i18n,
              subtitle: Text(
                inv.volunteerName.isEmpty ? 'helper_unnamed'.i18n : inv.volunteerName,
              ),
              trailing: const Icon(Icons.chevron_right),
              onPressed: () => showApproveHelperSheet(context, inv),
            ),
          ),
          const SizedBox(height: defaultSize),
        ],
        if (view.helpers.isNotEmpty) ...[
          AppCard(
            padding: EdgeInsets.zero,
            child: Column(
              children: [
                for (final (i, h) in view.helpers.indexed) ...[
                  if (i > 0) const DividerSpace(),
                  _ProxyRow(index: i, helper: h, notifier: notifier),
                ],
              ],
            ),
          ),
          const SizedBox(height: defaultSize),
        ],
        for (final inv in pending)
          AppTile(
            label: 'invite_waiting'.i18n,
            subtitle: Text('invite_expires'.i18n.fill([_hoursLeft(inv.expiresUnix)])),
            onPressed: () => appRouter.push(InviteShare(inviteId: inv.id)),
            trailing: AppTextButton(
              key: Key('personal_proxies.cancel.${inv.id}'),
              label: 'cancel'.i18n,
              onPressed: () => notifier.forgetInvite(inv.id),
            ),
          ),
        const SizedBox(height: defaultSize),
        KeyedSubtree(
          key: const Key('personal_proxies.invite'),
          child: PrimaryButton(
            label: 'invite_another'.i18n,
            onPressed: () => startPersonalProxyInvite(context, ref),
          ),
        ),
        if (view.helpers.isNotEmpty || view.invites.isNotEmpty) ...[
          const SizedBox(height: 8),
          AppTextButton(
            key: const Key('personal_proxies.remove_all'),
            label: 'helpers_remove_all'.i18n,
            onPressed: () => _removeAll(context, notifier),
          ),
        ],
        if (state.error != null) ...[
          const SizedBox(height: defaultSize),
          Text(
            helperErrorText(state.error!),
            key: const Key('helpers.error'),
            style: textTheme.bodyMedium!.copyWith(
              color: Theme.of(context).colorScheme.error,
            ),
          ),
        ],
      ],
    );
  }

  // AppDialog runs onPrimaryPressed 400 ms after the dialog closes, when this
  // tab may be gone and its ref unusable. The notifier is keepAlive.
  Future<void> _removeAll(BuildContext context, HelpersNotifier notifier) =>
      AppDialog.show(
        context: context,
        title: 'helpers_remove_all'.i18n,
        body: 'helpers_remove_all_body'.i18n,
        primaryLabel: 'remove'.i18n,
        onPrimaryPressed: notifier.endAllHelpers,
        secondaryLabel: 'cancel'.i18n,
      );
}

String _hoursLeft(int expiresUnix) {
  final left = expiresUnix - DateTime.now().millisecondsSinceEpoch ~/ 1000;
  return '${left <= 0 ? 0 : (left + 3599) ~/ 3600}';
}

class _ProxyRow extends StatelessWidget {
  const _ProxyRow({required this.index, required this.helper, required this.notifier});

  final int index;
  final HelperPairingView helper;

  /// Held rather than read from a ref: see [PersonalProxiesTab._removeAll].
  final HelpersNotifier notifier;

  @override
  Widget build(BuildContext context) {
    final name = helper.name.isEmpty ? 'helper_unnamed'.i18n : helper.name;
    return AppTile(
      label: name,
      subtitle: Row(
        key: Key(helper.online ? 'helpers.online.$index' : 'helpers.offline.$index'),
        children: [
          Icon(
            Icons.circle,
            size: 10,
            color: helper.online ? context.statusSuccessBgDot : context.textTertiary,
          ),
          const SizedBox(width: 6),
          Text(helper.online ? 'helper_online'.i18n : 'helper_offline'.i18n),
        ],
      ),
      trailing: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          AppTextButton(
            key: Key('personal_proxies.rename.${helper.pairingId}'),
            label: 'rename'.i18n,
            onPressed: () => _rename(context, name),
          ),
          AppTextButton(
            key: Key('personal_proxies.remove.${helper.pairingId}'),
            label: 'remove'.i18n,
            onPressed: () => AppDialog.show(
              context: context,
              title: 'helper_remove_confirm_title'.i18n.fill([name]),
              body: 'helper_remove_confirm_body'.i18n.fill([name]),
              primaryLabel: 'remove'.i18n,
              onPrimaryPressed: () => notifier.endPairing(helper.pairingId),
              secondaryLabel: 'cancel'.i18n,
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _rename(BuildContext context, String current) async {
    final name = await askHelperName(
      context,
      initial: current,
      title: 'rename'.i18n,
      label: 'personal_proxy_nickname_label'.i18n,
    );
    if (name == null) return;
    final r = await notifier.renameHelper(helper.pairingId, name);
    if (!r.ok && context.mounted) {
      context.showSnackBarError(helperErrorText(r.error ?? 'unavailable'));
    }
  }
}
```

`manage_private_server.dart` — in the `data:` branch:

```dart
        final showProxies = ref.watch(personalProxiesTabVisibleProvider);
```

set `DefaultTabController(length: showProxies ? 3 : 2, …)`, add to `tabs`:

```dart
                      if (showProxies)
                        Tab(
                          key: const Key('manage.personal_proxies_tab'),
                          child: Text('personal_proxies_tab'.i18n),
                        ),
```

and to the `TabBarView` children `if (showProxies) const PersonalProxiesTab(),` (imports `helpers_notifier.dart`, `personal_proxies_tab.dart`).

`setting.dart` — delete the `getHelp` enum value, the `helperInvites` watch (line 79), the `if (helperInvites) ...[ … ]` tile block (185-193), the `case _SettingType.getHelp:` (360-362), and the now-unused `helpers_notifier.dart` import.

`router.dart` — delete the `AutoRoute(path: '/get-help', page: GetHelp.page)` entry; delete `lib/features/helpers/get_help.dart`.

`helping_section.dart:60` — change "A volunteer who never opens GetHelp" to "A volunteer who never opens the Personal Proxies tab".

`en.po`:

```
# Manage Private Locations' tab for the user's personal proxies.
msgid "personal_proxies_tab"
msgstr "Personal Proxies"

msgid "invite_another"
msgstr "Invite another"

# Label of the field for the name this device shows for a personal proxy. It stays on this device.
msgid "personal_proxy_nickname_label"
msgstr "Name on this device"
```

Then delete from `en.po` each key no code references any more — check each with `grep -rn "'<key>'" lib test`: `helpers_title`, `helpers_intro`, `helpers_list_title`, `helpers_empty`, `helpers_nudge_second`, `invite_someone`.

- [ ] **Step 4: Regenerate and run**

Run: `make gen && flutter analyze && flutter test test/features/helpers/ test/features/private_server/`
Expected: codegen updates `router.gr.dart` (no `GetHelp`) and `helpers_notifier.g.dart` (`personalProxiesTabVisibleProvider`); analyze clean; tests PASS.

- [ ] **Step 5: Commit**

```bash
git add -A lib/features lib/core/router assets/locales/en.po test/features
git commit -m "helpers: Personal Proxies tab in Manage Private Locations replaces GetHelp"
```

---

### Task 9: App — VPN tab banner "Get a personal proxy"

**Files:**
- Create: `lib/features/helpers/personal_proxy_banner.dart`
- Modify: `lib/core/widgets/pro_banner.dart` (add `proBannerShows`)
- Modify: `lib/features/helpers/provider/helpers_notifier.dart` (add `personalProxyBannerVisible`), regenerate `.g.dart`
- Modify: `lib/features/home/vpn_tab.dart:39`
- Modify: `assets/locales/en.po` (add `personal_proxy_banner_title`, `personal_proxy_banner_body`)
- Test: `test/features/helpers/helpers_notifier_test.dart`, `test/features/home/vpn_tab_test.dart`

**Interfaces:**
- Consumes: `helperInvitesAvailableProvider`, `helpersProvider`, `HelpersNotifier.used`, `startPersonalProxyInvite` (Task 6), `proRenewalProvider`, `isUserProProvider`.
- Produces: `personalProxyBannerVisibleProvider` (bool); `bool proBannerShows(WidgetRef ref)`; `class PersonalProxyBanner extends ConsumerWidget` (key `vpn.personal_proxy_banner`).

- [ ] **Step 1: Write the failing tests**

Append to `test/features/helpers/helpers_notifier_test.dart`:

```dart
  group('personalProxyBannerVisibleProvider', () {
    ProviderContainer withFlag(bool on) {
      final c = ProviderContainer(
        overrides: [
          lanternServiceProvider.overrideWithValue(svc),
          helperInvitesAvailableProvider.overrideWithValue(on),
        ],
      );
      addTearDown(c.dispose);
      return c;
    }

    test('shows for a device that never used invites, without the keystore', () {
      final c = withFlag(true);
      expect(c.read(personalProxyBannerVisibleProvider), isTrue);
      expect(keys.reads, 0);
      expect(svc.calls, isEmpty);
    });

    test('waits for the view on a device that used invites', () async {
      storage.values[HelpersNotifier.usedKey] = true;
      svc.answers = {'unlock': _view(), 'refresh': _view()};
      final c = withFlag(true);
      expect(c.read(personalProxyBannerVisibleProvider), isFalse);
      await c.read(helpersProvider.notifier).refresh();
      expect(c.read(personalProxyBannerVisibleProvider), isTrue);
    });

    test('hides once the device has a personal proxy', () async {
      storage.values[HelpersNotifier.usedKey] = true;
      svc.answers = {
        'unlock': _view(),
        'refresh': _view(helpers: '[{"pairing_id":"p1","name":"Ali"}]'),
      };
      final c = withFlag(true);
      await c.read(helpersProvider.notifier).refresh();
      expect(c.read(personalProxyBannerVisibleProvider), isFalse);
    });

    test('hides without helper invites', () {
      expect(withFlag(false).read(personalProxyBannerVisibleProvider), isFalse);
    });
  });
```

Append to `test/features/home/vpn_tab_test.dart`, inside `main()` (add imports `package:lantern/features/helpers/provider/helpers_notifier.dart`):

```dart
  for (final (name, pro, banner) in [
    ('a Pro user sees the personal proxy banner', true, true),
    ('the Pro upsell wins for a free user', false, false),
  ]) {
    testWidgets(name, (tester) async {
      tester.view.devicePixelRatio = 1;
      tester.view.physicalSize = desktopWindowSize;
      addTearDown(tester.view.resetDevicePixelRatio);
      addTearDown(tester.view.resetPhysicalSize);
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            lanternServiceProvider.overrideWithValue(_FakeLanternService()),
            isUserProProvider.overrideWithValue(pro),
            isUserExpiredProvider.overrideWithValue(false),
            proRenewalProvider.overrideWithValue(ProRenewalInfo.none),
            personalProxyBannerVisibleProvider.overrideWithValue(true),
            serverLocationProvider.overrideWithValue(initialServerLocation()),
            radianceSettingsProvider.overrideWithValue(const RadianceSettingsState()),
            vpnProvider.overrideWithValue(VPNStatus.disconnected),
            macosExtensionProvider.overrideWithValue(
              const MacOSExtensionState(SystemExtensionStatus.activated),
            ),
          ],
          child: ScreenUtilInit(
            designSize: desktopWindowSize,
            child: MaterialApp(
              theme: AppTheme.appTheme(),
              home: const Scaffold(body: VpnTab()),
            ),
          ),
        ),
      );
      await tester.pump();
      expect(
        find.byKey(const Key('vpn.personal_proxy_banner')),
        banner ? findsOneWidget : findsNothing,
      );
      if (banner) {
        expect(find.text('personal_proxy_banner_title'.i18n), findsOneWidget);
        expect(find.text('personal_proxy_banner_body'.i18n), findsOneWidget);
      }
    });
  }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/features/helpers/helpers_notifier_test.dart test/features/home/vpn_tab_test.dart`
Expected: FAIL to compile (`personalProxyBannerVisibleProvider` undefined).

- [ ] **Step 3: Implement**

`helpers_notifier.dart`:

```dart
/// Whether the VPN tab offers "Get a personal proxy": while helper invites
/// are available and this device has no personal proxy. A device that never
/// used helper invites has none, so the keystore is never touched to decide;
/// one that did waits for a loaded view.
@riverpod
bool personalProxyBannerVisible(Ref ref) {
  if (!ref.watch(helperInvitesAvailableProvider)) return false;
  final s = ref.watch(helpersProvider);
  if (!s.viewLoaded) return !ref.read(helpersProvider.notifier).used;
  return s.view.helpers.isEmpty;
}
```

`pro_banner.dart`, top level:

```dart
/// Whether [ProBanner] shows anything: a renewal card, or the upsell for a
/// free user. Other banners in its slot wait while it does.
bool proBannerShows(WidgetRef ref) =>
    ref.watch(proRenewalProvider).state != ProRenewalState.none ||
    !ref.watch(isUserProProvider);
```

Create `lib/features/helpers/personal_proxy_banner.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:hooks_riverpod/hooks_riverpod.dart';
import 'package:lantern/core/common/common.dart';
import 'package:lantern/features/helpers/personal_proxy_invite.dart';
import 'package:lantern/features/helpers/provider/helpers_notifier.dart';

/// The VPN tab's "Get a personal proxy", in the Pro banner's slot.
class PersonalProxyBanner extends ConsumerWidget {
  const PersonalProxyBanner({super.key, this.topMargin = 16});

  final double topMargin;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    if (!ref.watch(personalProxyBannerVisibleProvider)) {
      return const SizedBox.shrink();
    }
    final textTheme = Theme.of(context).textTheme;
    return Padding(
      key: const Key('vpn.personal_proxy_banner'),
      padding: EdgeInsets.only(top: topMargin),
      child: AppCard(
        padding: EdgeInsets.zero,
        child: AppTile(
          icon: AppImagePaths.handshake,
          label: 'personal_proxy_banner_title'.i18n,
          subtitle: Text(
            'personal_proxy_banner_body'.i18n,
            style: textTheme.bodySmall!.copyWith(color: context.textSecondary),
          ),
          trailing: const AppImage(path: AppImagePaths.arrowForward),
          onPressed: () => startPersonalProxyInvite(context, ref),
        ),
      ),
    );
  }
}
```

`vpn_tab.dart:39`, replace `const ProBanner(),` with:

```dart
                  // The Pro upsell wins its slot; the personal proxy banner waits.
                  if (proBannerShows(ref))
                    const ProBanner()
                  else
                    const PersonalProxyBanner(),
```

(import `package:lantern/features/helpers/personal_proxy_banner.dart`; `proBannerShows` arrives through `common.dart`, which re-exports `pro_banner.dart`.)

`en.po`:

```
# VPN tab banner offering to invite a friend abroad as a personal proxy.
msgid "personal_proxy_banner_title"
msgstr "Get a personal proxy"

msgid "personal_proxy_banner_body"
msgstr "A friend abroad can keep you connected."
```

- [ ] **Step 4: Regenerate and run**

Run: `make gen && flutter analyze && flutter test test/features/helpers/helpers_notifier_test.dart test/features/home/ test/core/widgets/pro_banner_test.dart`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib assets/locales/en.po test
git commit -m "home: Get a personal proxy banner on the VPN tab"
```

---

### Task 10: App — "Have an invite link?" on Action Mode, with link normalization

**Files:**
- Modify: `lib/features/action_mode/action_mode.dart:28-39,97-107`
- Modify: `lib/core/utils/deeplink_utils.dart:53-54` (add `normalizeHelperInviteInput`)
- Modify: `lib/features/helpers/help_friend.dart:107` (normalize before `claim`)
- Modify: `assets/locales/en.po` (add `have_invite_link`; delete `help_friend_entry`)
- Test: `test/features/action_mode/action_mode_widgets_test.dart:152-195`, `test/features/helpers/volunteer_screens_test.dart`

**Interfaces:**
- Consumes: `helperAutoStartAllowed` (`lib/core/utils/country_code.dart:28`), `countryCodeProvider`, `isHelperInviteUri`, `helperInviteLink`.
- Produces: `String normalizeHelperInviteInput(String input)`; Action Mode row key `action_mode.invite_link`.

- [ ] **Step 1: Write the failing tests**

In `test/features/action_mode/action_mode_widgets_test.dart`, add `String country = 'US'` to `mount`'s parameters and `countryCodeProvider.overrideWithValue(country),` to its overrides (import `package:lantern/features/home/provider/country_code_notifier.dart`), then replace the three tests in `group('helper invites on the tab', …)` with:

```dart
    testWidgets('"Have an invite link?" shows in an uncensored country', (
      tester,
    ) async {
      await mount(tester, const ActionModeTab(), animated: true);
      await tester.scrollUntilVisible(
        find.byKey(const Key('action_mode.invite_link')),
        200,
        scrollable: find.byType(Scrollable).first,
      );
      expect(find.text('have_invite_link'.i18n), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    });

    for (final country in ['', 'IR', 'CN']) {
      testWidgets('no invite row where sharing may not start ("$country")', (
        tester,
      ) async {
        await mount(tester, const ActionModeTab(), animated: true, country: country);
        expect(find.byKey(const Key('action_mode.invite_link')), findsNothing);
        await tester.pumpWidget(const SizedBox());
      });
    }

    testWidgets("a volunteer sees You're helping", (tester) async {
      await mount(
        tester,
        const ActionModeTab(),
        animated: true,
        helpers: const HelpersState(
          view: HelpersView(
            unlocked: true,
            helping: [HelperPairingView(pairingId: 'p1', name: 'Sara')],
          ),
        ),
      );
      await tester.scrollUntilVisible(
        find.byKey(const Key('helping.today')),
        200,
        scrollable: find.byType(Scrollable).first,
      );
      expect(find.byKey(const Key('helping.today')), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    });
```

In `test/features/helpers/volunteer_screens_test.dart`, inside `group('helper invite deep links', …)` add:

```dart
    test('pasted links normalize like deep links', () {
      for (final u in [
        'https://lantern.io/help/#SECRET',
        'https://lantern.io/help#SECRET',
        '  https://www.lantern.io/help#SECRET\n',
        '${AppBuildInfo.appAuthScheme}://help#SECRET',
      ]) {
        expect(normalizeHelperInviteInput(u), 'https://lantern.io/help/#SECRET', reason: u);
      }
      expect(normalizeHelperInviteInput(' not a link '), 'not a link');
    });
```

and a widget test after `'without a held link, the link field is shown'`:

```dart
  testWidgets('a pasted lantern:// link is claimed in canonical form', (
    tester,
  ) async {
    final helpers = _Helpers(const HelpersState());
    await _pump(tester, helpers, const HelpFriend());
    await tester.enterText(
      find.byKey(const Key('help_friend.link')),
      '${AppBuildInfo.appAuthScheme}://help#AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
    );
    await _claim(tester);
    expect(helpers.claims.single.$1, _link);
  });
```

- [ ] **Step 2: Run them to verify they fail**

Run: `touch app.env && flutter test test/features/action_mode/action_mode_widgets_test.dart test/features/helpers/volunteer_screens_test.dart`
Expected: FAIL (`normalizeHelperInviteInput` undefined; row key not found).

- [ ] **Step 3: Implement**

`deeplink_utils.dart`, after `helperInviteLink`:

```dart
/// A pasted invite link in the canonical form the engine parses, accepting
/// every form a deep link may take. Anything else is passed on trimmed, for
/// the engine to refuse as bad_link.
String normalizeHelperInviteInput(String input) {
  final trimmed = input.trim();
  final uri = Uri.tryParse(trimmed);
  if (uri == null || !isHelperInviteUri(uri)) return trimmed;
  return helperInviteLink(uri);
}
```

`help_friend.dart`, in `submit`: `final r = await notifier.claim(normalizeHelperInviteInput(linkCtl.text), nameCtl.text);` (import `package:lantern/core/utils/deeplink_utils.dart`).

`action_mode.dart`: delete the `helperInvites`, `helping` and `waiting` watches (lines 28-39) and add instead:

```dart
    // A volunteer is someone abroad: the row stays hidden where sharing may
    // not start on its own (unknown or censored country), and in stealth.
    final inviteRow =
        AppBuildInfo.enableHelperInvites &&
        helperAutoStartAllowed(ref.watch(countryCodeProvider));
```

and replace the `if (AppBuildInfo.enableHelperInvites && (helperInvites || helping || waiting)) ...[ … ]` block with:

```dart
                    if (inviteRow) ...[
                      const SizedBox(height: defaultSize),
                      AppCard(
                        padding: EdgeInsets.zero,
                        child: AppTile(
                          tileKey: const Key('action_mode.invite_link'),
                          icon: AppImagePaths.handshake,
                          label: 'have_invite_link'.i18n,
                          trailing: const AppImage(path: AppImagePaths.arrowForward),
                          onPressed: () => appRouter.push(HelpFriend()),
                        ),
                      ),
                    ],
```

(imports `package:lantern/core/utils/country_code.dart`, `package:lantern/features/home/provider/country_code_notifier.dart`; drop `helpers_notifier.dart` if no longer used in the file.)

`en.po`: add

```
# Action Mode row that opens the screen for pasting a friend's invite link.
msgid "have_invite_link"
msgstr "Have an invite link?"
```

and delete `help_friend_entry` (no longer referenced; confirm with `grep -rn "'help_friend_entry'" lib test`).

- [ ] **Step 4: Run tests and analyzer**

Run: `flutter analyze && flutter test test/features/action_mode/ test/features/helpers/ test/core/utils/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib assets/locales/en.po test
git commit -m "action mode: permanent Have an invite link? row"
```

---

### Task 11: App — bump radiance to the merged personal-proxies commit

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `lantern-core/helper_redaction_test.go`

**Interfaces:**
- Consumes: the radiance PR from Tasks 1–3, merged. The controller supplies its merge commit SHA (`gh pr view <radiance PR> --repo getlantern/radiance --json mergeCommit -q .mergeCommit.oid`); the implementer does not pick a commit.
- Produces: the app builds against radiance with `vpn.PersonalProxiesTag`, `GetServerByTag("personal-proxies")` and `personal-proxies` redaction.

- [ ] **Step 1: Write the failing test**

Append to `lantern-core/helper_redaction_test.go` (import `"github.com/getlantern/radiance/vpn"`):

```go
// The app selects radiance's group by its tag (Dart personalProxiesTag), and
// the tag must be redacted like a helper tag.
func TestPersonalProxiesTagMatchesTheAppAndIsRedacted(t *testing.T) {
	assert.Equal(t, "personal-proxies", vpn.PersonalProxiesTag, "keep lib/core/utils/helper_tags.dart in step")

	t.Setenv("RADIANCE_TESTING", "false")
	t.Setenv("RADIANCE_LOG_LEVEL", "info")
	t.Setenv("RADIANCE_DISABLE_STDOUT_LOG", "true")
	path := filepath.Join(t.TempDir(), AppLogFileName)
	logger := rlog.NewLogger(rlog.Config{LogPath: path, Level: "info", Prod: true, DisablePublisher: true})
	logger.Info("Connecting to server", "tag", vpn.PersonalProxiesTag)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "personal-proxies")
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./lantern-core/ -run PersonalProxiesTag`
Expected: FAIL to compile (`undefined: vpn.PersonalProxiesTag`).

- [ ] **Step 3: Bump**

```bash
go get github.com/getlantern/radiance@<MERGE_SHA from the controller>
go mod tidy
git diff go.mod
```

Expected: only the radiance line (and any transitive lines radiance's own `go.mod` moved) change.

- [ ] **Step 4: Run the Go and Flutter suites**

Run: `CGO_ENABLED=1 go test -race -tags with_gvisor,with_quic,with_wireguard,with_utls,with_grpc ./lantern-core/...` then `touch app.env && flutter analyze && flutter test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum lantern-core/helper_redaction_test.go
git commit -m "deps: bump radiance for the personal-proxies group"
```

Then open the app PR with `/create-pr` (title "Personal proxies: server-list UI for helper invites"), describing the spec link, the deviations table and the stealth check.

---

### Task 12: [Human] Rollout

- [ ] Merge the radiance PR (Tasks 1–3) after review; give its merge SHA to the controller for Task 11.
- [ ] Merge the app PR (Tasks 4–11) after review and CI.
- [ ] Derek reviews the UI against Figma `hNlyYToB5TnX9SDBFDYJTq` (nodes 7960-2449, 7960-2465) on a build with the `helper_invites` flag on: the server-list menu, the Private Locations tab's Personal proxies section (online, all offline, none), the VPN tab banner (free vs Pro), the Selected Location row, Manage Private Locations' Personal Proxies tab (Rename, Remove, Cancel, Invite another), and the Action Mode "Have an invite link?" row.
- [ ] Spot-check a stealth build: no menu entry, banner, section, tab or Action Mode row.

---

## Self-review notes

- Spec §1 menu/banner/settings/invite/approval → Tasks 6, 9, 8, 6 (approval unchanged: `showApproveHelperSheet` is reused in the tab). §2 tab rename/section/selection/row label/group/privacy → Tasks 7, 5, 2–3, 1. §3 Manage tab, rename, remove, cancel, invite another, keystore notice, remove all, visibility rule, polling → Tasks 4 and 8. §4 paste row, normalization, country and stealth gating → Task 10. Testing section → tests in every task; stealth → Task 12 spot check plus gating in Tasks 6–10.
- Names used across tasks: `PersonalProxiesTag`/`personalProxiesTag`, `ServerLocationType.personalProxy`, `ServerLocation.personalProxy()`, `ServerLocation.selected()`, `startPersonalProxyInvite`, `PrivateLocationsMenu`, `PersonalProxiesSection`, `PersonalProxiesTab`, `PersonalProxyBanner`, `personalProxiesTabVisibleProvider`, `personalProxyBannerVisibleProvider`, `renameHelper`/`rename_helper`, `normalizeHelperInviteInput`, `proBannerShows`.
