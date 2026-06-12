package clientcore

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

func host(addr string, port uint16) webrtc.ICECandidate {
	return webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: addr, Port: port}
}

func srflx(addr string, port uint16, base string, basePort uint16) webrtc.ICECandidate {
	return webrtc.ICECandidate{
		Typ:            webrtc.ICECandidateTypeSrflx,
		Address:        addr,
		Port:           port,
		RelatedAddress: base,
		RelatedPort:    basePort,
	}
}

func TestSummarizeNATFromICE(t *testing.T) {
	tests := []struct {
		name        string
		candidates  []webrtc.ICECandidate
		wantNatted  bool
		wantMapping string
		wantType    string
		wantPortPsv bool
		wantExtIP   string
	}{
		{
			name:        "no srflx means UDP blocked",
			candidates:  []webrtc.ICECandidate{host("192.168.1.5", 50000)},
			wantMapping: natBehaviorUnknown,
			wantType:    natTypeUDPBlocked,
		},
		{
			name: "identical mapped endpoint across servers is cone",
			candidates: []webrtc.ICECandidate{
				host("192.168.1.5", 50000),
				srflx("203.0.113.7", 50000, "192.168.1.5", 50000),
				srflx("203.0.113.7", 50000, "192.168.1.5", 50000),
			},
			wantNatted:  true,
			wantMapping: natMappingIndependent,
			wantType:    natTypeCone,
			wantPortPsv: true,
			wantExtIP:   "203.0.113.7",
		},
		{
			name: "differing mapped ports across servers is symmetric",
			candidates: []webrtc.ICECandidate{
				srflx("203.0.113.7", 50001, "192.168.1.5", 50000),
				srflx("203.0.113.7", 50002, "192.168.1.5", 50000),
			},
			wantNatted:  true,
			wantMapping: natMappingDependent,
			wantType:    natTypeSymmetric,
			wantPortPsv: false,
			wantExtIP:   "203.0.113.7",
		},
		{
			name: "single srflx can't classify mapping",
			candidates: []webrtc.ICECandidate{
				srflx("203.0.113.7", 50000, "192.168.1.5", 50000),
			},
			wantNatted:  true,
			wantMapping: natBehaviorUnknown,
			wantType:    natTypeUnknown,
			wantPortPsv: true,
			wantExtIP:   "203.0.113.7",
		},
		{
			name: "mapped equals base means not natted, open",
			candidates: []webrtc.ICECandidate{
				srflx("203.0.113.7", 50000, "203.0.113.7", 50000),
				srflx("203.0.113.7", 50000, "203.0.113.7", 50000),
			},
			wantNatted:  false,
			wantMapping: natMappingIndependent,
			wantType:    natTypeOpen,
			wantPortPsv: true,
			wantExtIP:   "203.0.113.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeNATFromICE(tt.candidates)
			if got.IsNatted != tt.wantNatted {
				t.Errorf("IsNatted = %v, want %v", got.IsNatted, tt.wantNatted)
			}
			if got.MappingBehavior != tt.wantMapping {
				t.Errorf("MappingBehavior = %q, want %q", got.MappingBehavior, tt.wantMapping)
			}
			if got.NATType != tt.wantType {
				t.Errorf("NATType = %q, want %q", got.NATType, tt.wantType)
			}
			if got.PortPreservation != tt.wantPortPsv {
				t.Errorf("PortPreservation = %v, want %v", got.PortPreservation, tt.wantPortPsv)
			}
			if got.ExternalIP != tt.wantExtIP {
				t.Errorf("ExternalIP = %q, want %q", got.ExternalIP, tt.wantExtIP)
			}
			if got.FilteringBehavior != natBehaviorUnknown {
				t.Errorf("FilteringBehavior = %q, want %q", got.FilteringBehavior, natBehaviorUnknown)
			}
		})
	}
}
