//go:build !wasm

package clientcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
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

	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, iceNet{net: counting})
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
	producer, err := newProducerPeerConnection(webrtc.Configuration{}, covertdtls.Config{}, WebRTCTuning{}, iceNet{})
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
	pn := iceNet{mux: mux, externalIP: "203.0.113.9"}
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
	mapped := iceNet{mux: mux, externalIP: "203.0.113.9"}
	for _, tc := range []struct {
		cands []webrtc.ICECandidate
		pn    iceNet
		want  bool
	}{
		{[]webrtc.ICECandidate{host}, iceNet{}, false},
		{[]webrtc.ICECandidate{host, srflx}, iceNet{}, true},
		{[]webrtc.ICECandidate{host, ext}, mapped, true},
		{[]webrtc.ICECandidate{host, ext}, iceNet{externalIP: "203.0.113.9"}, false},
		{[]webrtc.ICECandidate{host}, mapped, false},
	} {
		if got := reachableCandidates(tc.cands, tc.pn); got != tc.want {
			t.Errorf("reachableCandidates(%v, mapped=%v) = %v, want %v", tc.cands, tc.pn.mapped(), got, tc.want)
		}
	}
}

// candidateTypes counts cands by type, and reports whether one is a host
// candidate on ext at port.
func candidateTypes(cands []string, ext string, port int) (types map[string]int, mapped bool) {
	types = map[string]int{}
	for _, c := range cands {
		f := strings.Fields(c)
		if len(f) < 8 || !strings.EqualFold(f[2], "udp") {
			continue
		}
		types[f[7]]++
		if f[4] == ext && f[5] == strconv.Itoa(port) && f[7] == "host" {
			mapped = true
		}
	}
	return types, mapped
}

// A consumer given a UDP mux and its external address advertises that address
// as a host candidate on the mux's port, and still gathers server-reflexive
// candidates from STUN, so a mapping that doesn't work leaves it no worse off.
func TestConsumerAdvertisesMappedAddressAndKeepsSTUN(t *testing.T) {
	mux, port := newTestMux(t)
	api, err := newWebRTCAPI(iceNet{mux: mux, externalIP: "203.0.113.9"}, WebRTCTuning{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{stunServer(t, answer)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	cands := gatheredCandidates(t, pc)
	types, mapped := candidateTypes(cands, "203.0.113.9", port)
	if !mapped {
		t.Errorf("no host candidate on the external address and the mux's port among %v", cands)
	}
	if types["srflx"] == 0 {
		t.Errorf("no server-reflexive candidate among %v", cands)
	}
}

// A producer given a UDP mux and its external address gathers STUN candidates
// too, as well as the mapped one.
func TestMappedProducerKeepsSTUNCandidates(t *testing.T) {
	mux, port := newTestMux(t)
	pc, err := newProducerPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{stunServer(t, answer)}}},
	}, covertdtls.Config{}, WebRTCTuning{}, iceNet{mux: mux, externalIP: "203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	cands := gatheredCandidates(t, pc)
	types, mapped := candidateTypes(cands, "203.0.113.9", port)
	if !mapped || types["srflx"] == 0 {
		t.Fatalf("want the mapped host candidate and a server-reflexive one among %v", cands)
	}
}

// mappedOptions is a default WebRTCOptions with a UDP mux and external address.
func mappedOptions(t *testing.T) *WebRTCOptions {
	t.Helper()
	mux, _ := newTestMux(t)
	o := NewDefaultWebRTCOptions()
	o.ICEUDPMux = mux
	o.ICEUDPMuxExternalIP = "203.0.113.9"
	return o
}

// producerState0 runs a producer's state 0 once and returns the state it went
// to and its peer connection, if it built one.
func producerState0(t *testing.T, fsm *WorkerFSM) (int, *webrtc.PeerConnection) {
	t.Helper()
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	var pc *webrtc.PeerConnection
	if len(out) > 0 {
		pc, _ = out[0].(*webrtc.PeerConnection)
	}
	if pc != nil {
		t.Cleanup(func() { pc.Close() })
	}
	return next, pc
}

// A mapped producer uses STUN when it works: it checks its STUN servers and
// gives them to pion.
func TestMappedProducerUsesWorkingSTUN(t *testing.T) {
	server := stunServer(t, answer)
	o := mappedOptions(t)
	o.STUNBatch = func(uint32) ([]string, error) { return []string{server}, nil }
	var ran atomic.Int32
	o.OnSTUNPreflight = func(STUNPreflightResult) { ran.Add(1) }

	next, pc := producerState0(t, NewProducerWebRTC(o, nil))
	if next != 1 || pc == nil {
		t.Fatalf("went to state %d, want 1 and a peer connection", next)
	}
	if ran.Load() != 1 {
		t.Errorf("ran the STUN check %d times, want 1", ran.Load())
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 1 || len(servers[0].URLs) != 1 || servers[0].URLs[0] != server {
		t.Errorf("ICE servers %v, want [%s]", servers, server)
	}
}

// A mapped producer whose STUN list can't be fetched still takes offers on its
// mapped address, with no ICE servers.
func TestMappedProducerTakesOffersWithoutSTUNList(t *testing.T) {
	o := mappedOptions(t)
	o.STUNBatch = func(uint32) ([]string, error) { return nil, errors.New("STUN list unavailable") }
	var ran atomic.Bool
	o.OnSTUNPreflight = func(STUNPreflightResult) { ran.Store(true) }

	next, pc := producerState0(t, NewProducerWebRTC(o, nil))
	if next != 1 || pc == nil {
		t.Fatalf("went to state %d, want 1 and a peer connection", next)
	}
	if ran.Load() {
		t.Error("ran the STUN check with no STUN servers")
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
		t.Errorf("ICE servers %v, want none", servers)
	}
}

// A mapped producer whose STUN check fails takes offers on its mapped address
// alone, and skips STUN until the check's backoff ends rather than wait out a
// failing check before every offer.
func TestMappedProducerPausesSTUNAfterFailedCheck(t *testing.T) {
	silent := stunServer(t, nil)
	o := mappedOptions(t)
	o.STUNBatch = func(uint32) ([]string, error) { return []string{silent}, nil }
	var ran atomic.Int32
	o.OnSTUNPreflight = func(STUNPreflightResult) { ran.Add(1) }
	now := time.Now()
	preflightNow = func() time.Time { return now }
	t.Cleanup(func() { preflightNow = time.Now })

	fsm := NewProducerWebRTC(o, nil)
	for i, tc := range []struct {
		advance time.Duration
		checks  int32
	}{
		{0, 1},
		{stunPreflightMinBackoff - time.Second, 1},
		{2 * time.Second, 2},
	} {
		now = now.Add(tc.advance)
		next, pc := producerState0(t, fsm)
		if next != 1 || pc == nil {
			t.Fatalf("attempt %d went to state %d, want 1 and a peer connection", i, next)
		}
		if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
			t.Errorf("attempt %d: ICE servers %v, want none", i, servers)
		}
		if got := ran.Load(); got != tc.checks {
			t.Errorf("attempt %d: %d STUN checks so far, want %d", i, got, tc.checks)
		}
	}
}

// A mapped consumer whose STUN list can't be fetched still makes an offer, on
// its mapped address alone.
func TestMappedConsumerOffersWithoutSTUNList(t *testing.T) {
	o := mappedOptions(t)
	o.STUNBatch = func(uint32) ([]string, error) { return nil, errors.New("STUN list unavailable") }
	fsm := newConsumerWebRTCSlot(o, nil, newRoomPreference(time.Now), nil)
	next, out := fsm.state[0](context.Background(), fsm.com, nil)
	var pc *webrtc.PeerConnection
	if len(out) > 0 {
		pc, _ = out[0].(*webrtc.PeerConnection)
	}
	if pc != nil {
		defer pc.Close()
	}
	if next != 1 || pc == nil {
		t.Fatalf("went to state %d, want 1 and a peer connection", next)
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
		t.Errorf("ICE servers %v, want none", servers)
	}
}

// Without a mapped address, a consumer whose STUN list can't be fetched waits
// for it, as before.
func TestUnmappedConsumerNeedsSTUNList(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.STUNBatch = func(uint32) ([]string, error) { return nil, errors.New("STUN list unavailable") }
	fsm := newConsumerWebRTCSlot(o, nil, newRoomPreference(time.Now), nil)
	if next, _ := fsm.state[0](context.Background(), fsm.com, nil); next != 0 {
		t.Fatalf("went to state %d, want 0", next)
	}
}

// A producer accepts a consumer that offers only host candidates when one is
// on a public address, as a consumer behind a mapped port does when its STUN
// found nothing, and still turns away one with only private host candidates.
func TestRemoteCandidatesReachable(t *testing.T) {
	lan := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: "192.168.1.10"}
	mapped := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: "203.0.113.9"}
	srflx := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeSrflx, Address: "198.51.100.1"}
	mdns := webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost, Address: "f3a1.local"}
	for _, tc := range []struct {
		cands []webrtc.ICECandidate
		want  bool
	}{
		{nil, false},
		{[]webrtc.ICECandidate{lan}, false},
		{[]webrtc.ICECandidate{lan, mdns}, false},
		{[]webrtc.ICECandidate{lan, srflx}, true},
		{[]webrtc.ICECandidate{lan, mapped}, true},
	} {
		if got := remoteCandidatesReachable(tc.cands); got != tc.want {
			t.Errorf("remoteCandidatesReachable(%v) = %v, want %v", tc.cands, got, tc.want)
		}
	}
}

// A mapped producer running without STUN drops a pass left from an earlier
// attempt, so the pass expiring can't send every later attempt back to state 0
// before it offers itself.
func TestMappedProducerWithoutSTUNDropsOldPass(t *testing.T) {
	server := stunServer(t, answer)
	o := mappedOptions(t)
	listUp := true
	o.STUNBatch = func(uint32) ([]string, error) {
		if listUp {
			return []string{server}, nil
		}
		return nil, errors.New("STUN list unavailable")
	}
	now := time.Now()
	preflightNow = func() time.Time { return now }
	t.Cleanup(func() { preflightNow = time.Now })

	// Freddie turns the producer's answer away, as when the consumer hung up,
	// which drops the producer's only STUN server from its cache.
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(freddie.Close)
	o.DiscoverySrv = freddie.URL

	fsm := NewProducerWebRTC(o, nil)
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("first attempt went to state %d, want 1", next)
	}
	consumer, _ := newTestPeerConnection(t)
	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	if next, _ := fsm.state[3](context.Background(), fsm.com, []interface{}{
		pc,
		"reply-to",
		common.OfferMsg{SDP: *consumer.LocalDescription()},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	}); next != 0 {
		t.Fatalf("state 3 went to state %d after a 404, want 0", next)
	}

	// The list is now down, so the next attempt runs on the mapped address alone.
	listUp = false
	next, pc = producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("second attempt went to state %d, want 1", next)
	}
	if servers := pc.GetConfiguration().ICEServers; len(servers) != 0 {
		t.Fatalf("second attempt has ICE servers %v, want none", servers)
	}
	now = now.Add(stunPreflightValidFor + time.Second)
	if next, _ := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc,
		common.PathAssertion{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	}); next == 0 {
		t.Fatal("state 2 went back to state 0 on a pass from an attempt that used STUN")
	}
}

// switchableSTUN is a local STUN server that answers only while up is true.
func switchableSTUN(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	up := &atomic.Bool{}
	up.Store(true)
	return stunServer(t, func(req *stun.Message, from *net.UDPAddr) *stun.Message {
		if !up.Load() {
			return nil
		}
		return answer(req, from)
	}), up
}

// signalBody is Freddie's reply carrying msg as a signal message of type typ.
func signalBody(t *testing.T, typ common.SignalMsgType, msg any) []byte {
	t.Helper()
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(common.SignalMsg{ReplyTo: "peer", Type: typ, Payload: string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// mappedProducerSTUNFailsWhileGathering runs a mapped producer whose STUN cohort passes its
// check in state 0 but stops answering before state 3 gathers, against a Freddie that answers
// the producer's answer with status, and returns the state 3 went to, the producer's worker, and
// the server the first attempt used.
func mappedProducerSTUNFailsWhileGathering(t *testing.T, status int) (int, *WorkerFSM, string, *atomic.Int32) {
	t.Helper()
	a, aUp := switchableSTUN(t)
	b, bUp := switchableSTUN(t)
	up := map[string]*atomic.Bool{a: aUp, b: bUp}
	o := mappedOptions(t)
	o.STUNBatchSize = 1
	var fetches atomic.Int32
	o.STUNBatch = func(uint32) ([]string, error) {
		fetches.Add(1)
		return []string{a, b}, nil
	}
	consumerICE := signalBody(t, common.SignalMsgICE, common.ICEMsg{
		Candidates: []webrtc.ICECandidate{{
			Foundation: "1", Priority: 1, Address: "198.51.100.7", Protocol: webrtc.ICEProtocolUDP,
			Port: 4000, Typ: webrtc.ICECandidateTypeSrflx, Component: 1,
		}},
		ConsumerSessionID: "session",
	})
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write(consumerICE)
		}
	}))
	t.Cleanup(freddie.Close)
	o.DiscoverySrv = freddie.URL

	fsm := NewProducerWebRTC(o, nil)
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("state 0 went to state %d, want 1", next)
	}
	servers := pc.GetConfiguration().ICEServers
	if len(servers) != 1 || len(servers[0].URLs) != 1 {
		t.Fatalf("ICE servers %v, want one", servers)
	}
	first := servers[0].URLs[0]
	up[first].Store(false)

	consumer, _ := newTestPeerConnection(t)
	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	next, _ = fsm.state[3](context.Background(), fsm.com, []interface{}{
		pc,
		"reply-to",
		common.OfferMsg{SDP: *consumer.LocalDescription()},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	return next, fsm, first, &fetches
}

// A mapped producer whose STUN finds nothing while gathering still answers, on its mapped
// address.
func TestMappedProducerAnswersWhenSTUNGathersNothing(t *testing.T) {
	next, _, _, _ := mappedProducerSTUNFailsWhileGathering(t, http.StatusOK)
	if next != 4 {
		t.Fatalf("state 3 went to state %d, want 4", next)
	}
}

// A mapped producer whose STUN finds nothing while gathering drops that cohort, and only that
// one: the consumer hanging up afterwards doesn't drop the next, untried.
func TestMappedProducerDropsOneCohortWhenSTUNGathersNothing(t *testing.T) {
	next, fsm, first, fetches := mappedProducerSTUNFailsWhileGathering(t, http.StatusNotFound)
	if next != 0 {
		t.Fatalf("state 3 went to state %d after a 404, want 0", next)
	}
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil {
		t.Fatalf("next attempt went to state %d, want 1", next)
	}
	servers := pc.GetConfiguration().ICEServers
	if len(servers) != 1 || len(servers[0].URLs) != 1 || servers[0].URLs[0] == first {
		t.Errorf("next attempt's ICE servers %v, want the other server, not %s", servers, first)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("fetched the STUN list %d times, want 1", got)
	}
}

// A mapped consumer whose STUN finds nothing still sends its candidates, with the mapped one
// among them.
func TestMappedConsumerOffersWhenSTUNGathersNothing(t *testing.T) {
	o := mappedOptions(t)
	o.ErrorBackoff = 10 * time.Millisecond
	api, err := newWebRTCAPI(iceNetOf(o), WebRTCTuning{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if _, err := pc.CreateDataChannel("data", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	producer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { producer.Close() })
	if err := producer.SetRemoteDescription(offer); err != nil {
		t.Fatal(err)
	}
	ans, err := producer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := signalBody(t, common.SignalMsgAnswer, ans)
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(freddie.Close)
	o.DiscoverySrv = freddie.URL

	fsm := newConsumerWebRTCSlot(o, nil, newRoomPreference(time.Now), nil)
	next, out := fsm.state[2](context.Background(), fsm.com, []interface{}{
		pc,
		"reply-to",
		offer,
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	if next != 3 {
		t.Fatalf("state 2 went to state %d, want 3", next)
	}
	var mapped bool
	for _, c := range out[2].([]webrtc.ICECandidate) {
		if c.Typ == webrtc.ICECandidateTypeHost && c.Address == "203.0.113.9" {
			mapped = true
		}
	}
	if !mapped {
		t.Errorf("no mapped host candidate among %v", out[2])
	}
}

// A mapped peer whose STUN list can't be fetched doesn't fetch it again on every attempt: it
// waits out a backoff first, as a failed STUN check makes it wait.
func TestMappedPeersBackOffAFailedSTUNList(t *testing.T) {
	for _, tc := range []struct {
		name string
		fsm  func(*WebRTCOptions) *WorkerFSM
	}{
		{"producer", func(o *WebRTCOptions) *WorkerFSM { return NewProducerWebRTC(o, nil) }},
		{"consumer", func(o *WebRTCOptions) *WorkerFSM {
			return newConsumerWebRTCSlot(o, nil, newRoomPreference(time.Now), nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := mappedOptions(t)
			var fetches atomic.Int32
			o.STUNBatch = func(uint32) ([]string, error) {
				fetches.Add(1)
				return nil, errors.New("STUN list unavailable")
			}
			now := time.Now()
			preflightNow = func() time.Time { return now }
			t.Cleanup(func() { preflightNow = time.Now })

			fsm := tc.fsm(o)
			for i, step := range []struct {
				advance time.Duration
				fetches int32
			}{
				{0, 1},
				{stunPreflightMinBackoff - time.Second, 1},
				{2 * time.Second, 2},
			} {
				now = now.Add(step.advance)
				next, pc := producerState0(t, fsm)
				if next != 1 || pc == nil {
					t.Fatalf("attempt %d went to state %d, want 1", i, next)
				}
				if got := fetches.Load(); got != step.fetches {
					t.Errorf("attempt %d: %d fetches so far, want %d", i, got, step.fetches)
				}
			}
		})
	}
}

// A mapped producer's attempts without STUN, after a failed check, drop no cohort: they never
// tried one, so the next check still has the untried servers.
func TestMappedProducerWithoutSTUNKeepsUntriedCohorts(t *testing.T) {
	// Both servers stay silent, counting the requests they get.
	var aHits, bHits atomic.Int32
	counting := func(n *atomic.Int32) func(*stun.Message, *net.UDPAddr) *stun.Message {
		return func(*stun.Message, *net.UDPAddr) *stun.Message { n.Add(1); return nil }
	}
	a, b := stunServer(t, counting(&aHits)), stunServer(t, counting(&bHits))
	o := mappedOptions(t)
	o.STUNBatchSize = 1
	var fetches atomic.Int32
	o.STUNBatch = func(uint32) ([]string, error) {
		fetches.Add(1)
		return []string{a, b}, nil
	}
	var checks atomic.Int32
	o.OnSTUNPreflight = func(STUNPreflightResult) { checks.Add(1) }
	freddie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(freddie.Close)
	o.DiscoverySrv = freddie.URL
	now := time.Now()
	preflightNow = func() time.Time { return now }
	t.Cleanup(func() { preflightNow = time.Now })

	fsm := NewProducerWebRTC(o, nil)
	next, pc := producerState0(t, fsm)
	if next != 1 || pc == nil || checks.Load() != 1 {
		t.Fatalf("first attempt went to state %d after %d checks, want 1 after 1", next, checks.Load())
	}
	if (aHits.Load() > 0) == (bHits.Load() > 0) {
		t.Fatalf("first check reached a %d times and b %d times, want exactly one of them", aHits.Load(), bHits.Load())
	}
	consumer, _ := newTestPeerConnection(t)
	offer, err := consumer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	if next, _ := fsm.state[3](context.Background(), fsm.com, []interface{}{
		pc,
		"reply-to",
		common.OfferMsg{SDP: *consumer.LocalDescription()},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	}); next != 0 {
		t.Fatalf("state 3 went to state %d after a 404, want 0", next)
	}

	now = now.Add(stunPreflightMinBackoff + time.Second)
	if next, _ := producerState0(t, fsm); next != 1 {
		t.Fatalf("attempt after the backoff went to state %d, want 1", next)
	}
	if checks.Load() != 2 || aHits.Load() == 0 || bHits.Load() == 0 {
		t.Errorf("%d checks reached a %d times and b %d times, want 2 checks reaching both", checks.Load(), aHits.Load(), bHits.Load())
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("fetched the STUN list %d times, want 1", got)
	}
}

// A producer turns away a consumer's candidate list it should not hand pion: too many, a
// duplicate, or a candidate with a bad port, protocol or address.
func TestValidateRemoteCandidates(t *testing.T) {
	ok := func(addr string, port uint16) webrtc.ICECandidate {
		return webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeSrflx, Protocol: webrtc.ICEProtocolUDP, Address: addr, Port: port}
	}
	many := make([]webrtc.ICECandidate, maxRemoteCandidates+1)
	for i := range many {
		many[i] = ok("198.51.100.1", uint16(1000+i))
	}
	badProto := ok("198.51.100.1", 1)
	badProto.Protocol = webrtc.ICEProtocolUnknown
	for _, tc := range []struct {
		name  string
		cands []webrtc.ICECandidate
		valid bool
	}{
		{"typical", []webrtc.ICECandidate{ok("192.168.1.10", 5000), ok("f3a1.local", 5001), ok("198.51.100.1", 5000)}, true},
		{"at the limit", many[:maxRemoteCandidates], true},
		{"over the limit", many, false},
		{"duplicate", []webrtc.ICECandidate{ok("198.51.100.1", 5000), ok("198.51.100.1", 5000)}, false},
		{"port 0", []webrtc.ICECandidate{ok("198.51.100.1", 0)}, false},
		{"bad protocol", []webrtc.ICECandidate{badProto}, false},
		{"hostname", []webrtc.ICECandidate{ok("metadata.google.internal", 80)}, false},
	} {
		if err := validateRemoteCandidates(tc.cands); (err == nil) != tc.valid {
			t.Errorf("%s: validateRemoteCandidates = %v, want valid %v", tc.name, err, tc.valid)
		}
	}
}
