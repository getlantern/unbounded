package common

import (
	"encoding/json"
	// "errors"
	"net"
	"slices"

	"github.com/pion/webrtc/v4"
)

const (
	SignalMsgGenesis SignalMsgType = iota
	SignalMsgOffer
	SignalMsgAnswer
	SignalMsgICE
)

const (
	SubprotocolsHeader      = "Sec-Websocket-Protocol"
	subprotocolsMagicCookie = "un80und3d"
)

type SignalMsgType int

func (t SignalMsgType) String() string {
	switch t {
	case SignalMsgGenesis:
		return "Genesis"
	case SignalMsgOffer:
		return "Offer"
	case SignalMsgAnswer:
		return "Answer"
	case SignalMsgICE:
		return "ICE"
	default:
		return "invalid"
	}
}

// TODO nelson 07/25/2025: JITUnavailable was added as an escape hatch to implement synchronization
// with the JIT egress consumer, as a way to disambiguate a nil path assertion. It's wacky and should
// be cleaned up here: https://github.com/getlantern/engineering/issues/2402
type PathAssertion struct {
	Allow          []Endpoint
	Deny           []Endpoint
	JITUnavailable bool
}

func (pa PathAssertion) Nil() bool {
	return len(pa.Allow) == 0 && len(pa.Deny) == 0
}

// TODO: ConsumerInfo is the downstream router's counterpart to PathAssertion. It's meant to describe
// useful information about a downstream connectivity situation. Like PathAssertion, a Nil()
// ConsumerInfo indicates no connectivity. ConsumerInfo lives here both to keep things consistent
// and because we imagine that ConsumerInfo objects may be served by Freddie, who will also perform
// the required IP geolocation during the discovery and matchmaking process. ConsumerInfo might one
// day evolve to become the consumer-side constraints object which is discussed in the RFCs, at
// which point it might make sense to collapse PathAssertion and ConsumerInfo into a single concept.
type ConsumerInfo struct {
	Addr      net.IP
	Tag       string
	SessionID string
}

func (ci ConsumerInfo) Nil() bool {
	return ci.Addr == nil && ci.Tag == "" && ci.SessionID == ""
}

type Endpoint struct {
	Host     string
	Distance uint
}

type GenesisMsg struct {
	PathAssertion PathAssertion
}

// TODO: We observe that OfferMsg and ConsumerInfo have a special relationship: OfferMsg is how
// consumer data goes in, and ConsumerInfo is how consumer data comes out. A consumer's 'Tag' is
// supplied at offer time, encapsulated in an OfferMsg; later, that Tag is surfaced to the
// producer's UI layer in a ConsumerInfo struct. This suggests that these structures can probably
// be collapsed into a single concept.
type OfferMsg struct {
	SDP webrtc.SessionDescription
	Tag string
}

// NB: in the last segment of our signaling handshake, the consumer sends the producer an ICEMsg,
// which encapsulates the consumer's gathered ICE candidates "a la carte" and the consumer's SessionID.
type ICEMsg struct {
	Candidates        []webrtc.ICECandidate
	ConsumerSessionID string
}

// A little confusing: SignalMsg is actually the parent msg which encapsulates an underlying msg,
// which could be a GenesisMsg, an OfferMsg, a webrtc.SessionDescription (which is currently sent
// unencapsulated as a SignalMsgAnswer), or an ICEMsg, which is sent as a SignalMsgICE.
type SignalMsg struct {
	ReplyTo string
	Type    SignalMsgType
	Payload string
}

// TODO: presently unused, should we just stop supporting old clients and version-enforce them off the network?
/*
type ICECandidate struct {
  statsID        string
  Foundation     string             `json:"foundation"`
  Priority       uint32             `json:"priority"`
  Address        string             `json:"address"`
  Protocol       webrtc.ICEProtocol `json:"protocol"`
  Port           uint16             `json:"port"`
  Typ            int                `json:"type"`
  Component      uint16             `json:"component"`
  RelatedAddress string             `json:"relatedAddress"`
  RelatedPort    uint16             `json:"relatedPort"`
  TCPType        string             `json:"tcpType"`
}

// we did an upgrade of pion/webrtc from 3.2.6 to 3.3.4, however marshaling and unmarshaling goes hand in hand
// and this broke the decoding, because some clients/consumers out there were still on 3.2.6 before pion/webrtc implemented
// encoding.TextMarshaler and encoding.TextUnmarshaler interfaces on ICECandidateType. This method will be a fallback to help unmarshal
// older messages sent by older clients
func fallBackIceCandidatesDecoder(raw []byte) ([]webrtc.ICECandidate, error) {
  var candidates []ICECandidate
  var webRTCCandidates []webrtc.ICECandidate
  err := json.Unmarshal(raw, &candidates)
  if err != nil {
    return webRTCCandidates, err
  }

  for _, c := range candidates {
    new := webrtc.ICECandidate{
      Foundation:     c.Foundation,
      Priority:       c.Priority,
      Address:        c.Address,
      Protocol:       c.Protocol,
      Typ:            webrtc.ICECandidateType(c.Typ),
      Port:           c.Port,
      Component:      c.Component,
      RelatedAddress: c.RelatedAddress,
      RelatedPort:    c.RelatedPort,
      TCPType:        c.TCPType,
    }

    webRTCCandidates = append(webRTCCandidates, new)
  }

  return webRTCCandidates, nil
}
*/

func DecodeSignalMsg(raw []byte) (string, interface{}, error) {
	var err error
	var msg SignalMsg

	err = json.Unmarshal(raw, &msg)

	if err == nil {
		switch msg.Type {
		case SignalMsgGenesis:
			var genesis GenesisMsg
			err = json.Unmarshal([]byte(msg.Payload), &genesis)
			return msg.ReplyTo, genesis, err
		case SignalMsgOffer:
			var offer OfferMsg
			err := json.Unmarshal([]byte(msg.Payload), &offer)
			return msg.ReplyTo, offer, err
		case SignalMsgAnswer:
			var answer webrtc.SessionDescription
			err := json.Unmarshal([]byte(msg.Payload), &answer)
			return msg.ReplyTo, answer, err
		case SignalMsgICE:
			var iceMsg ICEMsg
			err := json.Unmarshal([]byte(msg.Payload), &iceMsg)
			return msg.ReplyTo, iceMsg, err
		}
	}

	return "", nil, err
}

// We need to pass a few different data items from uncensored peer to egress server at WebSocket
// dial time. Unfortunately, we cannot use standard HTTP headers for Wasm build targets, because
// the browser spec disallows arbitrary headers. Our solution is the common trick of abusing the
// Sec-Websocket-Protocols header, which can be populated via a Wasm-friendly part of the
// coder/websocket API, to pass arbitrary data. Note that a server receiving a populated
// Sec-Websocket-Protocols header must reply with a reciprocal header containing some selected
// protocol from the request.
func NewSubprotocolsRequest(csid, version string) []string {
	return []string{subprotocolsMagicCookie, csid, version}
}

// NewSubprotocolsRequestWithCountry is NewSubprotocolsRequest plus an optional
// consumer country, so the egress can attribute a session to the region it is
// actually serving. The egress otherwise only ever sees the *donor's* address:
// the consumer sits behind the donor's WebRTC data channel, so nothing about
// where a session terminates is observable server-side without being told.
//
// Deliberately a separate constructor rather than a new parameter on
// NewSubprotocolsRequest: callers that have no country to offer, or that choose
// not to disclose one, should keep emitting the 3-element form and stay
// byte-identical on the wire to every release before this one.
//
// Privacy note: this travels consumer -> donor -> egress, so a donor forwarding
// it can read it. A donor already learns far more than a country code from ICE
// candidate exchange (the consumer's public IP), so the marginal disclosure to
// the donor is small — but it is not zero, and an empty country is always a
// valid choice. Pass a country only when the consumer has consented to it.
func NewSubprotocolsRequestWithCountry(csid, version, country string) []string {
	if country == "" {
		return NewSubprotocolsRequest(csid, version)
	}
	return []string{subprotocolsMagicCookie, csid, version, country}
}

// ParseSubprotocolsRequest accepts both the 3-element form and the 4-element
// form that carries a consumer country, so a new egress keeps serving old
// donors. Because the field is trailing and optional, an old egress also keeps
// serving new donors only if it tolerates the extra element — it does not (it
// requires exactly 3), so donors must not emit the 4-element form until the
// egress fleet has been upgraded past this commit.
func ParseSubprotocolsRequest(s []string) (csid string, version string, ok bool) {
	csid, version, _, ok = ParseSubprotocolsRequestWithCountry(s)
	return csid, version, ok
}

// SubprotocolsContainMagicCookie reports whether the cookie appears anywhere in s,
// i.e. whether the peer looks like it was trying to speak this protocol at all —
// independent of whether it got position, arity, or anything else right.
//
// Deliberately "anywhere" rather than "in the leading position", even though the
// parser requires it to lead. The two questions are different and only one of them
// is about parsing:
//
//   - can this be parsed as our protocol?  -> the cookie must LEAD (see
//     ParseSubprotocolsRequestWithCountry)
//   - might these values contain a consumer session ID?  -> the cookie appearing
//     ANYWHERE is enough to suspect so
//
// The second question is the one this answers, because it gates what reaches the
// log. A first version checked the leading position for both and so claimed a
// property it did not have: a client that built the list in the wrong order, say
// [csid, cookie, version], fails a leading-position check while carrying a real
// session ID — precisely the value withheld on purpose. Widening to "anywhere" is
// what makes the claim below actually true.
//
// A peer that fails this check cannot have supplied a consumer session ID: it shows
// no sign of following the format that carries one, so its values are safe to record
// in order to identify the caller. A peer that passes may well have a real session
// ID among its values, however garbled the rest, so those stay unlogged.
//
// The false-positive cost is negligible in the other direction: the cookie is a
// nonce-like constant, so unrelated software containing it by coincidence is not a
// realistic concern, and the consequence would only be declining to log values.
func SubprotocolsContainMagicCookie(s []string) bool {
	return slices.Contains(s, subprotocolsMagicCookie)
}

// ParseSubprotocolsRequestWithCountry additionally returns the consumer country
// when the peer supplied one; country is "" for the 3-element form.
func ParseSubprotocolsRequestWithCountry(s []string) (csid, version, country string, ok bool) {
	// Validate the magic cookie rather than only the arity. The previous parser
	// checked length alone, so any 3-element Sec-Websocket-Protocol list parsed
	// as ok and the mismatch surfaced later as a vaguer websocket.Accept failure.
	// The cookie is a single shared constant that has never changed, so nothing
	// that has ever spoken this protocol is affected by rejecting a wrong one.
	if len(s) < 1 || s[0] != subprotocolsMagicCookie {
		return "", "", "", false
	}

	switch len(s) {
	case 3:
		return s[1], s[2], "", true
	case 4:
		return s[1], s[2], normalizeCountry(s[3]), true
	default:
		return "", "", "", false
	}
}

// normalizeCountry bounds an untrusted country to a two-letter ISO-3166-1 alpha-2
// code, uppercased, or returns "" to mean "not supplied".
//
// This value arrives in a client-controlled Sec-Websocket-Protocol element and
// flows into span attributes. Passing it through verbatim would let any peer
// mint arbitrarily long or arbitrarily many distinct attribute values, which is
// a cardinality and payload amplification attack on the tracing backend rather
// than merely bad data. Dropping unparseable values is deliberate: a session
// labelled "not supplied" is a small loss, whereas a session that can label
// itself anything is a liability.
//
// Rejecting rather than truncating, because a truncated garbage value is
// indistinguishable from a real code and would silently pollute the same
// dimension.
func normalizeCountry(country string) string {
	if len(country) != 2 {
		return ""
	}
	out := []byte(country)
	for i, c := range out {
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = c - ('a' - 'A')
		case c >= 'A' && c <= 'Z':
			// already canonical
		default:
			return ""
		}
	}
	return string(out)
}

func NewSubprotocolsResponse() []string {
	return []string{subprotocolsMagicCookie}
}
