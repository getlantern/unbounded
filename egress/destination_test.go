package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAllowedDestination(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"1.1.1.1", true},
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"::ffff:1.1.1.1", true},

		{"127.0.0.1", false},
		{"::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fd00::1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"255.255.255.255", false},
		{"100.64.0.1", false},
		{"192.0.2.1", false},
		{"198.18.0.1", false},
		{"2001:db8::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:169.254.169.254", false},
		{"64:ff9b::a9fe:a9fe", false},
		{"2002:a9fe:a9fe::1", false},
		{"::a9fe:a9fe", false},
		{"::ffff:0:a9fe:a9fe", false},
		{"fec0::1", false},
		{"fec0::1%lo", false},
		{"64:ff9b::a9fe:a9fe%lo", false},
		{"2001:db8::1%eth0", false},
		{"2606:4700:4700::1111%eth0", false},
	} {
		if got := AllowedDestination(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("AllowedDestination(%v) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// The dialer checks the address it is about to connect, so a hostname resolving to loopback is
// refused just as the literal is, and a zoned address is refused rather than slipping past the
// denylist. Control runs before connect, so each refusal is the policy's, not the network's.
func TestDestinationDialerRefusesNonPublic(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())

	addrs := []string{l.Addr().String()}
	// These need an IPv6 socket, which Go creates before Control runs: localhost may resolve to
	// ::1 first, and Go reports the first family's error. Zone 1 is the first interface, which
	// exists on every platform.
	if l6, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		l6.Close()
		addrs = append(addrs, net.JoinHostPort("localhost", port),
			"[fec0::1%1]:80", "[64:ff9b::a9fe:a9fe%1]:80", "[2606:4700:4700::1111%1]:80")
	} else {
		t.Log("no IPv6 on this host; skipping the localhost and zoned cases")
	}

	d := NewDestinationDialer()
	for _, addr := range addrs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, err := d.DialContext(ctx, "tcp", addr)
		cancel()
		if err == nil {
			c.Close()
			t.Errorf("dialing %v succeeded, want it refused", addr)
		} else if !errors.Is(err, ErrNotPublic) {
			t.Errorf("dialing %v: %v, want the destination policy's refusal", addr, err)
		}
	}
}

func TestResolveAllowed(t *testing.T) {
	answers := func(addrs ...string) func(context.Context, string) ([]net.IPAddr, error) {
		return func(context.Context, string) ([]net.IPAddr, error) {
			var ips []net.IPAddr
			for _, a := range addrs {
				ip, zone, _ := strings.Cut(a, "%")
				ips = append(ips, net.IPAddr{IP: net.ParseIP(ip), Zone: zone})
			}
			return ips, nil
		}
	}
	orig := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = orig })

	for _, tc := range []struct {
		name    string
		answers []string
		want    string // empty for an error
	}{
		{"private first answer skipped", []string{"10.0.0.5", "fd00::1", "198.51.99.7"}, "198.51.99.7"},
		{"IPv4 preferred", []string{"2606:4700:4700::1111", "1.1.1.1"}, "1.1.1.1"},
		{"IPv6 when no IPv4 is allowed", []string{"10.0.0.5", "2606:4700:4700::1111"}, "2606:4700:4700::1111"},
		{"no allowed answer", []string{"127.0.0.1", "::1", "169.254.169.254"}, ""},
		{"zoned answer skipped", []string{"2606:4700:4700::1111%eth0", "2606:4700:4700::1001"}, "2606:4700:4700::1001"},
		{"only zoned answers", []string{"2606:4700:4700::1111%eth0"}, ""},
	} {
		lookupIPAddr = answers(tc.answers...)
		ip, err := ResolveAllowed(context.Background(), "example.test")
		switch {
		case tc.want == "" && !errors.Is(err, ErrNotPublic):
			t.Errorf("%s: ResolveAllowed = %v, %v; want ErrNotPublic", tc.name, ip, err)
		case tc.want != "" && (err != nil || !ip.Equal(net.ParseIP(tc.want))):
			t.Errorf("%s: ResolveAllowed = %v, %v; want %v", tc.name, ip, err, tc.want)
		}
	}
}
