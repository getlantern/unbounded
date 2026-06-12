package clientcore

import (
	"strings"

	"github.com/getlantern/broflake/otel"
	"github.com/pion/webrtc/v4"
)

// NAT behavior vocabulary. Mapping/filtering values and the non-cone NAT types reuse the
// strings go-nats previously emitted so dashboards keep grouping; the three go-nats cone
// variants (full / address-restricted / port-restricted) collapse to a single "Cone NAT"
// because filtering behavior is not observable from ICE (see otel.NATSummary).
const (
	natMappingIndependent = "independent"
	natMappingDependent   = "address-port dependent"
	natBehaviorUnknown    = "unspecified"

	natTypeOpen       = "Open to the Internet"
	natTypeCone       = "Cone NAT"
	natTypeSymmetric  = "Symmetric NAT"
	natTypeUDPBlocked = "UDP blocked by firewall"
	natTypeUnknown    = "Unknown"
)

// summarizeNATFromICE derives a NAT-behavior summary from the local ICE candidates
// gathered during connection establishment, avoiding a redundant standalone STUN
// probe. The mapping behavior is inferred by comparing the server-reflexive (srflx)
// mapped addresses returned by the multiple STUN servers in the cohort: identical
// mapped endpoints across servers indicate endpoint-independent (cone) mapping, while
// differing endpoints indicate address/port-dependent (symmetric) mapping.
//
// Filtering behavior is not observable from ICE — it requires the RFC 5780
// CHANGE-REQUEST transaction, which ICE never issues — so it is always reported as
// unspecified and the cone variants of NATType are not distinguished.
func summarizeNATFromICE(candidates []webrtc.ICECandidate) otel.NATSummary {
	var srflx []webrtc.ICECandidate
	for _, c := range candidates {
		if c.Typ == webrtc.ICECandidateTypeSrflx {
			srflx = append(srflx, c)
		}
	}

	// No server-reflexive candidate means the STUN servers were unreachable (only
	// host candidates gathered), which we treat as UDP being blocked.
	if len(srflx) == 0 {
		return otel.NATSummary{
			MappingBehavior:   natBehaviorUnknown,
			FilteringBehavior: natBehaviorUnknown,
			NATType:           natTypeUDPBlocked,
		}
	}

	// Mapping behavior compares mapped endpoints from the same address family, so
	// classify against whichever family gathered more srflx candidates.
	var v4, v6 []webrtc.ICECandidate
	for _, c := range srflx {
		if strings.Contains(c.Address, ":") {
			v6 = append(v6, c)
		} else {
			v4 = append(v4, c)
		}
	}
	fam := v4
	if len(v6) > len(v4) {
		fam = v6
	}

	sum := otel.NATSummary{
		ExternalIP:        fam[0].Address,
		FilteringBehavior: natBehaviorUnknown,
	}

	for _, c := range fam {
		if c.RelatedAddress != "" && c.Address != c.RelatedAddress {
			sum.IsNatted = true
		}
	}

	if fam[0].RelatedPort != 0 {
		sum.PortPreservation = fam[0].Port == fam[0].RelatedPort
	}

	type endpoint struct {
		addr string
		port uint16
	}
	distinct := make(map[endpoint]struct{}, len(fam))
	for _, c := range fam {
		distinct[endpoint{c.Address, c.Port}] = struct{}{}
	}
	switch {
	case len(fam) < 2:
		// A single srflx candidate can't reveal whether the mapping varies per server.
		sum.MappingBehavior = natBehaviorUnknown
	case len(distinct) == 1:
		sum.MappingBehavior = natMappingIndependent
	default:
		sum.MappingBehavior = natMappingDependent
	}

	switch {
	case !sum.IsNatted:
		sum.NATType = natTypeOpen
	case sum.MappingBehavior == natMappingDependent:
		sum.NATType = natTypeSymmetric
	case sum.MappingBehavior == natMappingIndependent:
		sum.NATType = natTypeCone
	default:
		sum.NATType = natTypeUnknown
	}

	return sum
}
