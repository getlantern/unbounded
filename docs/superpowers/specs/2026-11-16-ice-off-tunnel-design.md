# Keep Unbounded's ICE off the VPN tunnel

## Problem

On 2026-11-16 a personal proxy pairing between two macOS nightlies (v10.0.6-4368070, unbounded 232f6c5,
radiance 863a6d9, lantern-box v0.0.231) never connected, and the room's TURN servers didn't rescue it.
Both peers had the Lantern VPN on. Their logs (Freshdesk 186894, the consumer, and 186895, the
volunteer) show two gaps that the earlier bypass work (lantern-box 15d1327, radiance a6aa485 and
#768) left open.

1. **A host candidate on the tunnel's own interface.** pion gathers a host candidate per local
   interface, including the VPN's `utun`. The VPN process's sockets normally skip the tunnel, but a
   socket bound to the tunnel's address sends into it.
   - On the consumer (lantern-box's unbounded outbound), physnet's control function picks the
     interface from the socket's local address, so it pins that socket to `utun` outright. Its ICE
     checks entered the tunnel, sing-box routed them (`clash_mode=manual`) to the helper outbound
     the checks were trying to bring up, and they failed with `unbounded: no helper connected`.
   - On the volunteer (radiance's widget), `rtcOpt.Net` is unset, so pion uses plain sockets. With
     the VPN on, its STUN request on that interface went out through the hysteria2 proxy.
2. **Resolving the TURN host.** pion resolves `turn.cloudflare.com` through the OS resolver. In the
   VPN process that resolver points at the tunnel's DNS server, which the process's own sockets
   can't reach: #768 fixed exactly this for signaling. The consumer's gathering ran into the 8s
   TURN cap (`turnGatherWait`), with no TURN traffic and no matching DNS query in sing-box's log.
   pion doesn't log a failed lookup, so this is likely but unconfirmed.

STUN servers are IP literals, so STUN skips the lookup. That's why STUN appears to work while TURN
never does.

## Goals

- No ICE socket of either Unbounded role (lantern-box's consumer outbound, radiance's widget) sends
  into the VPN's own tunnel while the VPN runs.
- TURN and STUN hostnames resolve without the tunnel's DNS.
- The candidates each attempt gathered are visible in logs, by type, with no addresses.

Not goals: pool behaviour, Freddie, the browser widget (it has no tunnel), and Android or Windows
specifics beyond what the shared code gives them. Each change keeps today's behaviour when no VPN
runs.

## Design

### lantern-box: a protected `transport.Net`

`protocol/unbounded/net.go`'s `rtcNet` gains two things.

- **Interfaces.** `Interfaces()` (and `InterfaceByIndex` / `InterfaceByName`, which pion also uses)
  omit the interfaces the VPN registered as its own: the names in
  `NetworkManager.InterfaceMonitor().MyInterfaces()`, read on each call, since the tunnel can restart
  under a new name. With no NetworkManager or monitor on ctx (tests, a bare sing-box), nothing is
  omitted. pion then never gathers a candidate on the tunnel, so physnet never binds a socket to it.
- **Lookups.** `ResolveIPAddr`, `ResolveUDPAddr` and `ResolveTCPAddr` resolve a hostname through
  sing-box's `adapter.DNSRouter.Lookup` with `DNSQueryOptions.Transport` set to the
  `DNSTransportManager`'s default transport. A set transport skips the DNS rules, and so the
  fake-IP rule; radiance's default (`dns_local`) is DoH, which sing-box dials around the tunnel. If
  no router is on ctx (no VPN), or the address is already a literal, it falls back to `stdnet` as
  today. sing-box refuses a fake-IP default server at startup, so the fake-IP check on the default is
  only defensive. Lookups are bounded by the caller's timeout or 5s.
  - **Known limitation:** this keeps a lookup off the tunnel only if the box's default server is
    dialed around it. A remote server (DoH, DoT, UDP or TCP to an address) is dialed by sing-box's
    protected dialer. A box whose default is the `local` (system) resolver is not covered. radiance
    doesn't configure one.

The protected Net is exported from lantern-box as `unbounded.NewProtectedNet(ctx context.Context)
(transport.Net, error)`, which `NewOutbound` uses in place of its own construction for the consumer.
The constructor takes the dialer it needs from ctx, so radiance can call it without a sing-box
outbound. The volunteer service (`volunteer.go`) runs on servers with no tunnel and is left as is.

### radiance: the widget uses it

`unbounded/unbounded.go`'s widget sets `rtcOpt.Net` to a `tunnelNet`, which looks up the running
tunnel's sing-box context on every call. While a tunnel runs, each call goes to
`unbounded.NewProtectedNet(boxCtx)`, built once per tunnel. With no tunnel, each call goes to a
fresh host-stack Net, as pion uses today. The widget doesn't restart when the VPN connects or
disconnects: new sockets follow the tunnel, and an attempt already under way keeps the sockets it
opened until it ends.

### broflake: candidate counts

The consumer's and producer's `Local candidates` debug lines, now `REDACTED`, become counts by
candidate type, for example `host=2 srflx=1 prflx=0 relay=1`. Every type is listed even at zero. No
address, port or hostname is logged.

## Error handling

- A failed lookup returns the error to pion, which drops that ICE server, as it does today.
- An interface listing that fails returns the error, as `stdnet` does.
- Nothing this change adds logs an IP, a hostname from an ICE server URL, a key or a credential.
  sing-box's DNS router logs each lookup it serves, including these, as it does every app's;
  radiance's log handler redacts domains and addresses in sing-box's lines.

## Testing

- **lantern-box:** unit tests with a fake NetworkManager and interface monitor: the registered
  tunnel interface is omitted, others kept, nothing omitted without a monitor. Lookups go through a
  fake DNSRouter with the default transport, fall back without one, and pass literals through.
- **radiance:** unit tests that `tunnelNet` uses the protected Net while a tunnel runs, a new one per
  tunnel, and the host stack with none, and that the widget gets a `tunnelNet` through the existing
  `newWidget` seam.
- **broflake:** a unit test for the count formatting from a fixed candidate list.
- **End to end:** two macOS machines on nightlies with these changes, both VPNs on, the consumer
  in manual mode on the personal proxy. The room pairing connects, and the logs show a relay
  candidate on the consumer. The repeat with the VPN off still connects.

## Rollout

Three PRs, landing in order: broflake (counts, no behaviour change), lantern-box, then radiance
bumping lantern-box. Each reverts on its own.
