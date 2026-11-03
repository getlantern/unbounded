//go:build !wasm

package clientcore

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
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

	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, producerNet{net: counting})
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
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, producerNet{})
	if err != nil {
		t.Fatal(err)
	}
	producer.Close()
}

// newTestMux is a UDP mux on all IPv4 interfaces, closed when the test ends.
func newTestMux(t *testing.T) (ice.UDPMux, int) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
	t.Cleanup(func() { mux.Close() })
	return mux, conn.LocalAddr().(*net.UDPAddr).Port
}

// gatheredCandidates gathers pc's candidates for an offer.
func gatheredCandidates(t *testing.T, pc *webrtc.PeerConnection) []string {
	t.Helper()
	if _, err := pc.CreateDataChannel("test", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("gathering never completed")
	}
	var cands []string
	for _, line := range strings.Split(pc.LocalDescription().SDP, "\r\n") {
		if strings.HasPrefix(line, "a=candidate:") {
			cands = append(cands, line)
		}
	}
	return cands
}

// With a UDP mux and its external address, the producer's UDP candidates all
// use the mux's port, and one of them is the external address, as a host
// candidate.
func TestProducerAdvertisesMappedAddressOnMux(t *testing.T) {
	mux, port := newTestMux(t)
	pn := producerNet{mux: mux, externalIP: "203.0.113.9"}
	pc, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, pn)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	cands := gatheredCandidates(t, pc)
	var mapped bool
	for _, c := range cands {
		f := strings.Fields(c)
		if len(f) < 8 || !strings.EqualFold(f[2], "udp") {
			continue
		}
		if f[5] != strconv.Itoa(port) {
			t.Errorf("a UDP candidate isn't on the mux's port %d: %s", port, c)
		}
		if f[4] == "203.0.113.9" && f[7] == "host" {
			mapped = true
		}
	}
	if !mapped {
		t.Fatalf("no host candidate on the external address among %v", cands)
	}
}

func TestReachableCandidates(t *testing.T) {
	host := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: "192.168.1.10"}
	ext := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: "203.0.113.9"}
	srflx := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeSrflx, Address: "198.51.100.1"}
	mux, _ := newTestMux(t)
	mapped := producerNet{mux: mux, externalIP: "203.0.113.9"}
	for _, tc := range []struct {
		cands []webrtc.ICECandidate
		pn    producerNet
		want  bool
	}{
		{[]webrtc.ICECandidate{host}, producerNet{}, false},
		{[]webrtc.ICECandidate{host, srflx}, producerNet{}, true},
		{[]webrtc.ICECandidate{host, ext}, mapped, true},
		{[]webrtc.ICECandidate{host, ext}, producerNet{externalIP: "203.0.113.9"}, false},
		{[]webrtc.ICECandidate{host}, mapped, false},
	} {
		if got := reachableCandidates(tc.cands, tc.pn); got != tc.want {
			t.Errorf("reachableCandidates(%v, mapped=%v) = %v, want %v", tc.cands, tc.pn.mapped(), got, tc.want)
		}
	}
}

// A producer with a mapped address uses no STUN: it takes offers even when
// its STUN list can't be fetched, never runs the STUN check, and gives pion no
// ICE servers, so no candidate is gathered off the mapped port.
func TestProducerWithMappedAddressUsesNoSTUN(t *testing.T) {
	mux, _ := newTestMux(t)
	o := NewDefaultWebRTCOptions()
	var fetched atomic.Bool
	o.STUNBatch = func(uint32) ([]string, error) {
		fetched.Store(true)
		return nil, errors.New("STUN list unavailable")
	}
	o.ICEUDPMux = mux
	o.ICEUDPMuxExternalIP = "203.0.113.9"
	var ran atomic.Bool
	o.OnSTUNPreflight = func(STUNPreflightResult) { ran.Store(true) }

	fsm := NewProducerWebRTC(o, nil)
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	var pc *webrtc.PeerConnection
	if len(out) > 0 {
		pc, _ = out[0].(*webrtc.PeerConnection)
	}
	if pc != nil {
		defer pc.Close()
	}
	if next != 1 || pc == nil {
		t.Fatalf("went to state %d with %v, want 1 and a peer connection", next, out)
	}
	if fetched.Load() {
		t.Error("a producer with a mapped address fetched the STUN list")
	}
	if ran.Load() {
		t.Error("a producer with a mapped address ran the STUN check")
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
		t.Errorf("ICE servers %v, want none", servers)
	}
}
