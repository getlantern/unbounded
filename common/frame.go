package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// The egress sends each QUIC packet to a consumer in a frame that also names the WebSocket it
// came over, which the consumer reports as the packet's source address so QUIC can tell paths
// apart. A frame is one WebSocket or data channel message, and a volunteer relays it unread.
//
// The first frames were JSON, which base64-encodes the packet and adds about a third to every
// byte on the path. Frame version 1 is binary: the byte FrameV1, then a protobuf message whose
// fields are listed below. A reader skips fields it doesn't know, so fields can be added without
// breaking older readers. A change that older readers can't skip takes a new version byte, sent
// only to consumers that say they read it.
//
// A consumer says which frames it reads in a control frame sent the other way, toward the egress:
// controlMagic, a version byte, then a protobuf message of control fields. The egress keeps
// sending JSON until it hears one, so consumers that predate frames keep working. An egress that
// predates them hands the control frame to QUIC, which drops it: it begins like a short header
// packet with the fixed bit clear, which QUIC rejects. Were QUIC bit greasing (RFC 9287) ever
// negotiated, a real packet could clear that bit too, so the magic is long enough that one
// beginning with it is as unlikely as guessing seven random bytes.

// FrameV1 begins a version 1 packet frame. A JSON frame begins with '{'.
const FrameV1 byte = 0x01

// Packet frame fields.
const (
	framePayload    protowire.Number = 1 // bytes: the QUIC packet
	frameSourceAddr protowire.Number = 2 // string: the source address
)

// controlMagic begins a control frame.
var controlMagic = []byte{0x00, 'U', 'B', 'F', 'C', 'T', 'R', 'L'}

// controlV1 is the control frame version that follows controlMagic.
const controlV1 byte = 0x01

// Control frame fields.
const (
	controlCapabilities protowire.Number = 1 // varint: Capability bits
)

// Capability is a bit in a consumer's control frame.
type Capability uint64

const (
	// CapFrameV1 says the consumer reads FrameV1 packet frames.
	CapFrameV1 Capability = 1 << iota
)

// Control is what a consumer tells the egress in a control frame.
type Control struct {
	Capabilities Capability
}

// ErrUnknownFrame is returned for a packet frame of a version this build doesn't read.
var ErrUnknownFrame = errors.New("unknown packet frame version")

// AppendPacketFrame appends a FrameV1 frame of payload from sourceAddr to dst.
func AppendPacketFrame(dst []byte, sourceAddr string, payload []byte) []byte {
	dst = append(dst, FrameV1)
	dst = protowire.AppendTag(dst, frameSourceAddr, protowire.BytesType)
	dst = protowire.AppendString(dst, sourceAddr)
	dst = protowire.AppendTag(dst, framePayload, protowire.BytesType)
	return protowire.AppendBytes(dst, payload)
}

// ParsePacketFrame returns the source address and payload of a packet frame of either version.
// payload aliases b for a FrameV1 frame.
func ParsePacketFrame(b []byte) (sourceAddr string, payload []byte, err error) {
	if len(b) == 0 {
		return "", nil, errors.New("empty packet frame")
	}
	switch b[0] {
	case '{':
		var p UnboundedPacket
		if err := json.Unmarshal(b, &p); err != nil {
			return "", nil, fmt.Errorf("json packet frame: %w", err)
		}
		return p.SourceAddr, p.Payload, nil
	case FrameV1:
		return parseFrameV1(b[1:])
	default:
		return "", nil, fmt.Errorf("%w: %#x", ErrUnknownFrame, b[0])
	}
}

func parseFrameV1(b []byte) (sourceAddr string, payload []byte, err error) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", nil, fmt.Errorf("packet frame: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch {
		case num == framePayload && typ == protowire.BytesType:
			payload, n = protowire.ConsumeBytes(b)
		case num == frameSourceAddr && typ == protowire.BytesType:
			var s []byte
			s, n = protowire.ConsumeBytes(b)
			sourceAddr = string(s)
		default:
			// A field this build doesn't know, from a newer egress.
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return "", nil, fmt.Errorf("packet frame field %d: %w", num, protowire.ParseError(n))
		}
		b = b[n:]
	}
	return sourceAddr, payload, nil
}

// AppendControlFrame appends a control frame saying c to dst.
func AppendControlFrame(dst []byte, c Control) []byte {
	dst = append(dst, controlMagic...)
	dst = append(dst, controlV1)
	dst = protowire.AppendTag(dst, controlCapabilities, protowire.VarintType)
	return protowire.AppendVarint(dst, uint64(c.Capabilities))
}

// ParseControlFrame reports whether b is a control frame and, if it is one this build reads,
// what it says. A control frame of a later version is still recognized, so it is never mistaken
// for a QUIC packet, but says nothing.
func ParseControlFrame(b []byte) (c Control, isControl bool, err error) {
	if !bytes.HasPrefix(b, controlMagic) {
		return Control{}, false, nil
	}
	b = b[len(controlMagic):]
	if len(b) == 0 || b[0] != controlV1 {
		return Control{}, true, nil
	}
	b = b[1:]
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return Control{}, true, fmt.Errorf("control frame: %w", protowire.ParseError(n))
		}
		b = b[n:]
		if num == controlCapabilities && typ == protowire.VarintType {
			var v uint64
			v, n = protowire.ConsumeVarint(b)
			c.Capabilities = Capability(v)
		} else {
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return Control{}, true, fmt.Errorf("control frame field %d: %w", num, protowire.ParseError(n))
		}
		b = b[n:]
	}
	return c, true, nil
}
