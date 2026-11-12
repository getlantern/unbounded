package common

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestPacketFrameV1RoundTrips(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 1452)
	b := AppendPacketFrame(nil, "WebSocket connection 1234", payload)
	assert.Equal(t, FrameV1, b[0])

	addr, got, err := ParsePacketFrame(b)
	require.NoError(t, err)
	assert.Equal(t, "WebSocket connection 1234", addr)
	assert.Equal(t, payload, got)
}

// Consumers read the JSON frames every egress sent before FrameV1, and still sends to consumers
// that haven't said otherwise.
func TestParsePacketFrameReadsJSON(t *testing.T) {
	b, err := json.Marshal(UnboundedPacket{SourceAddr: "WebSocket connection 1234", Payload: []byte("quic")})
	require.NoError(t, err)
	addr, payload, err := ParsePacketFrame(b)
	require.NoError(t, err)
	assert.Equal(t, "WebSocket connection 1234", addr)
	assert.Equal(t, []byte("quic"), payload)
}

// A field a newer egress adds is skipped, whatever its type and wherever it falls, so new fields
// don't break older consumers.
func TestParsePacketFrameSkipsUnknownFields(t *testing.T) {
	b := []byte{FrameV1}
	b = protowire.AppendTag(b, 9, protowire.VarintType)
	b = protowire.AppendVarint(b, 300)
	b = protowire.AppendTag(b, frameSourceAddr, protowire.BytesType)
	b = protowire.AppendString(b, "addr")
	b = protowire.AppendTag(b, 10, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte("a future field"))
	b = protowire.AppendTag(b, framePayload, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte("quic"))
	b = protowire.AppendTag(b, 11, protowire.Fixed32Type)
	b = protowire.AppendFixed32(b, 7)
	b = protowire.AppendTag(b, 12, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, 7)

	addr, payload, err := ParsePacketFrame(b)
	require.NoError(t, err)
	assert.Equal(t, "addr", addr)
	assert.Equal(t, []byte("quic"), payload)
}

func TestParsePacketFrameRejectsUnknownVersionsAndDamage(t *testing.T) {
	_, _, err := ParsePacketFrame([]byte{0x02, 0x0a, 0x00})
	assert.ErrorIs(t, err, ErrUnknownFrame)

	_, _, err = ParsePacketFrame(nil)
	assert.Error(t, err)

	whole := AppendPacketFrame(nil, "addr", []byte("quic"))
	_, _, err = ParsePacketFrame(whole[:len(whole)-1])
	assert.Error(t, err, "a truncated frame")
}

// A version 1 frame costs a few bytes plus the source address over the packet, where JSON
// base64-encodes the packet.
func TestPacketFrameV1IsSmallerThanJSON(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 1452)
	addr := "WebSocket connection 0b2c6f5e-1a6c-4f63-9d3a-1d8f1f6b2e7a"
	v1 := AppendPacketFrame(nil, addr, payload)
	js, err := json.Marshal(UnboundedPacket{SourceAddr: addr, Payload: payload})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(v1), len(payload)+len(addr)+8)
	t.Logf("1452-byte packet: v1 frame %d bytes, JSON %d", len(v1), len(js))
}

func TestControlFrameRoundTrips(t *testing.T) {
	b := AppendControlFrame(nil, Control{Capabilities: CapFrameV1})
	c, isControl, err := ParseControlFrame(b)
	require.NoError(t, err)
	assert.True(t, isControl)
	assert.Equal(t, CapFrameV1, c.Capabilities)
}

// A control frame's first byte is a QUIC short header's with the fixed bit clear, which no QUIC
// packet of a version without grease has, so an egress that predates control frames drops it.
func TestControlFrameIsNotAQUICPacket(t *testing.T) {
	b := AppendControlFrame(nil, Control{Capabilities: CapFrameV1})
	assert.Zero(t, b[0]&0x80, "long header bit")
	assert.Zero(t, b[0]&0x40, "fixed bit")
}

func TestParseControlFrameLeavesPacketsAlone(t *testing.T) {
	for _, b := range [][]byte{nil, {0x40, 1, 2, 3}, {0xc0, 0, 0, 0, 1}, []byte("{}")} {
		_, isControl, err := ParseControlFrame(b)
		assert.False(t, isControl)
		assert.NoError(t, err)
	}
}

// A control frame of a later version, or with fields this build doesn't know, is still taken
// for a control frame and never passed on as a packet.
func TestParseControlFrameToleratesTheFuture(t *testing.T) {
	later := append(append([]byte(nil), controlMagic...), 0x02, 0xff)
	c, isControl, err := ParseControlFrame(later)
	assert.True(t, isControl)
	assert.NoError(t, err)
	assert.Zero(t, c.Capabilities)

	b := AppendControlFrame(nil, Control{Capabilities: CapFrameV1})
	b = protowire.AppendTag(b, 5, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte("a future field"))
	c, isControl, err = ParseControlFrame(b)
	require.NoError(t, err)
	assert.True(t, isControl)
	assert.Equal(t, CapFrameV1, c.Capabilities)
}

// Frames come from the network, so no input may panic.
func FuzzParseFrames(f *testing.F) {
	f.Add(AppendPacketFrame(nil, "addr", []byte("quic")))
	f.Add(AppendControlFrame(nil, Control{Capabilities: CapFrameV1}))
	f.Add([]byte(`{"SourceAddr":"a","Payload":"cXVpYw=="}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _ = ParsePacketFrame(b)
		_, _, _ = ParseControlFrame(b)
	})
}
