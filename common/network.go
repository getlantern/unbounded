package common

import (
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"golang.org/x/mod/semver"
)

var (
	VersionHeader = "X-BF-Version"
)

var QUICCfg = quic.Config{
	MaxIncomingStreams:    int64(2 << 16),
	MaxIncomingUniStreams: int64(2 << 16),
	MaxIdleTimeout:        60 * time.Second,
	KeepAlivePeriod:       15 * time.Second,
}

type DebugAddr string

func (a DebugAddr) Network() string {
	return string(a)
}

func (a DebugAddr) String() string {
	return string(a)
}

// A note about cancelDeadlinesOnClose: it's best to set this to true, but depending on the
// integration and proxy mode, flipping it to false can fix bugs or misbehaviors. It seems that
// different proxy stacks have different opinions about read and write cancellation. sing-box in
// particular complains about those cancel calls, and so we introduced this parameterization.
type QUICStreamNetConn struct {
	quic.Stream
	OnClose                func()
	AddrLocal              net.Addr
	AddrRemote             net.Addr
	CancelDeadlinesOnClose bool
}

func (c QUICStreamNetConn) LocalAddr() net.Addr {
	return c.AddrLocal
}

func (c QUICStreamNetConn) RemoteAddr() net.Addr {
	return c.AddrRemote
}

func (c QUICStreamNetConn) Close() error {
	if c.OnClose != nil {
		c.OnClose()
	}

	if c.CancelDeadlinesOnClose {
		c.Stream.CancelWrite(42069)
		c.Stream.CancelRead(42069)
	}

	return c.Stream.Close()
}

func IsPublicAddr(addr net.IP) bool {
	return !addr.IsPrivate() && !addr.IsUnspecified() && !addr.IsLoopback()
}

type UnboundedPacket struct {
	SourceAddr string
	Payload    []byte
}

// Validate the protocol version header. If the header isn't present, you're invalid. NB that a
// "valid" version must match only the *major* version.
func IsValidProtocolVersion(h *http.Header) bool {
	return semver.Major(h.Get(VersionHeader)) == semver.Major(Version)
}
