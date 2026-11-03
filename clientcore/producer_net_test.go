//go:build !wasm

package clientcore

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common/covertdtls"
)

// countingNet is the host's network stack, counting the UDP sockets pion
// opens on it.
type countingNet struct {
	*stdnet.Net
	listens atomic.Int32
}

func (n *countingNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, laddr)
	if err == nil {
		n.listens.Add(1)
	}
	return c, err
}

func (n *countingNet) ListenPacket(network, address string) (net.PacketConn, error) {
	c, err := n.Net.ListenPacket(network, address)
	if err == nil {
		n.listens.Add(1)
	}
	return c, err
}

// A producer built with no tuning and no covert DTLS, the production
// default, must still open its ICE sockets on the Net it was given rather
// than on pion's default.
func TestProducerOpensSocketsOnGivenNet(t *testing.T) {
	std, err := stdnet.NewNet()
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingNet{Net: std}

	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, counting)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	// Gathering is what opens the sockets; an offer with a data channel
	// starts it without needing a remote peer.
	if _, err := producer.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := producer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(producer)
	if err := producer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(15 * time.Second):
		t.Fatal("gathering never completed")
	}
	if n := counting.listens.Load(); n == 0 {
		t.Fatal("producer opened no sockets on the Net it was given")
	}
}

// nil keeps pion's default stack, as before the option reached the producer.
func TestProducerNilNetBuilds(t *testing.T) {
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	producer.Close()
}
