//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"time"

	"github.com/pion/stun/v4"
)

// stunPreflight sends a STUN binding request to each of servers at once, and reports whether any
// answered within timeout. Without an answer, it reports the furthest any server got.
func stunPreflight(ctx context.Context, servers []string, timeout time.Duration) STUNPreflightResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results := make(chan string, len(servers))
	for _, s := range servers {
		go func() { results <- probeSTUN(ctx, s, timeout/2) }()
	}
	outcome := STUNPreflightNoServers
	for range servers {
		r := <-results
		if r == STUNPreflightOK {
			outcome = r
			break
		}
		if preflightRank[r] > preflightRank[outcome] {
			outcome = r
		}
	}
	return STUNPreflightResult{Outcome: outcome, Servers: len(servers), Elapsed: time.Since(start)}
}

// preflightRank orders failures by how far a probe got.
var preflightRank = map[string]int{
	STUNPreflightNoServers: 0,
	STUNPreflightNoSocket:  1,
	STUNPreflightNoReply:   2,
}

// probeSTUN sends a binding request to server, again after resendAfter, and waits for an answer
// until ctx ends. Each probe resolves and dials on its own, so a slow lookup doesn't hold up the
// rest, and the socket matches the server's address family.
func probeSTUN(ctx context.Context, server string, resendAfter time.Duration) string {
	uri, err := stun.ParseURI(server)
	if err != nil || uri.Scheme != stun.SchemeTypeSTUN {
		return STUNPreflightNoServers
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", uri.Host)
	if err != nil || len(ips) == 0 {
		return STUNPreflightNoServers
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ips[0], Port: uri.Port})
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
