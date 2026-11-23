package clientcore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// A consumer reads packets in whichever frame its egress sends, JSON or FrameV1, and drops one it
// can't read rather than failing QUIC's read.
func TestBroflakeConnReadsEitherPacketFrame(t *testing.T) {
	read := make(chan IPCMsg, 4)
	c := BroflakeConn{readChan: read, updateReadDeadline: make(chan time.Time)}

	js, err := json.Marshal(common.UnboundedPacket{SourceAddr: "path-a", Payload: []byte("json packet")})
	require.NoError(t, err)
	read <- IPCMsg{IpcType: ChunkIPC, Data: js}
	read <- IPCMsg{IpcType: ChunkIPC, Data: []byte{0x7f, 1, 2, 3}} // a frame version from the future
	read <- IPCMsg{IpcType: ChunkIPC, Data: common.AppendPacketFrame(nil, "path-b", []byte("binary packet"))}

	p := make([]byte, 1500)
	n, addr, err := c.ReadFrom(p)
	require.NoError(t, err)
	assert.Equal(t, "json packet", string(p[:n]))
	assert.Equal(t, "path-a", addr.String())

	n, addr, err = c.ReadFrom(p)
	require.NoError(t, err, "the unreadable frame is dropped, not returned as an error")
	assert.Equal(t, "binary packet", string(p[:n]))
	assert.Equal(t, "path-b", addr.String())
}
