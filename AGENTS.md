# AGENTS.md

Guidance for AI agents working in the Unbounded repo. Read this before making changes.

## What this project is

Unbounded is a censorship-circumvention stack built on **browser-based P2P proxies**.
Volunteers in uncensored regions lend their residential IP addresses to relay traffic
for users in censored regions, forming an ephemeral WebRTC swarm. It is a descendant of
the "flash proxy" / Snowflake lineage, with Turbo Tunnel-style connection-state
persistence across ephemeral peer transports.

Traffic path:
`app -> local proxy (SOCKS5/HTTP) -> censored client -> WebRTC -> volunteer widget -> WebSocket -> egress server -> internet`

### Naming: "broflake" vs "unbounded"

The repo and product are **Unbounded**, but the Go module is
`github.com/getlantern/broflake` (the former name) and the engine/UI still use
`broflake`/`BROFLAKE_*` throughout. Treat the two names as synonyms; do not "fix" one to
the other.

## Two peer roles (important for understanding the code)

The same engine (`clientcore`) runs as one of two `ClientType`s, selected at build time
via ldflags (`cmd/build.sh`) or set on `BroflakeOptions.ClientType`:

- **`desktop`** — the **censored client / consumer**. Runs in the censored region,
  exposes a local SOCKS5 or HTTP proxy (`runLocalProxy` in [cmd/proxy.go](cmd/proxy.go)),
  and consumes connectivity over WebRTC. This is the only role that opens a local proxy.
- **`widget`** — the **uncensored / volunteer peer**. Runs in the uncensored region,
  *produces* connectivity over WebRTC for consumers and relays to the egress server over
  WebSocket. It runs no local proxy — it is a pure relay daemon.

The role switch lives in [clientcore/broflake.go](clientcore/broflake.go) (`NewBroflake`,
two `switch bfOpt.ClientType` blocks). `desktop` builds a producer user-stream + WebRTC
consumer table; `widget` builds a WebRTC producer table + JIT egress-consumer table.

## Module map

| Path         | What it is |
|--------------|------------|
| `clientcore` | The high-level client engine/API. One cross-platform engine that compiles both native (`//go:build !wasm`, pion WebRTC) and to `GOOS=js GOARCH=wasm` (`//go:build wasm`, browser WebRTC). Most logic lives here. |
| `cmd`        | Driver/entrypoints for standalone builds. `client_default_impl.go` (native `main`), `client_wasm_impl.go` (wasm JS bindings), `proxy.go` (local SOCKS5/HTTP proxy for desktop). Build scripts: `build.sh`, `build_web.sh`. |
| `common`     | Shared types/utilities (logging, versioning, covert DTLS, resources). |
| `egress`     | Egress server (exit node). WebSocket ingress, QUIC, connection migration/"freeze", geo, metrics. Entry: `egress/cmd`. |
| `freddie`    | Discovery / signaling / matchmaking server. Entry: `freddie/cmd`. |
| `netstate`   | Network-topology observability. `netstate/d` is `netstated` (state machine + web viz at `GET /`); `netstate/client` is the injected client. |
| `ui`         | Embeddable React web UI (Create React App + rewire) that wraps `widget.wasm`. Also builds the browser extension (`ui/extension`). |
| `examples/private-swarm` | Tutorial: minimal egress + signaling + censored client + browser volunteer. |

## Build / run / test

Go 1.24 (`go.mod`); native binaries build with `-race` by default.

```bash
# Native binaries (output to cmd/dist/bin/)
cd cmd && ./build.sh desktop     # censored client
cd cmd && ./build.sh widget      # uncensored/volunteer peer (headless)

# Browser widget (wasm) -> cmd/dist/public/widget.wasm, copied into ui/public/
cd cmd && ./build_web.sh

# Servers (run from repo root)
PORT=9000 go run ./freddie/cmd/main.go
PORT=8000 go run ./egress/cmd/egress.go

# Full local sandbox in tmux (builds binaries, starts freddie/egress/netstate/peers)
./quickstart.sh local      # or: ui | derek <n> | egress | wt   (./quickstop.sh to tear down)
```

Tests (this is exactly what CI gates publishing on):

```bash
go test -race ./...                        # native test suite
go build -ldflags "-s -w" -o /dev/null ./cmd   # verify the js/wasm target still compiles
```

There is **no `mise` config** and no `yarn test` yet. UI has no unit tests; the wasm
widget has no functional/browser test — a change can compile + pass `go test` and still
ship a broken widget.

## Gotchas that will bite you

- **The wasm build target is easy to break silently.** `clientcore` compiles for both
  native and `GOOS=js GOARCH=wasm`. Code added without the right `//go:build` tags (e.g.
  anything importing native-only packages) breaks the browser widget while native tests
  stay green. Always run the `go build ... ./cmd` wasm check above.
- **Do not run `yarn deploy`.** Merging to `main` is what publishes the widget. `yarn
  deploy` republishes the committed (stale) `ui/public/widget.wasm` and mismatches the
  page JS. See the README warning and `.github/workflows/build-widget-wasm.yml`.
- **Build the UI the way CI does: `CI=true yarn build:web`.** With `CI` set,
  `react-scripts` turns ESLint warnings into errors (GitHub Actions sets it
  automatically), so a plain local build can pass and still fail the publish.
- **Publishing on merge to `main`** (via `build-widget-wasm.yml`) ships *both*
  `widget.wasm` and the embed page to `embed.lantern.io`, which feeds
  unbounded.lantern.io and every third-party embedder. Changes under
  `clientcore/`, `common/`, `cmd/`, `netstate/` (and `ui/`) go to production users.
- **`BROFLAKE_STATS=1`** enables per-second data-plane stats logging (off by default,
  zero cost otherwise). Native standalone builds log at debug level to stderr by design.

## Issue tracking & conventions

Per the user's global instructions: track work in the project's issue tracker (Linear),
not TodoWrite/markdown TODO lists. Never commit or push to `main` without an explicit
request; commit and push freely on non-default branches. Opening PRs requires a request.
Prefer Podman/OCI idioms and use `mise` tasks when a repo adopts them (this repo has not).
