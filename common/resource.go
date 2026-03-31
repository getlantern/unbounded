package common

import (
	"encoding/json"
	// "errors"
	"net"

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
func NewSubprotocolsRequest(csid, peerID, version string) []string {
	return []string{subprotocolsMagicCookie, csid, peerID, version}
}

func ParseSubprotocolsRequest(s []string) (csid string, peerID string, version string, ok bool) {
	switch len(s) {
	case 4:
		return s[1], s[2], s[3], true
	case 3:
		// Backwards compat: old clients don't send peerID
		return s[1], "", s[2], true
	default:
		return "", "", "", false
	}
}

func NewSubprotocolsResponse() []string {
	return []string{subprotocolsMagicCookie}
}
