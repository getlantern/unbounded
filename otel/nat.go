package otel

// NATSummary holds the NAT-behavior attributes attached to a NAT telemetry span.
//
// It is derived from the WebRTC ICE candidates the client already gathers while
// establishing a connection, not from a separate STUN probe. FilteringBehavior is
// therefore always "unspecified": ICE does not issue the RFC 5780 CHANGE-REQUEST
// transaction required to measure filtering behavior, so the full-cone /
// address-restricted / port-restricted distinction cannot be determined and NATType
// collapses cone variants into a single "Cone NAT" value.
type NATSummary struct {
	IsNatted          bool
	MappingBehavior   string
	FilteringBehavior string
	PortPreservation  bool
	NATType           string
	ExternalIP        string
}
