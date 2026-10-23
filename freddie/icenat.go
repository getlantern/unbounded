package freddie

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/getlantern/broflake/common"
)

// NAT classes for freddie.ice.nat. Freddie relays every peer's ICE candidates (a volunteer's in its
// answer SDP, a consumer's in its ICE message), so it can see how each peer's NAT maps its sockets
// without any client reporting it.
const (
	// natNoSrflx means no STUN server answered, so the peer can only offer host candidates, which a
	// remote peer can't reach.
	natNoSrflx = "no_srflx"
	// natSymmetric means one local socket got different public mappings from different STUN
	// servers. Hole punching fails against such a NAT unless the other side is open, and only a
	// relay helps.
	natSymmetric = "symmetric"
	// natUndetermined means every socket got one mapping, which rules nothing out: pion uses a new
	// socket per STUN server, and browsers drop duplicate candidates.
	natUndetermined = "undetermined"
)

type iceCandidate struct {
	typ      string
	address  string
	port     int
	relAddr  string
	relPort  int
	protocol string
}

type natClass struct {
	nat string
	// baseHidden is true when the peer hid its srflx candidates' local address (browsers report
	// raddr 0.0.0.0 rport 0). Freddie then treats all of them as one socket, which overcounts
	// symmetric NAT on a multihomed host, so the label keeps those apart.
	baseHidden bool
	// portPreserved reports whether every srflx kept its local port. Only meaningful when the base
	// is known.
	portPreserved bool
}

func classifyNAT(cands []iceCandidate) natClass {
	mappings := make(map[string]map[string]bool)
	baseHidden := false
	portPreserved := true
	srflx := 0
	for _, c := range cands {
		if c.typ != "srflx" || !strings.EqualFold(c.protocol, "udp") {
			continue
		}
		srflx++
		base := c.relAddr + ":" + strconv.Itoa(c.relPort)
		if c.relPort == 0 {
			baseHidden = true
			base = "hidden"
		} else if c.port != c.relPort {
			portPreserved = false
		}
		if mappings[base] == nil {
			mappings[base] = make(map[string]bool)
		}
		mappings[base][c.address+":"+strconv.Itoa(c.port)] = true
	}
	if srflx == 0 {
		return natClass{nat: natNoSrflx}
	}
	class := natClass{nat: natUndetermined, baseHidden: baseHidden, portPreserved: portPreserved && !baseHidden}
	for _, m := range mappings {
		if len(m) > 1 {
			class.nat = natSymmetric
		}
	}
	return class
}

// recordICENAT classifies the ICE candidates a signaling message carries: a volunteer's answer
// SDP, or a consumer's ICE message. Other message types carry none.
func (f *Freddie) recordICENAT(ctx context.Context, span trace.Span, msgType common.SignalMsgType, data string) {
	var role string
	var cands []iceCandidate
	var ok bool
	switch msgType {
	case common.SignalMsgAnswer:
		role = "volunteer"
		cands, ok = sdpCandidates(data)
	case common.SignalMsgICE:
		role = "consumer"
		cands, ok = iceMsgCandidates(data)
	default:
		return
	}
	if !ok {
		return
	}
	class := classifyNAT(cands)
	attrs := []attribute.KeyValue{
		attribute.String("role", role),
		attribute.String("nat", class.nat),
		attribute.Bool("base_hidden", class.baseHidden),
		attribute.Bool("port_preserved", class.portPreserved),
	}
	f.iceNAT.Add(ctx, 1, metric.WithAttributes(attrs...))
	span.SetAttributes(
		attribute.String("ice.role", role),
		attribute.String("ice.nat", class.nat),
		attribute.Int("ice.candidates", len(cands)),
	)
}

// sdpCandidates parses the a=candidate lines of a JSON-encoded webrtc.SessionDescription, the form
// a volunteer's answer takes.
func sdpCandidates(data string) ([]iceCandidate, bool) {
	var sd webrtc.SessionDescription
	if err := json.Unmarshal([]byte(data), &sd); err != nil {
		return nil, false
	}
	var cands []iceCandidate
	for _, line := range strings.Split(sd.SDP, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		if c, ok := parseCandidateLine(strings.TrimPrefix(line, "a=")); ok {
			cands = append(cands, c)
		}
	}
	return cands, true
}

// parseCandidateLine parses "candidate:<foundation> <component> <transport> <priority> <address>
// <port> typ <type> [raddr <address> rport <port>] ...".
func parseCandidateLine(line string) (iceCandidate, bool) {
	f := strings.Fields(strings.TrimPrefix(line, "candidate:"))
	if len(f) < 8 || f[6] != "typ" {
		return iceCandidate{}, false
	}
	port, err := strconv.Atoi(f[5])
	if err != nil {
		return iceCandidate{}, false
	}
	c := iceCandidate{protocol: f[2], address: f[4], port: port, typ: f[7]}
	for i := 8; i+1 < len(f); i += 2 {
		switch f[i] {
		case "raddr":
			c.relAddr = f[i+1]
		case "rport":
			c.relPort, _ = strconv.Atoi(f[i+1])
		}
	}
	return c, true
}

// iceMsgCandidates reads a consumer's JSON-encoded common.ICEMsg.
func iceMsgCandidates(data string) ([]iceCandidate, bool) {
	var msg common.ICEMsg
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return nil, false
	}
	cands := make([]iceCandidate, 0, len(msg.Candidates))
	for _, c := range msg.Candidates {
		cands = append(cands, iceCandidate{
			typ:      c.Typ.String(),
			address:  c.Address,
			port:     int(c.Port),
			relAddr:  c.RelatedAddress,
			relPort:  int(c.RelatedPort),
			protocol: c.Protocol.String(),
		})
	}
	return cands, true
}
