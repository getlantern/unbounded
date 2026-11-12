//go:build !wasm

package clientcore

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// vnetDataChannelPair connects two peer connections over a virtual network whose router delays
// every packet by oneWay, and returns an unordered, unretransmitted data channel between them,
// configured as a consumer's is, with a counter of bytes the far end received.
func vnetDataChannelPair(t *testing.T, oneWay time.Duration) (*webrtc.PeerConnection, *webrtc.DataChannel, *atomic.Uint64) {
	t.Helper()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{
		CIDR:          "1.2.3.0/24",
		MinDelay:      oneWay,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	require.NoError(t, err)
	newPC := func(ip string) *webrtc.PeerConnection {
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		require.NoError(t, err)
		require.NoError(t, wan.AddNet(n))
		se := webrtc.SettingEngine{}
		se.SetNet(n)
		pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = pc.Close() })
		return pc
	}
	offerer, answerer := newPC("1.2.3.4"), newPC("1.2.3.5")
	require.NoError(t, wan.Start())
	t.Cleanup(func() { _ = wan.Stop() })

	var received atomic.Uint64
	answerer.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnMessage(func(m webrtc.DataChannelMessage) { received.Add(uint64(len(m.Data))) })
	})
	opened := make(chan struct{})
	ordered, retransmits := false, uint16(0)
	d, err := offerer.CreateDataChannel("data", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits})
	require.NoError(t, err)
	d.OnOpen(func() { close(opened) })

	offer, err := offerer.CreateOffer(nil)
	require.NoError(t, err)
	gathered := webrtc.GatheringCompletePromise(offerer)
	require.NoError(t, offerer.SetLocalDescription(offer))
	<-gathered
	require.NoError(t, answerer.SetRemoteDescription(*offerer.LocalDescription()))
	answer, err := answerer.CreateAnswer(nil)
	require.NoError(t, err)
	gathered = webrtc.GatheringCompletePromise(answerer)
	require.NoError(t, answerer.SetLocalDescription(answer))
	<-gathered
	require.NoError(t, offerer.SetRemoteDescription(*answerer.LocalDescription()))
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("data channel didn't open")
	}
	return offerer, d, &received
}

// blast sends 1200-byte messages on d for dur as fast as admit allows, keeping no more than
// 4MiB buffered whatever admit says, and returns the bytes delivered per second over the last
// half of dur, once the transfer has settled.
func blast(t *testing.T, pc *webrtc.PeerConnection, d *webrtc.DataChannel, received *atomic.Uint64, dur time.Duration,
	admit func(n int, buffered uint64, now time.Time) bool, onTick func()) float64 {
	t.Helper()
	payload := make([]byte, 1200)
	start := time.Now()
	half := start.Add(dur / 2)
	var atHalf uint64
	tick := time.NewTicker(gateRTTEvery)
	defer tick.Stop()
	for now := time.Now(); now.Before(start.Add(dur)); now = time.Now() {
		select {
		case <-tick.C:
			if onTick != nil {
				onTick()
			}
		default:
		}
		if atHalf == 0 && now.After(half) {
			atHalf = received.Load()
		}
		buffered := d.BufferedAmount()
		if buffered > 4<<20 {
			time.Sleep(time.Millisecond)
			continue
		}
		if !admit(len(payload), buffered, now) {
			// What a QUIC sender does after a drop isn't modelled; pace like one.
			time.Sleep(100 * time.Microsecond)
			continue
		}
		require.NoError(t, d.Send(payload))
	}
	return float64(received.Load()-atHalf) / (dur / 2).Seconds()
}

// On a path with a long round trip, the gate must leave the data channel's throughput alone: what
// is buffered includes a round trip's worth in flight, which a fixed threshold would cap.
func TestSendGate_KeepsThroughputOnALongRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("runs data channels over a virtual network for several seconds")
	}
	if raceEnabled {
		t.Skip("under the race detector pion is bound by CPU, not by the path this measures")
	}
	const oneWay, dur = 20 * time.Millisecond, 4 * time.Second

	pc, d, received := vnetDataChannelPair(t, oneWay)
	ungated := blast(t, pc, d, received, dur, func(int, uint64, time.Time) bool { return true }, nil)

	// On a machine too slow to fill the path, the channel queues in pion, not on the path, and
	// the gate rightly drops: that measures the machine, not the gate.
	if ungated < 10e6 {
		t.Skipf("ungated throughput %.1f MB/s: the channel is bound by CPU, not the path", ungated/1e6)
	}

	pc, d, received = vnetDataChannelPair(t, oneWay)
	g := newSendGate(0)
	gated := blast(t, pc, d, received, dur, g.allow, func() { g.setRTT(sctpRTT(pc)) })

	pc, d, received = vnetDataChannelPair(t, oneWay)
	fixed := blast(t, pc, d, received, dur, func(_ int, buffered uint64, _ time.Time) bool { return buffered <= 64<<10 }, nil)

	t.Logf("40ms round trip: ungated %.1f MB/s, gated %.1f MB/s (rtt %v), fixed 64KiB threshold %.1f MB/s",
		ungated/1e6, gated/1e6, g.rtt, fixed/1e6)
	require.Positive(t, g.rtt, "the gated run measured no round trip, so it never gated")
	require.Greater(t, ungated, 0.0)
	require.GreaterOrEqual(t, gated, 0.8*ungated, "the gate cut a long path's throughput")
}
