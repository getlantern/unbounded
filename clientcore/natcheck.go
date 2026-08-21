//go:build !wasm

// natcheck.go implements an RFC 5780-style NAT mapping-behavior probe. A widget
// can only serve peers over STUN (Unbounded has no TURN), so whether this host's
// NAT preserves a stable public mapping across destinations is the single biggest
// predictor of whether it can hole-punch at all. The probe sends STUN Binding
// requests from ONE local socket to several different STUN servers and compares
// the reflexive addresses they report:
//
//	all identical    -> endpoint-independent mapping (cone); hole-punching viable
//	ports differ     -> endpoint-dependent mapping (symmetric); STUN-only P2P fails
//
// This is independent of pion's ICE gathering (which uses a separate socket per
// STUN server, so its candidate ports say nothing about NAT type).
package clientcore

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pion/stun/v3"
)

type NATMapping int

const (
	NATMappingUnknown NATMapping = iota
	NATMappingEndpointIndependent
	NATMappingEndpointDependent
)

func (m NATMapping) String() string {
	switch m {
	case NATMappingEndpointIndependent:
		return "endpoint-independent"
	case NATMappingEndpointDependent:
		return "endpoint-dependent"
	default:
		return "unknown"
	}
}

// NATCheckResult reports what the mapping probe observed.
type NATCheckResult struct {
	Mapping     NATMapping
	PublicAddrs []string // distinct reflexive addresses observed, "ip:port"
	Samples     int      // number of STUN servers that answered
}

// CheckNATMapping probes NAT mapping behavior using the given STUN servers (raw
// "host:port" or "stun:host:port"). It sends from a single UDP socket so the
// comparison is meaningful. perServerTimeout bounds each individual request; the
// whole probe also honors ctx. The result is best-effort: fewer than two
// answering servers yields NATMappingUnknown.
func CheckNATMapping(ctx context.Context, stunServers []string, perServerTimeout time.Duration) NATCheckResult {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return NATCheckResult{}
	}
	defer conn.Close()

	seen := map[string]struct{}{}
	var addrs []string
	samples := 0
	for _, s := range stunServers {
		if ctx.Err() != nil {
			break
		}
		ip, port, err := stunMappedAddr(ctx, conn, s, perServerTimeout)
		if err != nil {
			continue
		}
		samples++
		a := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		if _, ok := seen[a]; !ok {
			seen[a] = struct{}{}
			addrs = append(addrs, a)
		}
	}

	res := NATCheckResult{PublicAddrs: addrs, Samples: samples}
	switch {
	case samples < 2:
		res.Mapping = NATMappingUnknown
	case len(addrs) == 1:
		res.Mapping = NATMappingEndpointIndependent
	default:
		res.Mapping = NATMappingEndpointDependent
	}
	return res
}

// stunMappedAddr sends one Binding request to server on conn and returns the
// reflexive address the server reports.
func stunMappedAddr(ctx context.Context, conn *net.UDPConn, server string, timeout time.Duration) (net.IP, int, error) {
	server = strings.TrimPrefix(strings.TrimSpace(server), "stun:")
	saddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return nil, 0, err
	}
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.WriteToUDP(msg.Raw, saddr); err != nil {
		return nil, 0, err
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	buf := make([]byte, 1500)
	// Loop past stray datagrams from other servers until one decodes as a valid
	// STUN success carrying an XOR-MAPPED-ADDRESS, or the deadline passes.
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, 0, err
		}
		resp := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
		if resp.Decode() != nil {
			continue
		}
		var xor stun.XORMappedAddress
		if xor.GetFrom(resp) != nil {
			continue
		}
		return xor.IP, xor.Port, nil
	}
}
