package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// deniedPrefixes are special-use ranges that are not reachable public internet destinations, or
// that embed an IPv4 address the egress would then reach on the client's behalf. See the IANA
// IPv4 and IPv6 special-purpose address registries.
var deniedPrefixes = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and broadcast
		"::/96",           // IPv4-compatible (deprecated)
		"::ffff:0:0:0/96", // IPv4-translated (SIIT)
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard-only
		"2001::/23",       // IETF protocol assignments, including Teredo
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4
		"fec0::/10",       // site-local (deprecated)
	} {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

// AllowedDestination reports whether the egress may proxy to addr: a global unicast address
// outside every private and special-use range. The egress is open to anyone who speaks its
// protocol, so without this it is a vantage point onto its own host and network, e.g. cloud
// metadata at 169.254.169.254.
func AllowedDestination(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		// IsGlobalUnicast excludes loopback, link-local, multicast and unspecified.
		return false
	}
	for _, p := range deniedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// NewDestinationDialer returns a dialer that refuses any destination AllowedDestination rejects.
// The check runs in Control, against the address actually being connected after resolution, so a
// hostname cannot pass on one answer and then connect to another.
func NewDestinationDialer() *net.Dialer {
	return &net.Dialer{Control: controlDestination}
}

func controlDestination(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("egress: unparseable destination %q: %w", address, err)
	}
	if !AllowedDestination(ap.Addr()) {
		return fmt.Errorf("egress: destination %v is not a public address", ap.Addr())
	}
	return nil
}

// ResolveAllowed resolves name and returns the first of its answers AllowedDestination accepts,
// so a name whose first answer is private (split-horizon, ULA) still reaches its public one. It is
// an error for a name to have no such answer.
func ResolveAllowed(ctx context.Context, name string) (net.IP, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, a := range ips {
		if dst, ok := netip.AddrFromSlice(a.IP); ok && AllowedDestination(dst) {
			return a.IP, nil
		}
	}
	return nil, fmt.Errorf("egress: %s has no public address among %d answers", name, len(ips))
}

// AllowedResolver is a go-socks5 NameResolver that resolves with ResolveAllowed.
type AllowedResolver struct{}

func (AllowedResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	ip, err := ResolveAllowed(ctx, name)
	return ctx, ip, err
}
