package egress

import (
	"context"
	"net"
	"net/netip"
	"testing"
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
	} {
		if got := AllowedDestination(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("AllowedDestination(%v) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// The dialer checks the address it is about to connect, so a hostname resolving to loopback is
// refused just as the literal is.
func TestDestinationDialerRefusesLoopback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())

	d := NewDestinationDialer()
	for _, addr := range []string{l.Addr().String(), net.JoinHostPort("localhost", port)} {
		if c, err := d.DialContext(context.Background(), "tcp", addr); err == nil {
			c.Close()
			t.Errorf("dialing %v succeeded, want it refused", addr)
		}
	}
}

func TestResolveAllowed(t *testing.T) {
	answers := func(addrs ...string) func(context.Context, string) ([]net.IPAddr, error) {
		return func(context.Context, string) ([]net.IPAddr, error) {
			var ips []net.IPAddr
			for _, a := range addrs {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(a)})
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
	} {
		lookupIPAddr = answers(tc.answers...)
		ip, err := ResolveAllowed(context.Background(), "example.test")
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("%s: ResolveAllowed = %v, want an error", tc.name, ip)
		case tc.want != "" && (err != nil || !ip.Equal(net.ParseIP(tc.want))):
			t.Errorf("%s: ResolveAllowed = %v, %v; want %v", tc.name, ip, err, tc.want)
		}
	}
}
