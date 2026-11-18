//go:build !wasm

package clientcore

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newVNetICE builds a virtual internet: a STUN and TURN server at 1.2.3.1, and a consumer and a
// producer, each on its own LAN behind a NAT that maps and filters as behavior says.
// EndpointIndependent NATs let a hole be punched. EndpointAddrPortDependent ones are symmetric,
// so only a relay connects them.
func newVNetICE(t *testing.T, behavior vnet.EndpointDependencyType) (consumer, producer *vnet.Net, stunURL string, turnServer webrtc.ICEServer) {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "1.2.3.0/24", LoggerFactory: lf})
	require.NoError(t, err)

	srvNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"1.2.3.1"}})
	require.NoError(t, err)
	require.NoError(t, wan.AddNet(srvNet))

	lan := func(cidr, public, local string) *vnet.Net {
		r, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR:          cidr,
			StaticIPs:     []string{public},
			NATType:       &vnet.NATType{MappingBehavior: behavior, FilteringBehavior: behavior},
			LoggerFactory: lf,
		})
		require.NoError(t, err)
		require.NoError(t, wan.AddRouter(r))
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{local}})
		require.NoError(t, err)
		require.NoError(t, r.AddNet(n))
		return n
	}
	consumer = lan("10.0.1.0/24", "1.2.3.10", "10.0.1.2")
	producer = lan("10.0.2.0/24", "1.2.3.20", "10.0.2.2")
	require.NoError(t, wan.Start())
	t.Cleanup(func() { _ = wan.Stop() })

	pc, err := srvNet.ListenPacket("udp4", "1.2.3.1:3478")
	require.NoError(t, err)
	key := turn.GenerateAuthKey("u", "test", "p")
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: "test",
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == "u"
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("1.2.3.1"),
				Address:      "1.2.3.1",
				Net:          srvNet,
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	return consumer, producer, "stun:1.2.3.1:3478",
		webrtc.ICEServer{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"}
}

// connectICE connects a consumer's and a producer's ICE sessions as the FSMs do: credentials and
// candidates swapped, then dial and accept at once.
func connectICE(t *testing.T, consumerNet, producerNet transport.Net, servers []webrtc.ICEServer, tuning WebRTCTuning) (consumer, producer *iceSession) {
	t.Helper()
	newSession := func(n transport.Net, controlling bool) *iceSession {
		o := NewDefaultWebRTCOptions()
		o.Net = n
		o.Tuning = tuning
		s, err := newICESession(o, controlling, servers, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	consumer, producer = newSession(consumerNet, true), newSession(producerNet, false)
	cu, cp, err := consumer.credentials()
	require.NoError(t, err)
	pu, pp, err := producer.credentials()
	require.NoError(t, err)
	pCands, _, err := producer.gather(0)
	require.NoError(t, err)
	cCands, _, err := consumer.gather(0)
	require.NoError(t, err)
	require.NoError(t, consumer.addRemote(pCands))
	require.NoError(t, producer.addRemote(cCands))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	accepted := make(chan error, 1)
	go func() { accepted <- producer.connect(ctx, cu, cp) }()
	require.NoError(t, consumer.connect(ctx, pu, pp))
	require.NoError(t, <-accepted)
	return consumer, producer
}

// exchange checks that a full-size datagram crosses from a to b intact.
func exchange(t *testing.T, a, b net.PacketConn) {
	t.Helper()
	msg := bytes.Repeat([]byte{0xab}, 1200)
	_, err := a.WriteTo(msg, nil)
	require.NoError(t, err)
	require.NoError(t, b.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 1500)
	n, from, err := b.ReadFrom(buf)
	require.NoError(t, err)
	require.NotNil(t, from, "a QUIC endpoint keys connections by the address ReadFrom returns")
	assert.Equal(t, msg, buf[:n])
}

func TestICESessionPunched(t *testing.T) {
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, WebRTCTuning{})
	assert.False(t, consumer.relayed())
	exchange(t, consumer.packetConn(), producer.packetConn())
	exchange(t, producer.packetConn(), consumer.packetConn())
}

func TestICESessionRelayed(t *testing.T) {
	cn, pn, stunURL, turnServer := newVNetICE(t, vnet.EndpointAddrPortDependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}, turnServer}, WebRTCTuning{})
	assert.True(t, consumer.relayed())
	exchange(t, consumer.packetConn(), producer.packetConn())
	exchange(t, producer.packetConn(), consumer.packetConn())
}

// When one side goes away, the other's session ends, so its FSM moves on.
func TestICESessionEndsWhenPeerCloses(t *testing.T) {
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	fast := WebRTCTuning{ICEDisconnectedTimeout: time.Second, ICEFailedTimeout: 2 * time.Second, ICEKeepaliveInterval: 200 * time.Millisecond}
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, fast)
	require.NoError(t, producer.Close())
	select {
	case <-producer.done():
	case <-time.After(time.Second):
		t.Fatal("closing a session didn't end it")
	}
	select {
	case <-consumer.done():
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer's session outlived the producer's")
	}
	_, _, err := consumer.packetConn().ReadFrom(make([]byte, 1500))
	assert.Error(t, err, "reads fail once the session ends")
}

func TestSummarizeICECandidates(t *testing.T) {
	host := "1 1 udp 2130706431 10.0.0.2 5000 typ host"
	srflx := "2 1 udp 1694498815 8.8.8.8 6000 typ srflx raddr 10.0.0.2 rport 5000"
	nonHost, public := summarizeICECandidates([]string{host})
	assert.False(t, nonHost)
	assert.Nil(t, public)
	nonHost, public = summarizeICECandidates([]string{host, srflx, "not a candidate"})
	assert.True(t, nonHost)
	assert.Equal(t, "8.8.8.8", public.String())
}

// A candidate that can't be parsed is refused with errBadCandidate, which never quotes it: the
// candidate is the peer's address.
func TestAddRemoteRefusesBadCandidateWithoutQuotingIt(t *testing.T) {
	cn, _, _, _ := newVNetICE(t, vnet.EndpointIndependent)
	o := NewDefaultWebRTCOptions()
	o.Net = cn
	s, err := newICESession(o, true, nil, time.Second)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	err = s.addRemote([]string{"1 1 udp 1 203.0.113.9 notaport typ host"})
	assert.ErrorIs(t, err, errBadCandidate)
	assert.NotContains(t, err.Error(), "203.0.113.9")
}

func TestICEURLsCarryTURNCredentials(t *testing.T) {
	urls, err := iceURLs([]webrtc.ICEServer{
		{URLs: []string{"stun:1.2.3.1:3478"}},
		{URLs: []string{"turn:1.2.3.1:3478?transport=udp"}, Username: "u", Credential: "p"},
	})
	require.NoError(t, err)
	require.Len(t, urls, 2)
	assert.Empty(t, urls[0].Username)
	assert.Equal(t, "u", urls[1].Username)
	assert.Equal(t, "p", urls[1].Password)
}

// A peer can't make the session check more than maxRemoteCandidates addresses. The refusal
// quotes none of them.
func TestAddRemoteCapsCandidates(t *testing.T) {
	cn, _, _, _ := newVNetICE(t, vnet.EndpointIndependent)
	o := NewDefaultWebRTCOptions()
	o.Net = cn
	cands := func(n int) []string {
		var c []string
		for i := 0; i < n; i++ {
			c = append(c, fmt.Sprintf("1 1 udp 2130706431 203.0.113.%d 5000 typ host", i+1))
		}
		return c
	}
	s, err := newICESession(o, true, nil, time.Second)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	err = s.addRemote(cands(maxRemoteCandidates + 1))
	assert.ErrorIs(t, err, errTooManyCandidates)
	assert.NotContains(t, err.Error(), "203.0.113")

	s2, err := newICESession(o, true, nil, time.Second)
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()
	assert.NoError(t, s2.addRemote(cands(maxRemoteCandidates)))
}

// A session's packet conn gives fixed placeholder addresses, never either side's, and has no
// RemoteAddr, so an exit that logs a conn's addresses logs nothing about the user.
func TestICEPacketConnAddressesArePlaceholders(t *testing.T) {
	cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
	consumer, producer := connectICE(t, cn, pn, []webrtc.ICEServer{{URLs: []string{stunURL}}}, WebRTCTuning{})
	local := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 1}
	peer := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1}
	for _, pair := range [][2]net.PacketConn{
		{consumer.packetConn(), producer.packetConn()},
		{producer.packetConn(), consumer.packetConn()},
	} {
		from, to := pair[0], pair[1]
		assert.Equal(t, local.String(), to.LocalAddr().String())
		_, ok := to.(interface{ RemoteAddr() net.Addr })
		assert.False(t, ok, "no RemoteAddr to log")
		_, err := from.WriteTo([]byte("x"), nil)
		require.NoError(t, err)
		require.NoError(t, to.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, addr, err := to.ReadFrom(make([]byte, 1500))
		require.NoError(t, err)
		assert.Equal(t, peer.String(), addr.String())
	}
}

// A server URL that can't be parsed is refused with errBadICEServer, which never quotes it: it
// may name a TURN host.
func TestICEURLsRefuseBadServerWithoutQuotingIt(t *testing.T) {
	_, err := iceURLs([]webrtc.ICEServer{{URLs: []string{"turn:relay.secret.example:3478:1"}}})
	assert.ErrorIs(t, err, errBadICEServer)
	assert.NotContains(t, err.Error(), "relay.secret.example")
}
