package common

import (
	"encoding/json"
	"net"

	"github.com/pion/webrtc/v3"
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
	Addr net.IP
	Tag  string
}

func (ci ConsumerInfo) Nil() bool {
	return ci.Addr == nil && ci.Tag == ""
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
	Typ            string             `json:"type"`
	Component      uint16             `json:"component"`
	RelatedAddress string             `json:"relatedAddress"`
	RelatedPort    uint16             `json:"relatedPort"`
	TCPType        string             `json:"tcpType"`
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
			var candidates []ICECandidate
			var webRTCCandidates []webrtc.ICECandidate
			err := json.Unmarshal([]byte(msg.Payload), &candidates)
			if err != nil {
				return msg.ReplyTo, webRTCCandidates, err
			}

			for _, c := range candidates {
				new := webrtc.ICECandidate{
					Foundation:     c.Foundation,
					Priority:       c.Priority,
					Address:        c.Address,
					Protocol:       c.Protocol,
					Port:           c.Port,
					Component:      c.Component,
					RelatedAddress: c.RelatedAddress,
					RelatedPort:    c.RelatedPort,
					TCPType:        c.TCPType,
				}

				iceType, err := webrtc.NewICECandidateType(c.Typ)
				if err != nil {
					Debugf("Failed to get ICE candidate type%v", err)
					continue
				}

				new.Typ = iceType

				webRTCCandidates = append(webRTCCandidates, new)
			}

			return msg.ReplyTo, webRTCCandidates, err
		}
	}

	return "", nil, err
}
