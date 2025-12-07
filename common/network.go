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

type QUICStreamNetConn struct {
	quic.Stream
	OnClose    func()
	AddrLocal  net.Addr
	AddrRemote net.Addr
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

	// TODO: see https://github.com/getlantern/engineering/issues/2854
	go func() {
		<-time.After(1 * time.Second)
		c.Stream.CancelWrite(42069)
		c.Stream.CancelRead(42069)
	}()

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
