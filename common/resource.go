package common

import (
	"encoding/json"
	"errors"
	"net"

	"github.com/pion/webrtc/v4"
)

const (
	SignalMsgGenesis SignalMsgType = iota
	SignalMsgOffer
	SignalMsgAnswer
	SignalMsgICE
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

type PathAssertion struct {
	Allow []Endpoint
	Deny  []Endpoint
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
// ConsumerInfo (and its client-specific accomplice, ConsumerInfoIPC) were motivated solely to
// provide UI status information, and it's irksome that they exist for this reason alone.
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

// A little confusing: SignalMsg is actually the parent msg which encapsulates an underlying msg,
// which could be a GenesisMsg, an OfferMsg, a webrtc.SessionDescription (which is currently sent
// unencapsulated as a SignalMsgAnswer), or a slice of webrtc.ICECandidate (which is currently sent
// unencapsulated as a SignalMsgICE)
type SignalMsg struct {
	ReplyTo string
	Type    SignalMsgType
	Payload string
}

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
			var candidates []webrtc.ICECandidate
			var unMarshalTypeErr *json.UnmarshalTypeError
			err := json.Unmarshal([]byte(msg.Payload), &candidates)
			if errors.As(err, &unMarshalTypeErr) {
				candidates, err = fallBackIceCandidatesDecoder([]byte(msg.Payload))
				return msg.ReplyTo, candidates, err
			}
			return msg.ReplyTo, candidates, err
		}
	}

	return "", nil, err
}
