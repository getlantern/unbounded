//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
)

// stunPreflight sends a STUN binding request to each of servers at once, and reports whether any
// answered within timeout. Without an answer, it reports the furthest any server got. The probes
// resolve and dial on n when it's set, as pion does, so the check tests the path ICE will use.
func stunPreflight(ctx context.Context, n transport.Net, servers []string, timeout time.Duration) STUNPreflightResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p := hostProber()
	if n != nil {
		p = netProber(n)
	}
	outcome := firstOK(len(servers), func(i int) string { return p.probeSTUN(ctx, servers[i]) })
	return STUNPreflightResult{Outcome: outcome, Servers: len(servers), Elapsed: time.Since(start)}
}

// firstOK runs probe(0) to probe(n-1) at once and returns ok as soon as one returns it, or else the
// furthest any got. Probes still running when it returns must end with their ctx.
func firstOK(n int, probe func(i int) string) string {
	results := make(chan string, n)
	for i := range n {
		go func() { results <- probe(i) }()
	}
	outcome := STUNPreflightNoServers
	for range n {
		r := <-results
		if r == STUNPreflightOK {
			return r
		}
		if preflightRank[r] > preflightRank[outcome] {
			outcome = r
		}
	}
	return outcome
}

// preflightRank orders failures by how far a probe got.
var preflightRank = map[string]int{
	STUNPreflightNoServers: 0,
	STUNPreflightNoSocket:  1,
	STUNPreflightNoReply:   2,
}

// prober resolves STUN servers and dials them, on the host's stack or on a transport.Net.
type prober struct {
	dial dialFunc
	// resolve returns at most one address per family for host:port.
	resolve func(ctx context.Context, host string, port int) []*net.UDPAddr
}

func hostProber() prober {
	return prober{
		dial: func(addr *net.UDPAddr) (udpConn, error) { return net.DialUDP("udp", nil, addr) },
		resolve: func(ctx context.Context, host string, port int) []*net.UDPAddr {
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil
			}
			var targets []*net.UDPAddr
			if i := slices.IndexFunc(ips, func(ip net.IP) bool { return ip.To4() != nil }); i >= 0 {
				targets = append(targets, &net.UDPAddr{IP: ips[i], Port: port})
			}
			if i := slices.IndexFunc(ips, func(ip net.IP) bool { return ip.To4() == nil }); i >= 0 {
				targets = append(targets, &net.UDPAddr{IP: ips[i], Port: port})
			}
			return targets
		},
	}
}

// netProber resolves through n as pion does, so a name only n can resolve still passes.
func netProber(n transport.Net) prober {
	return prober{
		dial: func(addr *net.UDPAddr) (udpConn, error) { return n.DialUDP("udp", nil, addr) },
		resolve: func(_ context.Context, host string, port int) []*net.UDPAddr {
			hostPort := net.JoinHostPort(host, strconv.Itoa(port))
			var targets []*net.UDPAddr
			for _, network := range []string{"udp4", "udp6"} {
				if addr, err := n.ResolveUDPAddr(network, hostPort); err == nil && addr != nil {
					targets = append(targets, addr)
				}
			}
			return targets
		},
	}
}

// probeSTUN resolves server and probes its first IPv4 and first IPv6 address at once, as ICE
// gathers from both families. Each server resolves on its own, so a slow lookup doesn't hold up
// the rest.
func (p prober) probeSTUN(ctx context.Context, server string) string {
	uri, err := stun.ParseURI(server)
	if err != nil || uri.Scheme != stun.SchemeTypeSTUN {
		return STUNPreflightNoServers
	}
	targets := p.resolve(ctx, uri.Host, uri.Port)
	return firstOK(len(targets), func(i int) string { return probeAddr(ctx, p.dial, targets[i]) })
}

// probeAddr sends a binding request to addr, again halfway through the time left before ctx's
// deadline, and waits for an answer until ctx ends. Timing the resend from here rather than from
// the start of the preflight still leaves room for it after a slow lookup.
func probeAddr(ctx context.Context, dial dialFunc, addr *net.UDPAddr) string {
	conn, err := dial(addr)
	if err != nil {
		return STUNPreflightNoSocket
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return STUNPreflightNoSocket
	}
	if _, err := conn.Write(req.Raw); err != nil {
		return STUNPreflightNoSocket
	}
	resendAfter := stunPreflightTimeout / 2
	if deadline, ok := ctx.Deadline(); ok {
		resendAfter = time.Until(deadline) / 2
	}
	resend := time.AfterFunc(resendAfter, func() { _, _ = conn.Write(req.Raw) })
	defer resend.Stop()

	buf := make([]byte, 1500)
	for {
		// A read fails when ctx ends and closes conn, or when the server's host refuses the port.
		n, err := conn.Read(buf)
		if err != nil {
			return STUNPreflightNoReply
		}
		res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if res.Decode() != nil || res.TransactionID != req.TransactionID {
			continue
		}
		var mapped stun.XORMappedAddress
		if mapped.GetFrom(res) == nil {
			return STUNPreflightOK
		}
	}
}

// udpConn is what a probe needs of its socket, which net.DialUDP and transport.Net both provide.
type udpConn interface {
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Close() error
}

type dialFunc func(addr *net.UDPAddr) (udpConn, error)
