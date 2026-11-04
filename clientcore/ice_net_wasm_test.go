//go:build wasm

package clientcore

import (
	"net"
	"testing"
)

// stubMux stands in for an ice.UDPMux; the browser never uses one.
type stubMux struct{}

func (stubMux) Close() error                                     { return nil }
func (stubMux) GetConn(string, net.Addr) (net.PacketConn, error) { return nil, nil }
func (stubMux) RemoveConnByUfrag(string)                         {}
func (stubMux) GetListenAddresses() []net.Addr                   { return nil }

// The browser ignores the mapped-port options, so a browser producer given
// them still uses STUN as usual.
func TestProducerNetOfIgnoresMappedOptionsInTheBrowser(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.ICEUDPMux = stubMux{}
	o.ICEUDPMuxExternalIP = "203.0.113.9"
	if pn := iceNetOf(o); pn.mapped() {
		t.Fatal("a browser producer treated mapped-port options as mapped, so it would skip STUN")
	}
}
