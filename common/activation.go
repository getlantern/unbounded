package common

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// An activation is one stretch of a widget's proxy mode being switched
// on, from on to off. The widget mints an ID when proxy mode turns on
// and sends it on every egress WebSocket it opens until proxy mode turns
// off, so the egress can count activations rather than WebSockets: one
// widget holds several consumer slots, and each slot opens a new
// WebSocket every time a consumer joins, leaves, or migrates.
//
// The ID travels as its own tagged element rather than a positional one
// because the country before it is optional and a placeholder cannot
// stand in for it: browsers refuse an empty subprotocol, and the egress
// drops empty elements before parsing.
const (
	activationPrefix = "act."
	activationIDLen  = 32 // hex characters, 128 bits
)

// NewActivationID returns a fresh random activation ID, in the only form
// the parser accepts.
func NewActivationID() string {
	b := make([]byte, activationIDLen/2)
	// crypto/rand.Read never returns an error; see its documentation.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewSubprotocolsRequestWithActivation is NewSubprotocolsRequestWithCountry
// plus an optional activation ID, appended as a tagged trailing element.
// An empty or malformed ID is omitted, so the result is then identical
// to NewSubprotocolsRequestWithCountry.
//
// An egress older than v2.3.17 accepts the ID only when there is no
// country: it takes a 4-element list and discards the tagged element as
// an invalid country. It refuses the 5-element list outright, so donors
// must not send an ID until the egress fleet runs a parser that knows
// about it.
func NewSubprotocolsRequestWithActivation(csid, version, country, activationID string) []string {
	req := NewSubprotocolsRequestWithCountry(csid, version, country)
	if id := normalizeActivationID(activationID); id != "" {
		req = append(req, activationPrefix+id)
	}
	return req
}

// ParseSubprotocolsRequestWithActivation accepts the 3-element form and
// up to two optional trailing elements, in this order: a consumer
// country, then a tagged activation ID. Either may be absent. A
// malformed country or ID parses as "" rather than refusing the
// connection, since refusing would take a donor offline over a
// telemetry field. The wrong order or an extra element is refused,
// because nothing emits it.
func ParseSubprotocolsRequestWithActivation(s []string) (csid, version, country, activationID string, ok bool) {
	if len(s) < 3 || s[0] != subprotocolsMagicCookie {
		return "", "", "", "", false
	}

	extra := s[3:]
	if n := len(extra); n > 0 && strings.HasPrefix(extra[n-1], activationPrefix) {
		activationID = normalizeActivationID(strings.TrimPrefix(extra[n-1], activationPrefix))
		extra = extra[:n-1]
	}
	switch len(extra) {
	case 0:
	case 1:
		if strings.HasPrefix(extra[0], activationPrefix) {
			return "", "", "", "", false
		}
		country = normalizeCountry(extra[0])
	default:
		return "", "", "", "", false
	}
	return s[1], s[2], country, activationID, true
}

// normalizeActivationID returns id if it is exactly the form
// NewActivationID produces, and "" otherwise.
//
// The ID arrives in a client-controlled subprotocol element and becomes
// a key in the egress's activation store, so it is bounded the same
// way normalizeCountry bounds the country: rejected rather than
// truncated, and never passed through verbatim.
func normalizeActivationID(id string) string {
	if len(id) != activationIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return id
}
