# TURN relay for personal proxies (room pairings)

## Problem

A personal proxy pairing connects a consumer (the inviter, usually in a censored country) to its own volunteer (the helper) over WebRTC. When neither side's NAT can be traversed and neither side has a mapped port, the pairing never connects. A shared-pool consumer can wait for another volunteer; a paired consumer has only its helper.

A relay fixes this, but an ordinary relay is a fixed IP that censors block. On 2026-11-12 we tested Cloudflare's managed TURN service (`turn.cloudflare.com`) from residential exits inside China, Iran, Russia, Turkmenistan and Myanmar: every country could allocate a relay over TLS 443 and move 2 MB of QUIC through it to a volunteer that also allocated a Cloudflare relay (median 0.4–3 Mbps through the residential hop; 14.5 Mbps without it). Censors block Cloudflare by hostname, not IP, and `turn.cloudflare.com` was blocked nowhere. It costs $0.05/GB.

## Goal

Room pairings get Cloudflare TURN servers in their ICE configuration, so ICE falls back to a relayed path when no direct path works. Pool pairings are unchanged.

Non-goals: TURN for the shared pool; running our own TURN servers; per-pairing scoped credentials (Cloudflare credentials can't be scoped); changing how ICE chooses a path.

## Design

ICE ranks relay candidates below host and server-reflexive ones, so adding TURN servers never displaces a working direct path; the relay is used only when nothing better connects.

Neither peer knows whether an attempt is a room pairing when it creates its `RTCPeerConnection`: the consumer learns it from the genesis it picks, the producer from the offer that answers its genesis. Both learn it before candidate gathering starts (`SetLocalDescription`), and pion (and browsers) apply ICE servers set with `SetConfiguration` before gathering (pion v4.2.25 `peerconnection.go:654`, `icegatherer.go:128`). So Freddie, which alone knows a genesis was delivered through a room, attaches the TURN servers to the two messages that tell each peer so.

### unbounded

**`common.SignalMsg.ICEServers`** — `[]webrtc.ICEServer` with `json:",omitempty"`. Set only by Freddie, like `Room` and `Pairing`; producers and consumers can't set it on messages they post (Freddie builds every `SignalMsg` it relays from form fields). Old clients ignore it; messages without it are byte-identical to today's.

**`freddie.Freddie.RoomICEServers func(context.Context) []webrtc.ICEServer`** — optional hook, nil by default. Freddie calls it once per genesis that carries at least one verified room ticket, and:

1. stamps the result on each room's `SignalMsg` (the per-room messages already built in `handleSignalPost`), so only consumers in that room receive it;
2. if `DeliverGenesis` reports `roomMatched`, stamps the same result on the offer it writes back to the producer. When a room consumer took the genesis the pool step didn't run, so the offer necessarily came from a room consumer. Freddie decodes the offer `SignalMsg`, sets `ICEServers`, and re-encodes it; if decoding fails it writes the offer unchanged.

A nil hook or an empty result changes nothing. The hook must not block signaling: the lantern-cloud implementation returns cached credentials.

ICE server URLs and credentials are never logged or put on spans.

**`clientcore` consumer** — while collecting genesis candidates, remember each room genesis's `ICEServers` (like `roomPairings`). Pick the genesis *before* `CreateOffer` (today the offer is created first; the pick doesn't depend on it). If the picked genesis carries ICE servers, call `SetConfiguration` with the current configuration's servers plus them, then create the offer. On error, log and continue without TURN.

**`clientcore` producer** — decode the genesis response with a function that also returns the `SignalMsg`, pass its `ICEServers` into state 3, and before `SetRemoteDescription` call `SetConfiguration` the same way.

A shared helper, `addICEServers(pc, servers) error`, does the get-append-set for both, and is a no-op for an empty list. It must work in the wasm build (pion's JS `PeerConnection` has `GetConfiguration`/`SetConfiguration`).

**Relay measurement** — the producer's outcome report gains whether a connected attempt's selected candidate pair used a relay (either side's candidate type is `relay`), sent as a new form field; Freddie adds a `path` label (`relay`, `direct`, `unreported`) to `freddie.producer.outcomes`. Together with `delivery=room` (PR #506) this shows how many room connections TURN carried.

### lantern-cloud

**`cmd/freddie/turn.go`** — a `turnServers` type that implements the hook:

- Reads `turn_key_id` and `turn_key_api_token` from Vault `secret/lantern_cloud/cloudflare_turn` through a `vault.NewLease` (as `country.go` does for front secrets).
- Calls `POST https://rtc.live.cloudflare.com/v1/turn/keys/{id}/credentials/generate-ice-servers` with `{"ttl": 3600}`, and caches the result for 30 minutes, refreshing in the background so the hook never waits on Cloudflare. Before the first fetch succeeds, or if Cloudflare or Vault fail, the hook returns nil (no TURN) and a refresh-outcome metric records it.
- Drops URLs on port 53, as Cloudflare recommends.
- `main.go` sets `f.RoomICEServers` when the Vault secret is readable.

**Vault policy** — add read on `secret/data/lantern_cloud/cloudflare_turn` to `tf/_modules/services/freddie/src/freddie.hcl`. Applying it is a production change for the operator.

## Security and cost

- Credentials are shared by all room pairings for up to an hour. Anyone holding a valid room ticket can extract them and relay through our account until they expire. Exposure is bounded by ticket issuance and $0.05/GB; Cloudflare's dashboard shows usage.
- The relay sees only DTLS-encrypted WebRTC traffic.
- `turn.cloudflare.com` can be blocked by name like any other host. If it is, room pairings fall back to today's behaviour.

## Testing

- `common`: `SignalMsg` round-trips `ICEServers` and omits it when empty.
- `freddie`: a room genesis with the hook set stamps the room consumer's genesis and the producer's offer; a pool genesis, a room genesis that fell through to the pool, and a nil hook stamp nothing.
- `clientcore`: `addICEServers` keeps existing servers and adds new ones; the consumer applies a picked room genesis's servers before creating its offer; the producer applies the offer's before answering.
- End to end in `clientcore`: a producer and consumer whose host and srflx candidates can't reach each other (vnet or filtered UDP), with a local `pion/turn` server returned by the hook, connect through the relay and report `path=relay`.
- `lantern-cloud`: the fetcher against an `httptest` Cloudflare (success, error, port-53 filtering, cache refresh), and the hook returning nil before the first success.

## Rollout

1. unbounded PR (protocol field, Freddie hook, clients, metric). Safe to ship alone: with no hook, nothing changes.
2. lantern-cloud PR (Vault policy + `turn.go`), bumping unbounded. The operator applies the policy, then Freddie deploys.
3. Clients pick up the new unbounded through radiance/lantern releases; until then only Freddie changes.
