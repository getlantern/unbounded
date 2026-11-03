//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
)

// stunPreflight sends a STUN binding request to each of servers at once, and reports whether any
// answered within timeout. Without an answer, it reports the furthest any server got. The probes
// open their sockets on n when it's set, as pion's do, so the check tests the path ICE will use.
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

// prober resolves STUN servers and opens sockets to probe them, on the host's stack or on a
// transport.Net.
type prober struct {
	listen listenFunc
	// resolve returns, for each address family, a func that resolves host:port in that family and
	// returns nil when it can't. probeSTUN runs them at once.
	resolve func(host string, port int) []func(ctx context.Context) *net.UDPAddr
}

func hostProber() prober {
	return prober{
		listen: func(network string) (udpConn, error) { return net.ListenUDP(network, &net.UDPAddr{}) },
		resolve: func(host string, port int) []func(ctx context.Context) *net.UDPAddr {
			family := func(ipNetwork string) func(ctx context.Context) *net.UDPAddr {
				return func(ctx context.Context) *net.UDPAddr {
					ips, err := net.DefaultResolver.LookupIP(ctx, ipNetwork, host)
					if err != nil || len(ips) == 0 {
						return nil
					}
					return &net.UDPAddr{IP: ips[0], Port: port}
				}
			}
			return []func(ctx context.Context) *net.UDPAddr{family("ip4"), family("ip6")}
		},
	}
}

// netProber opens its sockets on n, as pion's are. STUN names still resolve with the host's
// resolver: transport.Net's resolver takes no context, so an abandoned lookup could never be
// cancelled.
func netProber(n transport.Net) prober {
	p := hostProber()
	p.listen = func(network string) (udpConn, error) { return n.ListenUDP(network, &net.UDPAddr{}) }
	return p
}

// probeSTUN resolves server in each address family and probes what it finds, every family at
// once, as ICE gathers from both. Each server and family resolves on its own, so a slow lookup
// doesn't hold up the rest.
func (p prober) probeSTUN(ctx context.Context, server string) string {
	uri, err := stun.ParseURI(server)
	if err != nil || uri.Scheme != stun.SchemeTypeSTUN {
		return STUNPreflightNoServers
	}
	families := p.resolve(uri.Host, uri.Port)
	return firstOK(len(families), func(i int) string {
		addr := families[i](ctx)
		if addr == nil {
			return STUNPreflightNoServers
		}
		return probeAddr(ctx, p.listen, addr)
	})
}

// probeAddr sends a binding request to addr, again halfway through the time left before ctx's
// deadline, and waits for an answer until ctx ends. Timing the resend from here rather than from
// the start of the preflight still leaves room for it after a slow lookup.
//
// Like ICE's server-reflexive gathering, it listens on an unspecified address in addr's family
// and writes to addr, rather than dialing it.
func probeAddr(ctx context.Context, listen listenFunc, addr *net.UDPAddr) string {
	network := "udp6"
	if addr.IP.To4() != nil {
		network = "udp4"
	}
	conn := openSocket(ctx, listen, network)
	if conn == nil {
		return STUNPreflightNoSocket
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return STUNPreflightNoSocket
	}
	if _, err := conn.WriteTo(req.Raw, addr); err != nil {
		return STUNPreflightNoSocket
	}
	resendAfter := stunPreflightTimeout / 2
	if deadline, ok := ctx.Deadline(); ok {
		resendAfter = time.Until(deadline) / 2
	}
	resend := time.AfterFunc(resendAfter, func() { _, _ = conn.WriteTo(req.Raw, addr) })
	defer resend.Stop()

	buf := make([]byte, 1500)
	for {
		// A read fails when ctx ends and closes conn.
		n, _, err := conn.ReadFrom(buf)
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

// openSocket listens on network, or returns nil if it can't before ctx ends. A transport.Net's
// ListenUDP takes no context, so it runs aside, and a socket it opens after ctx ends is closed.
func openSocket(ctx context.Context, listen listenFunc, network string) udpConn {
	type opened struct {
		conn udpConn
		err  error
	}
	ch := make(chan opened, 1)
	go func() {
		conn, err := listen(network)
		ch <- opened{conn, err}
	}()
	select {
	case o := <-ch:
		if o.err != nil {
			return nil
		}
		return o.conn
	case <-ctx.Done():
		go func() {
			if o := <-ch; o.err == nil {
				o.conn.Close()
			}
		}()
		return nil
	}
}

// udpConn is what a probe needs of its socket, which net.ListenUDP and transport.Net both provide.
type udpConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	Close() error
}

type listenFunc func(network string) (udpConn, error)
