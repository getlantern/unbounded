// producer.go defines standard producer behavior over WebRTC, including the discovery process,
// signaling, connection establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=471342300
package clientcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/covertdtls"
)

// Producer FSM state indices and their human-readable names, for logging. The
// indices must match the positions of the FSMstates in the slice returned by
// NewProducerWebRTC (the FSM dispatches by index; see protocol.go).
const (
	pInit          = iota // 0: create PeerConnection, ensure STUN cache
	pAwaitPath            // 1: wait for upstream path assertion to share
	pSignalGenesis       // 2: advertise availability, receive an offer
	pSignalAnswer        // 3: answer the offer, exchange ICE candidates
	pAwaitConn           // 4: wait for NAT traversal / datachannel to open
	pProxy               // 5: relay data between peer and egress
)

var producerStateNames = map[int]string{
	pInit:          "init",
	pAwaitPath:     "await-path",
	pSignalGenesis: "signal-genesis",
	pSignalAnswer:  "signal-answer",
	pAwaitConn:     "await-connection",
	pProxy:         "proxy",
}

// formatCandidates renders ICE candidates compactly for logging, e.g.
// "srflx/udp 203.0.113.7:54321".
func formatCandidates(cands []webrtc.ICECandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, fmt.Sprintf("%s/%s %s:%d", c.Typ, c.Protocol, c.Address, c.Port))
	}
	return out
}

// publicAddrs returns the sorted, de-duplicated set of public IP addresses among
// a candidate set — the addresses a peer could actually reach us at. Host and
// CGNAT/private reflexive addresses are excluded, and ports are ignored: NAT
// assigns a fresh port per connection attempt, so including them would make the
// set appear to "change" every attempt. This is what we key local-candidate
// change detection on, so we log our presentation once and again only when the
// public address itself actually moves.
func publicAddrs(cands []webrtc.ICECandidate) []string {
	seen := map[string]struct{}{}
	for _, c := range cands {
		ip := net.ParseIP(c.Address)
		if ip != nil && common.IsPublicAddr(ip) {
			seen[ip.String()] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Local-candidate change detection is process-wide, not per-worker: all of a
// widget's producer workers present the same public address, so tracking this
// per-worker would log the same line once per worker at startup. One engine runs
// per process (see stats.go), so a package-level record logs it once.
var (
	localCandMu  sync.Mutex
	localCandSig string
)

// localCandidatesChanged reports whether pub differs from the last public-address
// set observed by any producer worker, recording it as the new baseline. It
// returns false for the initial empty set so we log only once we actually present
// a public address.
func localCandidatesChanged(pub []string) bool {
	sig := strings.Join(pub, ",")
	localCandMu.Lock()
	defer localCandMu.Unlock()
	if sig == localCandSig {
		return false
	}
	localCandSig = sig
	return true
}

func NewProducerWebRTC(options *WebRTCOptions, wg *sync.WaitGroup) *WorkerFSM {
	var scache STUNCache

	// Per-worker logging context. A WorkerFSM runs its states sequentially in a
	// single goroutine (see protocol.go), so these are safe to read/write from any
	// state without synchronization. curPeer/curSession/curRemoteCand describe the
	// connection attempt currently in flight; they are reset at state pInit and
	// populated once signaling reveals them, so every log line for an attempt can
	// carry the peer and session. lastLocalSig tracks the last-logged local ICE
	// candidate set so we log it at startup and then only when it changes.
	var (
		curPeer       net.IP
		curSession    string
		curRemoteCand []webrtc.ICECandidate
		curLocalCand  []webrtc.ICECandidate
	)

	// Latest ICE / peer-connection state for the attempt in flight. The pion event
	// callbacks run on their own goroutines, so these are atomics read by the FSM
	// goroutine when it needs to explain a timeout. They let us tell a genuine NAT
	// failure (ICE never left "checking", or reached "failed") apart from a slow
	// but successful handshake (ICE "connected" but the datachannel just hadn't
	// finished DTLS/SCTP when the NATFailTimeout fired).
	var curICEState, curPCState atomic.Value

	// plog returns a logger tagged with the state name plus the peer and session
	// once they are known for the current attempt.
	plog := func(state int) *slog.Logger {
		l := slog.With("worker", "producer", "state", producerStateNames[state])
		if curPeer != nil {
			l = l.With("peer", curPeer.String())
		}
		if curSession != "" {
			l = l.With("session", curSession)
		}
		return l
	}

	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)

			// A fresh connection attempt begins here; clear the previous attempt's
			// peer/session/candidate context so stale values never leak onto this
			// attempt's log lines.
			curPeer, curSession, curRemoteCand, curLocalCand = nil, "", nil, nil
			curICEState.Store("new")
			curPCState.Store("new")
			logger := plog(pInit)

			// Populate the STUN cache if necessary
			if scache.size() == 0 {
				allSTUNSrvs, err := options.STUNBatch(math.MaxInt32)
				if err != nil {
					logger.Debug("error creating STUN batch", "error", err)
					return 0, []interface{}{}
				}

				scache = newSTUNCache(allSTUNSrvs, float64(options.STUNBatchSize))
				logger.Debug("populated the STUN cache", "servers", scache.size())
			}

			STUNSrvs := scache.cohort()
			// slog.Debug("Using STUN servers", "count", len(STUNSrvs), "total", options.STUNBatchSize, "servers", STUNSrvs)
			// slog.Debug("STUN cache size", "size", scache.size())

			config := webrtc.Configuration{
				ICEServers: []webrtc.ICEServer{
					{
						URLs: STUNSrvs,
					},
				},
			}

			// Producers are the answerers, which makes them the active peer in
			// the DTLS handshake per RFC 5763 — they send the ClientHello. The
			// default pion fingerprint is DPI-filtered in Russia (see
			// net4people/bbs#603), so we route through a SettingEngine with a
			// covert-dtls hook when enabled.
			peerConnection, err := newProducerPeerConnection(config, options.CovertDTLS)
			if err != nil {
				logger.Debug("error creating RTCPeerConnection", "error", err)
				return 0, []interface{}{}
			}

			// Producers are the answerers, so we don't create a datachannel

			// We want to make sure we capture the connection establishment event whenever it happens,
			// but we also want to avoid control flow spaghetti (it would very hard to reason about
			// client operation if we sometimes jump forward to future states based on async events
			// firing outside of the state machine). Solution: Pass forward this buffered channel such
			// that we can explicitly check for connection establishment in state 4. In theory, it's
			// possible that magical ICE mysteries could cause the connection to open as early as the end
			// of state 2. In practice, the differences here should be on the order of nanoseconds. But
			// we should monitor the logs to see if connections open too long before we check for them.
			connectionEstablished := make(chan *webrtc.DataChannel, 1)

			// connectionClosed (and the OnClose handler below) is implemented for Firefox, the only
			// browser which doesn't implement WebRTC's onconnectionstatechange event. We listen for both
			// onclose and onconnectionstatechange under the assumption that non-Firefox browsers can
			// benefit from faster connection failure detection by listening for the `failed` event.
			connectionClosed := make(chan struct{}, 1)
			peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
				dlog := plog(pAwaitConn).With("dc_id", d.ID(), "dc_label", d.Label())
				dlog.Debug("datachannel created")

				d.OnOpen(func() {
					dlog.Debug("datachannel opened")
					connectionEstablished <- d
				})

				d.OnClose(func() {
					dlog.Debug("datachannel closed")
					connectionClosed <- struct{}{}
				})
			})

			// Ditto, but for connection state changes
			connectionChange := make(chan webrtc.PeerConnectionState, 16)
			peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
				curPCState.Store(s.String())
				plog(pAwaitConn).Debug("peer connection state change", "conn_state", s.String())
				connectionChange <- s
			})

			// ICE connection state changes trace NAT-traversal progress
			// (checking -> connected/failed). Kept at DEBUG — a handful of lines per
			// attempt — but invaluable when diagnosing why traversal fails, which is
			// exactly the case this logging pass is meant to serve.
			peerConnection.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
				curICEState.Store(s.String())
				plog(pAwaitConn).Debug("ICE connection state change", "ice_state", s.String())
			})

			return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 1
			// input[0]: *webrtc.PeerConnection
			// input[1]: chan *webrtc.DataChannel
			// input[2]: chan webrtc.PeerConnectionState
			// input[3]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			connectionEstablished := input[1].(chan *webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			// slog.Debug("Producer state 1...")

			// Do we have a non-nil path assertion, indicating that we have upstream connectivity to share?
			// We find out by sending an ConnectivityCheckIPC message, which asks the process responsible
			// for path assertions to send a message reflecting the current state of our path assertion.
			// If yes, we can proceed right now! If no, just wait for the next non-nil path assertion message...
			if !sendCtx(ctx, com.tx, IPCMsg{IpcType: ConnectivityCheckIPC}) {
				return 0, input
			}

			for {
				select {
				// Handle inbound IPC messages, wait for a non-nil *and not JIT unavailable* path assertion
				// TODO nelson 07/25/2025: the JIT unavailable flag was added to synchronize with the
				// JIT egress consumer, but it's wacky and should be cleaned up here:
				// https://github.com/getlantern/engineering/issues/2402
				case msg := <-com.rx:
					if msg.IpcType == PathAssertionIPC && !msg.Data.(common.PathAssertion).Nil() &&
						!msg.Data.(common.PathAssertion).JITUnavailable {
						return 2, []interface{}{peerConnection, msg.Data.(common.PathAssertion), connectionEstablished, connectionChange, connectionClosed}
					}
				// Since we're putting this state into an infinite loop, explicitly handle cancellation
				case <-ctx.Done():
					peerConnection.Close()
					return 0, []interface{}{}
				}
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 2
			// input[0]: *webrtc.PeerConnection
			// input[1]: common.PathAssertion
			// input[2]: chan *webrtc.DataChannel
			// input[3]: chan webrtc.PeerConnectionState
			// input[4]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			pa := input[1].(common.PathAssertion)
			connectionEstablished := input[2].(chan *webrtc.DataChannel)
			connectionChange := input[3].(chan webrtc.PeerConnectionState)
			connectionClosed := input[4].(chan struct{})
			logger := plog(pSignalGenesis)

			// Construct a genesis message
			g, err := json.Marshal(common.GenesisMsg{PathAssertion: pa})
			if err != nil {
				logger.Debug("error marshaling genesis JSON", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Signal the genesis message
			form := url.Values{
				"data":    {string(g)},
				"send-to": {options.GenesisAddr},
				"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
			}

			req, err := http.NewRequestWithContext(
				ctx,
				"POST",
				options.DiscoverySrv+options.Endpoint,
				strings.NewReader(form.Encode()),
			)
			if err != nil {
				logger.Debug("error constructing genesis request", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HTTPClient.Do(req)
			if err != nil {
				logger.Debug("couldn't signal genesis message", "url", options.DiscoverySrv+options.Endpoint, "error", err)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			defer res.Body.Close()

			// Freddie never returns 404s for genesis messages, so we're not catching that case here

			// Handle bad protocol version
			if res.StatusCode == http.StatusTeapot {
				logger.Debug("received 'bad protocol version' response to genesis")
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// The HTTP request is complete
			offerBytes, err := io.ReadAll(res.Body)
			if err != nil {
				logger.Debug("error reading genesis response body", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: Freddie sends back a 0-length body when nobody replied to our message. Is that the
			// smartest way to handle this case systemwide?
			if len(offerBytes) == 0 {
				// No offer waiting for us — this is the common idle case, so stay silent.
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Looks like we got some kind of response. It ought to be an offer SDP wrapped in a SignalMsg
			replyTo, offer, err := common.DecodeSignalMsg(offerBytes)
			if err != nil {
				logger.Debug("error decoding genesis signal message", "error", err, "msg", string(offerBytes))
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: here we assume we've received a valid offer SDP, we also need to handle invalid case
			return 3, []interface{}{peerConnection, replyTo, offer, connectionEstablished, connectionChange, connectionClosed}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 3
			// input[0]: *webrtc.PeerConnection
			// input[1]: string (replyTo)
			// input[2]: common.OfferMsg (remote offer)
			// input[3]: chan *webrtc.DataChannel
			// input[4]: chan webrtc.PeerConnectionState
			// input[5]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			replyTo := input[1].(string)
			offer := input[2].(common.OfferMsg)
			connectionEstablished := input[3].(chan *webrtc.DataChannel)
			connectionChange := input[4].(chan webrtc.PeerConnectionState)
			connectionClosed := input[5].(chan struct{})
			logger := plog(pSignalAnswer).With("peer_tag", offer.Tag, "peer_country", offer.Country)

			// Create a channel that's blocked until ICE gathering is complete
			gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

			// XXX: in our present signaling handshake, the *consumer's* ICE candidates are sent "a la carte"
			// as a list in the final segment of the handshake. But here on the producer side, our ICE
			// candidates are attached to our answer SDP and signaled all at once. Thus, the only reason
			// we create this slice of candidates is to evaluate whether STUN worked, which happens below.
			// Believe it or not, this post facto examination of ICE candidates is the idiomatic way to
			// determine whether any of your STUN type ICE agents worked...
			localCandidates := []webrtc.ICECandidate{}
			peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
				// Interestingly, the null candidate is a nil pointer so we cause a nil ptr dereference
				// if we try to append it to the list... so let's just not include it?
				if c != nil {
					localCandidates = append(localCandidates, *c)
				}
			})

			// Assign the offer to our connection
			err := peerConnection.SetRemoteDescription(offer.SDP)
			if err != nil {
				logger.Debug("error setting remote description", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Generate an answer
			answer, err := peerConnection.CreateAnswer(nil)
			if err != nil {
				logger.Debug("error creating answer SDP", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// This kicks off ICE candidate gathering
			err = peerConnection.SetLocalDescription(answer)
			if err != nil {
				logger.Debug("error setting local description", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			<-gatherComplete
			curLocalCand = localCandidates
			logger.Debug("ICE gathering complete", "local_candidates", formatCandidates(localCandidates))

			// Log our local ICE candidates at startup and thereafter only when the set
			// of public addresses we present to peers changes (see publicAddrs — ports
			// and CGNAT noise are ignored so this doesn't fire every attempt). A change
			// means our reflexive address moved or STUN stopped yielding a public
			// candidate, both worth surfacing.
			if pub := publicAddrs(localCandidates); localCandidatesChanged(pub) {
				slog.Info("local ICE candidates",
					"public_addrs", pub,
					"candidates", formatCandidates(localCandidates),
				)
			}

			// If the STUN server(s) we used for this signaling attempt were blocked or unresponsive,
			// we probably wound up with a slice of valid ICE candidates, but of only the 'host' type.
			// We don't want to bother signaling those, so here's our escape hatch.
			var hasNonHostCandidate bool
			for _, c := range localCandidates {
				if c.Typ != webrtc.ICECandidateTypeHost {
					hasNonHostCandidate = true
				}
			}

			if !hasNonHostCandidate {
				logger.Info("connection attempt failed",
					"reason", "no-local-non-host-candidates",
					"detail", "our STUN cohort yielded only host candidates (likely blocked or unresponsive)",
					"local_candidates", formatCandidates(localCandidates),
				)
				scache.drop()
				logger.Debug("dropped the current STUN cohort", "reason", "ice-failed")

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Our answer SDP with ICE candidates attached
			finalAnswer := peerConnection.LocalDescription()

			a, err := json.Marshal(finalAnswer)
			if err != nil {
				logger.Debug("error marshaling answer JSON", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Signal our answer
			form := url.Values{
				"data":    {string(a)},
				"send-to": {replyTo},
				"type":    {strconv.Itoa(int(common.SignalMsgAnswer))},
			}

			req, err := http.NewRequestWithContext(
				ctx,
				"POST",
				options.DiscoverySrv+options.Endpoint,
				strings.NewReader(form.Encode()),
			)
			if err != nil {
				logger.Debug("error constructing answer request", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HTTPClient.Do(req)
			if err != nil {
				logger.Debug("couldn't signal answer SDP", "url", options.DiscoverySrv+options.Endpoint, "error", err)
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}
			defer res.Body.Close()

			switch res.StatusCode {
			case http.StatusTeapot:
				logger.Debug("received 'bad protocol version' response to answer")
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusNotFound:
				logger.Debug("signaling partner hung up before sending candidates")

				// XXX: if our signaling partner hung up while we were gathering ICE candidates, we
				// interpret that signal to mean that our current STUN cohort is too slow, and we should
				// take our chances with a new cohort. It's a pretty weak signal, considering that our
				// signaling partner may have hung up for many other reasons. And "too slow" is a bit of
				// an ambiguous idea, because every censored peer's STUN cohort is *expected* to contain
				// one or more unreachable STUN server at all times, which means that a censored peer's ICE
				// gathering duration is *expected* to be the worst case every time. So if our network is
				// functioning coherently, nobody should be hanging up so hastily while their signaling partner
				// is performing the ICE gathering step. Thus, dropping the cohort here is basically just
				// voodoo, but it's probably harmless voodoo.
				scache.drop()
				logger.Debug("dropped the current STUN cohort", "reason", "signaling-partner-hung-up")

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusOK:
				// Our signaling message was delivered, proceed
			default:
				logger.Debug("unexpected http status code signaling answer", "status_code", res.StatusCode)
				<-time.After(options.ErrorBackoff)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// The HTTP request is complete
			iceBytes, err := io.ReadAll(res.Body)
			if err != nil {
				logger.Debug("error reading ICE response body", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// TODO: Freddie sends back a 0-length body when our signaling partner doesn't reply.
			// Is that the smartest way to handle this case systemwide?
			if len(iceBytes) == 0 {
				// NB: to receive a 200 OK with a 0-length body indicates that our signaling partner was
				// alive to receive our answer SDP, but subsequently either A) died before they were able
				// to complete ICE gathering and send a list of candidates, or B) took so long to perform
				// ICE gathering that Freddie's TTL for this step expired.
				logger.Info("connection attempt failed",
					"reason", "no-remote-candidates",
					"detail", "partner accepted our answer but sent no ICE candidates (died or timed out during ICE gathering)",
				)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Looks like we got some kind of response. Should be an ICEMsg in a SignalMsg
			replyTo, iceMsg, err := common.DecodeSignalMsg(iceBytes)
			if err != nil {
				logger.Debug("error decoding ICE signal message", "error", err, "msg", string(iceBytes))
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			if iceMsg.(common.ICEMsg).ConsumerSessionID == "" {
				logger.Debug("missing session ID from signaling partner, aborting")
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Record the peer's session and candidates for this attempt so subsequent
			// log lines (including the state-4 outcome) carry them.
			candidates := iceMsg.(common.ICEMsg).Candidates
			curSession = iceMsg.(common.ICEMsg).ConsumerSessionID
			curRemoteCand = candidates

			var remoteAddr net.IP
			var remoteHasNonHostCandidate bool

			// TODO: here we assume valid candidates, but we need to handle the invalid case too
			for _, c := range candidates {
				if c.Typ != webrtc.ICECandidateTypeHost {
					remoteHasNonHostCandidate = true
				}

				// XXX: webrtc.AddICECandidate accepts ICECandidateInit types, which are apparently
				// just serialized ICECandidates?
				err := peerConnection.AddICECandidate(c.ToJSON())
				if err != nil {
					logger.Debug("error adding remote ICE candidate", "error", err)
					// Borked!
					peerConnection.Close() // TODO: there's an err we should handle here
					return 0, []interface{}{}
				}

				// We extract an address from the remote ICE candidates just to send it to the UI for
				// geolocation purposes. Under the assumption that any public address will suffice, we
				// arbitrarily select the last public address found in the list of candidates
				parsedIP := net.ParseIP(c.Address)
				if parsedIP != nil && common.IsPublicAddr(parsedIP) {
					remoteAddr = parsedIP
				}
			}
			// Now that we know the peer address and session, rebuild the logger so this
			// and later states tag every line with them.
			curPeer = remoteAddr
			logger = plog(pSignalAnswer).With("peer_tag", offer.Tag, "peer_country", offer.Country)
			logger.Debug("received peer ICE candidates", "remote_candidates", formatCandidates(candidates))

			// As of 003c9ef0fe25677ee832e1351fb1474057a3e4c9, our signaling partner should not have sent
			// us ICE candidates unless they contained at least one non-host type candidate. However, we
			// perform this check on the producer side because some consumers may still on an old version.
			if !remoteHasNonHostCandidate {
				logger.Info("connection attempt failed",
					"reason", "remote-only-host-candidates",
					"detail", "partner sent only host-type ICE candidates (their STUN likely failed)",
					"remote_candidates", formatCandidates(candidates),
				)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			return 4, []interface{}{
				peerConnection,
				connectionEstablished,
				connectionChange,
				connectionClosed,
				remoteAddr,
				offer,
				iceMsg.(common.ICEMsg).ConsumerSessionID,
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 4
			// input[0]: *webrtc.PeerConnection
			// input[1]: chan *webrtc.DataChannel
			// input[2]: chan webrtc.PeerConnectionState
			// input[3]: chan struct{}
			// input[4]: net.IP
			// input[5]: common.OfferMsg
			// input[6]: string
			peerConnection := input[0].(*webrtc.PeerConnection)
			connectionEstablished := input[1].(chan *webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			remoteAddr := input[4].(net.IP)
			offer := input[5].(common.OfferMsg)
			consumerSessionID := input[6].(string)

			logger := plog(pAwaitConn)
			logger.Debug("signaling complete, awaiting NAT traversal")

			select {
			case <-ctx.Done():
				peerConnection.Close()
				return 0, []interface{}{}
			case d := <-connectionEstablished:
				logger.Debug("WebRTC connection established",
					"remote_candidates", formatCandidates(curRemoteCand),
					"local_candidates", formatCandidates(curLocalCand),
				)
				return 5, []interface{}{
					peerConnection,
					d,
					connectionChange,
					connectionClosed,
					remoteAddr,
					offer,
					consumerSessionID,
				}
			case <-time.After(options.NATFailTimeout):
				// The timer fired before the datachannel opened. That chain is
				// ICE -> DTLS -> SCTP -> datachannel, so a bare "timeout" conflates
				// very different failures. Use the last ICE/peer-connection state to
				// classify what actually went wrong.
				iceState, _ := curICEState.Load().(string)
				pcState, _ := curPCState.Load().(string)

				reason := "nat-traversal-timeout"
				detail := "ICE did not complete before the timeout: peer likely unreachable via STUN (symmetric NAT / CGNAT on either end, and we have no TURN), or the timeout is too short for checks to finish"
				switch iceState {
				case "connected", "completed":
					// ICE actually succeeded; the datachannel just wasn't up yet.
					// This is NOT a NAT problem — the timeout is too short for the
					// DTLS/SCTP handshake (covert-dtls mimicry adds latency).
					reason = "handshake-timeout"
					detail = "ICE connectivity succeeded but the datachannel had not opened when the timeout fired (DTLS/SCTP slower than NATFailTimeout); a longer timeout would likely let this connection through"
				case "failed":
					reason = "ice-failed"
					detail = "ICE exhausted all candidate pairs without finding a working path to the peer; no TURN fallback exists"
				}

				logger.Info("connection attempt failed",
					"reason", reason,
					"detail", detail,
					"ice_state", iceState,
					"conn_state", pcState,
					"timeout", options.NATFailTimeout,
					"remote_candidates", formatCandidates(curRemoteCand),
					"local_candidates", formatCandidates(curLocalCand),
				)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// XXX: This loop represents an alternate strategy for detecting NAT traversal success or
			// failure based on peerConnection state changes. Notably, this strategy explicitly waits
			// for the peerConnection failure event (instead of giving up after a timeout). This strategy
			// is more "correct" than the one employed above, but when compared to using a short timeout
			// value, it's very inefficient. In practice, if NAT traversal is destined to succeed, it will
			// succeed within ~5s, but ICE often requires ~20s to conclude that a connection has failed.
			/**
			  for {
			    s := <-connectionChange

			    if s == webrtc.PeerConnectionStateConnected {
			      common.Debugf("A WebRTC connection has been established!")
			      d := <-connectionEstablished

			      return 5, []interface{}{
			        peerConnection,
			        d,
			        connectionChange,
			        connectionClosed,
			        remoteAddr,
			        offer,
			      }
			    } else if s == webrtc.PeerConnectionStateFailed {
			      common.Debugf("NAT traversal failed, aborting!")
			      // Borked!
			      peerConnection.Close() // TODO: there's an err we should handle here
			      return 0, []interface{}{}
			    }
			  }
			*/
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 5
			// input[0]: *webrtc.PeerConnection
			// input[1]: *webrtc.DataChannel
			// input[2]: chan webrtc.PeerConnectionState
			// input[3]: chan struct{}
			// input[4]: net.IP
			// input[5]: common.OfferMsg
			// input[6]: string
			peerConnection := input[0].(*webrtc.PeerConnection)
			d := input[1].(*webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			remoteAddr := input[4].(net.IP)
			offer := input[5].(common.OfferMsg)
			consumerSessionID := input[6].(string)

			logger := plog(pProxy)
			logger.Debug("entering proxy state")

			// Announce the new connectivity situation for this slot
			if !sendCtx(ctx, com.tx, IPCMsg{
				IpcType: ConsumerInfoIPC,
				Data:    common.ConsumerInfo{Addr: remoteAddr, Tag: offer.Tag, SessionID: consumerSessionID, Country: offer.Country},
			}) {
				return 0, input
			}

			// Inbound from datachannel (consumer → widget) and outbound
			// (widget → consumer) counters + 1s summary. Mirrors the
			// consumer-side instrumentation so both ends of the
			// datachannel are visible from the logs. Gated on
			// BROFLAKE_STATS; zero cost in production.
			var pdcRxBytes, pdcRxMsgs, pdcRxDrops atomic.Uint64
			var pdcTxBytes, pdcTxMsgs atomic.Uint64
			d.OnMessage(func(msg webrtc.DataChannelMessage) {
				select {
				case com.tx <- IPCMsg{IpcType: ChunkIPC, Data: msg.Data}:
					if bfStatsEnabled {
						pdcRxBytes.Add(uint64(len(msg.Data)))
						pdcRxMsgs.Add(1)
					}
				default:
					// Drop the chunk if we can't keep up with the data rate
					if bfStatsEnabled {
						pdcRxDrops.Add(1)
					}
				}
			})
			pdcStatsDone := make(chan struct{})
			if bfStatsEnabled {
				go func() {
					t := time.NewTicker(time.Second)
					defer t.Stop()
					var lastRB, lastRM, lastRD, lastTB, lastTM uint64
					for {
						select {
						case <-pdcStatsDone:
							return
						case <-t.C:
							rb, rm, rd := pdcRxBytes.Load(), pdcRxMsgs.Load(), pdcRxDrops.Load()
							tb, tm := pdcTxBytes.Load(), pdcTxMsgs.Load()
							dRB, dRM, dRD := rb-lastRB, rm-lastRM, rd-lastRD
							dTB, dTM := tb-lastTB, tm-lastTM
							lastRB, lastRM, lastRD, lastTB, lastTM = rb, rm, rd, tb, tm
							if dRB+dTB+dRD > 0 {
								logger.Debug("widget datachannel 1s", "rx_msgs", dRM, "rx_bytes", dRB, "rx_drops", dRD, "tx_msgs", dTM, "tx_bytes", dTB)

							}
						}
					}
				}()
				defer close(pdcStatsDone)
			}

		proxyloop:
			for {
				select {
				// XXX: there's likely a race or dead code here, see: https://github.com/getlantern/engineering/issues/2320
				// Handle connection failure
				case s := <-connectionChange:
					if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected {
						logger.Debug("connection failed mid-session, resetting", "conn_state", s.String())
						break proxyloop
					} else if s == webrtc.PeerConnectionStateClosed {
						logger.Debug("connection closed, resetting")
						break proxyloop
					}
				// Handle connection failure for Firefox
				case _ = <-connectionClosed:
					logger.Debug("connection closed (datachannel), resetting")
					break proxyloop
				// Handle messages from the router
				case msg := <-com.rx:
					switch msg.IpcType {
					case ChunkIPC:
						payload := msg.Data.([]byte)
						if err := d.Send(payload); err != nil {
							logger.Debug("error sending to datachannel, resetting", "bytes", len(payload), "error", err)
							break proxyloop
						}
						if bfStatsEnabled {
							pdcTxBytes.Add(uint64(len(payload)))
							pdcTxMsgs.Add(1)
						}
					case PathAssertionIPC:
						pa := msg.Data.(common.PathAssertion)
						if pa.Nil() {
							// Nil path assertion signals upstream worker reset; disconnect the downstream peer.
							// TODO: clean this up (https://github.com/getlantern/engineering/issues/2402)
							logger.Debug("upstream worker reset, disconnecting peer")
							break proxyloop
						}
					}
				// Since we're putting this state into an infinite loop, explicitly handle cancellation
				case <-ctx.Done():
					break proxyloop
				}
			}

			peerConnection.Close() // TODO: there's an err we should handle here

			// We've reset this slot, so announce the nil connectivity situation
			if !sendCtx(ctx, com.tx, IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{}}) {
				return 0, input
			}
			return 0, []interface{}{}
		}),
	})
}

func newProducerPeerConnection(config webrtc.Configuration, dtls covertdtls.Config) (*webrtc.PeerConnection, error) {
	if !dtls.Enabled() {
		return webrtc.NewPeerConnection(config)
	}
	se := webrtc.SettingEngine{}
	if err := covertdtls.Apply(dtls, &se); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(config)
}
