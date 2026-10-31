//go:build !wasm

package clientcore

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/pion/stun/v4"
)

// stunPreflight sends a STUN binding request to each of servers from one UDP socket, and reports
// whether any answered within timeout.
func stunPreflight(ctx context.Context, servers []string, timeout time.Duration) STUNPreflightResult {
	start := time.Now()
	result := func(outcome string) STUNPreflightResult {
		return STUNPreflightResult{Outcome: outcome, Servers: len(servers), Elapsed: time.Since(start)}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var addrs []*net.UDPAddr
	for _, s := range servers {
		uri, err := stun.ParseURI(s)
		if err != nil || uri.Scheme != stun.SchemeTypeSTUN {
			continue
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", uri.Host)
		if err != nil || len(ips) == 0 {
			continue
		}
		addrs = append(addrs, &net.UDPAddr{IP: ips[0], Port: uri.Port})
	}
	if len(addrs) == 0 {
		return result(STUNPreflightNoServers)
	}

	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return result(STUNPreflightNoSocket)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return result(STUNPreflightNoSocket)
	}
	send := func() bool {
		var sent bool
		for _, a := range addrs {
			if _, err := conn.WriteToUDP(req.Raw, a); err == nil {
				sent = true
			}
		}
		return sent
	}
	if !send() {
		return result(STUNPreflightNoSocket)
	}
	resend := time.AfterFunc(timeout/2, func() { send() })
	defer resend.Stop()

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || (errors.As(err, &ne) && ne.Timeout()) {
				return result(STUNPreflightNoReply)
			}
			// An ICMP unreachable from one server can surface as a read error while others may
			// still answer. Pause so an error that repeats doesn't spin until the timeout.
			select {
			case <-ctx.Done():
				return result(STUNPreflightNoReply)
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if res.Decode() != nil || res.TransactionID != req.TransactionID {
			continue
		}
		var mapped stun.XORMappedAddress
		if mapped.GetFrom(res) == nil {
			return result(STUNPreflightOK)
		}
	}
}
