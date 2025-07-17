// consumer.go implements standard consumer behavior over WebRTC, including the discovery process,
// signaling, connection establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=0

package clientcore

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/otel"
	"github.com/pion/webrtc/v4"
)

func NewConsumerWebRTC(options *WebRTCOptions, wg *sync.WaitGroup) *WorkerFSM {
	var scache STUNCache

	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			common.Debugf("Consumer state 0, constructing RTCPeerConnection...")

			// We're resetting this slot, so send a nil path assertion IPC message
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}

			// Populate the STUN cache if necessary
			if scache.size() == 0 {
				allSTUNSrvs, err := options.STUNBatch(math.MaxInt32)
				if err != nil {
					common.Debugf("Error creating STUN batch: %v", err)
					return 0, []interface{}{}
				}

				scache = newSTUNCache(allSTUNSrvs, float64(options.STUNBatchSize))
				common.Debugf("Populated the STUN cache (%v servers)", scache.size())
			}

			STUNSrvs := scache.cohort()
			common.Debugf("Using %v/%v STUN servers: %v", len(STUNSrvs), options.STUNBatchSize, STUNSrvs)
			common.Debugf("STUN cache size: %v", scache.size())

			config := webrtc.Configuration{
				ICEServers: []webrtc.ICEServer{
					{
						URLs: STUNSrvs,
					},
				},
			}

			// // Example custom DTLS settings
			// settingEngine := &webrtc.SettingEngine{}
			// settingEngine.SetDTLSEllipticCurves(elliptic.P256, elliptic.P384, elliptic.X25519)
			// webrtcAPI := webrtc.NewAPI(webrtc.WithSettingEngine(*settingEngine))
			// peerConnection, err := webrtcAPI.NewPeerConnection(config)

			// Construct the RTCPeerConnection
			peerConnection, err := webrtc.NewPeerConnection(config)
			if err != nil {
				common.Debugf("Error creating RTCPeerConnection: %v", err)
				return 0, []interface{}{}
			}

			// Consumers are the offerers, so we must create a datachannel
			// The following configuration creates a UDP-like unreliable channel
			dataChannelConfig := webrtc.DataChannelInit{Ordered: new(bool), MaxRetransmits: new(uint16)}
			d, err := peerConnection.CreateDataChannel("data", &dataChannelConfig)
			if err != nil {
				common.Debugf("Error creating WebRTC datachannel: %v", err)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// We want to make sure we capture the connection establishment event whenever it happens,
			// but we also want to avoid control flow spaghetti (it would very hard to reason about
			// client operation if we sometimes jump forward to future states based on async events
			// firing outside of the state machine). Solution: Pass forward this buffered channel such
			// that we can explicitly check for connection establishment in state 4. In theory, it's
			// possible that magical ICE mysteries could cause the connection to open as early as the end
			// of state 2. In practice, the differences here should be on the order of nanoseconds. But
			// we should monitor the logs to see if connections open too long before we check for them.
			connectionEstablished := make(chan *webrtc.DataChannel, 1)

			d.OnOpen(func() {
				common.Debugf("A datachannel has opened!")
				connectionEstablished <- d
			})

			// connectionClosed (and the OnClose handler below) is implemented for Firefox, the only
			// browser which doesn't implement WebRTC's onconnectionstatechange event. We listen for both
			// onclose and onconnectionstatechange under the assumption that non-Firefox browsers can
			// benefit from faster connection failure detection by listening for the `failed` event.
			connectionClosed := make(chan struct{}, 1)
			d.OnClose(func() {
				common.Debugf("A datachannel has closed!")
				connectionClosed <- struct{}{}
			})

			// Ditto, but for connection state changes
			connectionChange := make(chan webrtc.PeerConnectionState, 16)
			peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
				common.Debugf("Peer connection state change: %v", s.String())
				connectionChange <- s
			})

			// TODO: right now we listen for ICE connection state changes only to log messages about
			// client behavior. In the future, by passing a channel forward in the same manner as above,
			// we could probably use the ICE connection state change event to determine the precise
			// moment of NAT traversal failure (instead of just waiting on a timer).
			peerConnection.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
				common.Debugf("ICE connection state change: %v", s.String())
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
			common.Debugf("Consumer state 1...")

			// Listen for genesis messages
			req, err := http.NewRequestWithContext(
				ctx,
				"GET",
				options.DiscoverySrv+options.Endpoint,
				nil,
			)
			if err != nil {
				common.Debugf("Error constructing request")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HTTPClient.Do(req)
			if err != nil {
				common.Debugf("Couldn't subscribe to genesis stream at %v: %v", options.DiscoverySrv+options.Endpoint, err)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			defer res.Body.Close()

			// Handle bad protocol version
			if res.StatusCode == http.StatusTeapot {
				common.Debugf("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// We make a long-lived HTTP request to Freddie. Freddie streams newline-terminated genesis
			// messages as they become available. We wait until we hear one genesis message, then continue
			// listening for a tunable amount of time ("patience") to see if we might hear a few more
			// messages to select from. When either our patience expires or our HTTP request times out, we
			// pick a random message from the set we've collected and make an offer for it.
			scanner := bufio.NewScanner(res.Body)
			genesisMsg := make(chan struct{})
			reqTimeout := make(chan struct{})
			patienceExpired := make(<-chan time.Time)
			doneListening := make(chan struct{}, 1)
			genesisCandidates := []string{}

		listenLoop:
			for {
				go func() {
					isReqOpen := scanner.Scan()

					if len(doneListening) > 0 {
						return
					}

					if isReqOpen {
						genesisMsg <- struct{}{}
						return
					}

					reqTimeout <- struct{}{}
				}()

				select {
				case <-genesisMsg:
					rawMsg := scanner.Bytes()
					if err := scanner.Err(); err != nil {
						// TODO: what does this error mean? Should we be returning to state 1?
						return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
					}

					rt, _, err := common.DecodeSignalMsg(rawMsg)
					if err != nil {
						common.Debugf("Error decoding signal message: %v (msg: %v)", err, string(rawMsg))
						<-time.After(options.ErrorBackoff)
						// Take the error in stride, continue listening to our existing HTTP request stream
						continue
					}

					// TODO: post-MVP, evaluate the genesis message for suitability!
					genesisCandidates = append(genesisCandidates, rt)
					if len(genesisCandidates) == 1 {
						patienceExpired = time.After(options.Patience)
					}
				case <-reqTimeout:
					break listenLoop
				case <-patienceExpired:
					break listenLoop
				}
			}

			doneListening <- struct{}{}

			// Endgame case 1: we never heard any suitable genesis messages, so just restart this state
			if len(genesisCandidates) == 0 {
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Endgame case 2: create an offer SDP, pick a random genesis candidate, and shoot our shot
			sdp, err := peerConnection.CreateOffer(nil)
			if err != nil {
				// An error creating the offer is troubling, so let's start fresh by resetting the state
				common.Debugf("Error creating offer SDP: %v", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			idx := rand.Intn(len(genesisCandidates))
			replyTo := genesisCandidates[idx]

			common.Debugf(
				"Sending offer for genesis message %v/%v (patience: %v)",
				idx+1,
				len(genesisCandidates),
				options.Patience,
			)

			return 2, []interface{}{
				peerConnection,
				replyTo,
				sdp,
				connectionEstablished,
				connectionChange,
				connectionClosed,
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 2
			// input[0]: *webrtc.PeerConnection
			// input[1]: string (reply-to UUID)
			// input[2]: webrtc.SessionDescription (offer)
			// input[3]: chan *webrtc.DataChannel
			// input[4]: chan webrtc.PeerConnectionState
			// input[5]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			replyTo := input[1].(string)
			sdp := input[2].(webrtc.SessionDescription)
			connectionEstablished := input[3].(chan *webrtc.DataChannel)
			connectionChange := input[4].(chan webrtc.PeerConnectionState)
			connectionClosed := input[5].(chan struct{})
			common.Debugf("Consumer state 2...")

			offerJSON, err := json.Marshal(common.OfferMsg{SDP: sdp, Tag: options.Tag})
			if err != nil {
				common.Debugf("Error marshaling JSON: %v", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Signal the offer
			form := url.Values{
				"data":    {string(offerJSON)},
				"send-to": {replyTo},
				"type":    {strconv.Itoa(int(common.SignalMsgOffer))},
			}

			req, err := http.NewRequestWithContext(
				ctx,
				"POST",
				options.DiscoverySrv+options.Endpoint,
				strings.NewReader(form.Encode()),
			)
			if err != nil {
				common.Debugf("Error constructing request")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HTTPClient.Do(req)
			if err != nil {
				common.Debugf("Couldn't signal offer SDP to %v: %v", options.DiscoverySrv+options.Endpoint, err)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			defer res.Body.Close()

			switch res.StatusCode {
			case http.StatusTeapot:
				common.Debugf("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			case http.StatusNotFound:
				// We didn't win the connection
				common.Debugf("Too late for genesis message %v! Got %v", replyTo, res.Status)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			case http.StatusOK:
				// We won the connection, proceed
			default:
				// Unexpected bad stuff
				common.Debugf("Unexpected http status code: %v", res.StatusCode)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// The HTTP request is complete
			answerBytes, err := io.ReadAll(res.Body)
			if err != nil {
				common.Debugf("Error reading body: %v\n", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: Freddie sends back a 0-length body when nobody replied to our message. Is that the
			// smartest way to handle this case systemwide?
			if len(answerBytes) == 0 {
				// NB: to receive a 200 OK with a 0-length body here indicates that our signaling partner
				// was alive to receive our offer and accepted it, but subsequently either A) died before
				// they were able to perform ICE gathering and send back an answer, or B) took so long to
				// perform ICE gathering that Freddie's TTL for this step expired.
				common.Debugf("No response for our offer SDP!")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Looks like we got some kind of response. Should be an answer SDP in a SignalMsg
			replyTo, answer, err := common.DecodeSignalMsg(answerBytes)
			if err != nil {
				common.Debugf("Error decoding signal message: %v (msg: %v)", err, string(answerBytes))
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: here we assume valid answer SDP, but we need to handle the invalid case too

			// Create a channel that's blocked until ICE gathering is complete
			gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

			candidates := []webrtc.ICECandidate{}
			peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
				// Interestingly, the null candidate is a nil pointer so we cause a nil ptr dereference
				// if we try to append it to the list... so let's just not include it?
				if c != nil {
					candidates = append(candidates, *c)
				}
			})

			// This kicks off ICE candidate gathering
			err = peerConnection.SetLocalDescription(sdp)
			if err != nil {
				common.Debugf("Error setting local description: %v", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Assign the answer to our connection
			err = peerConnection.SetRemoteDescription(answer.(webrtc.SessionDescription))
			if err != nil {
				common.Debugf("Error setting remote description: %v", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			<-gatherComplete
			common.Debug("ICE gathering complete!")
			common.Debugf("Local candidates: %v", candidates)

			// If the STUN server(s) we used for this signaling attempt were blocked or unresponsive,
			// we probably wound up with a slice of valid ICE candidates, but of only the 'host' type.
			// We don't want to bother signaling those, so here's our escape hatch.
			var hasNonHostCandidate bool
			for _, c := range candidates {
				if c.Typ != webrtc.ICECandidateTypeHost {
					hasNonHostCandidate = true
				}
			}

			if !hasNonHostCandidate {
				common.Debugf("ICE failed to gather any non-host candidates, aborting!")
				scache.drop()
				common.Debugf("Dropped the current STUN cohort (reason: ICE failed)")

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			return 3, []interface{}{peerConnection, replyTo, candidates, connectionEstablished, connectionChange, connectionClosed}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 3
			// input[0]: *webrtc.PeerConnection
			// input[1]: string (replyTo)
			// input[2]: []webrtc.ICECandidates
			// input[3]: chan *webrtc.DataChannel
			// input[4]: chan webrtc.PeerConnectionState
			// input[5]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			replyTo := input[1].(string)
			candidates := input[2].([]webrtc.ICECandidate)
			connectionEstablished := input[3].(chan *webrtc.DataChannel)
			connectionChange := input[4].(chan webrtc.PeerConnectionState)
			connectionClosed := input[5].(chan struct{})
			common.Debugf("Consumer state 3...")

			iceMsg := common.ICEMsg{
				Candidates:        candidates,
				ConsumerSessionID: options.ConsumerSessionID,
			}

			iceMsgJSON, err := json.Marshal(iceMsg)
			if err != nil {
				common.Debugf("Error marshaling JSON: %v", err)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Signal our ICE candidates
			form := url.Values{
				"data":    {string(iceMsgJSON)},
				"send-to": {replyTo},
				"type":    {strconv.Itoa(int(common.SignalMsgICE))},
			}

			req, err := http.NewRequestWithContext(
				ctx,
				"POST",
				options.DiscoverySrv+options.Endpoint,
				strings.NewReader(form.Encode()),
			)
			if err != nil {
				common.Debugf("Error constructing request")
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HTTPClient.Do(req)
			if err != nil {
				common.Debugf("Couldn't signal ICE candidates to %v: %v", options.DiscoverySrv+options.Endpoint, err)
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}
			defer res.Body.Close()

			switch res.StatusCode {
			case http.StatusTeapot:
				common.Debugf("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusNotFound:
				common.Debugf("Signaling partner hung up, aborting!")

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
				common.Debugf("Dropped the current STUN cohort (reason: signaling partner hung up)")

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusOK:
				// Signaling is complete, so we can short circuit instead of awaiting the response body
				return 4, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			default:
				// Borked!
				common.Debugf("Received unexpected status code: %v", res.StatusCode)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 4
			// input[0]: *webrtc.PeerConnection
			// input[1]: chan *webrtc.DataChannel
			// input[2]: chan webrtc.PeerConnectionState
			// input[3]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			connectionEstablished := input[1].(chan *webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			common.Debugf("Consumer state 4, signaling complete!")

			// XXX: Use our current cohort of STUN servers to perform NAT behavior discovery such that we
			// can send interesting traces revealing the outcome of our NAT traversal attempt. If the
			// cohort fails here, we won't drop it.
			STUNSrvs := scache.cohort()

			select {
			case d := <-connectionEstablished:
				common.Debugf("A WebRTC connection has been established!")
				go otel.CollectAndSendNATBehaviorTelemetry(STUNSrvs, "nat_success")
				return 5, []interface{}{peerConnection, d, connectionChange, connectionClosed}
			case <-time.After(options.NATFailTimeout):
				common.Debugf("NAT failure, aborting!")
				go otel.CollectAndSendNATBehaviorTelemetry(STUNSrvs, "nat_failure")
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 5
			// input[0]: *webrtc.PeerConnection
			// input[1]: *webrtc.DataChannel
			// input[2]: chan webrtc.PeerConnectionState
			// input[3]: chan struct{}
			peerConnection := input[0].(*webrtc.PeerConnection)
			d := input[1].(*webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})

			// Send a path assertion IPC message representing the connectivity now provided by this slot
			// TODO: post-MVP we shouldn't be hardcoding (*, 1) here...
			allowAll := []common.Endpoint{{Host: "*", Distance: 1}}
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: allowAll}}

			// Inbound from datachannel:
			d.OnMessage(func(msg webrtc.DataChannelMessage) {
				select {
				case com.tx <- IPCMsg{IpcType: ChunkIPC, Data: msg.Data}:
					// Do nothing, msg sent
				default:
					// Drop the chunk if we can't keep up with the data rate
				}
			})

		proxyloop:
			for {
				select {
				// XXX: there's likely a race or dead code here, see: https://github.com/getlantern/engineering/issues/2320
				// Handle connection failure
				case s := <-connectionChange:
					if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected {
						common.Debugf("Connection failure, resetting!")
						break proxyloop
					} else if s == webrtc.PeerConnectionStateClosed {
						common.Debugf("Connection closed, resetting!")
						break proxyloop
					}
				// Handle connection failure for Firefox
				case _ = <-connectionClosed:
					common.Debugf("Firefox connection failure, resetting!")
					break proxyloop
					// Handle messages from the router
				case msg := <-com.rx:
					switch msg.IpcType {
					case ChunkIPC:
						if err := d.Send(msg.Data.([]byte)); err != nil {
							common.Debugf("Error sending to datachannel, resetting!")
							break proxyloop
						}
					}
					// Since we're putting this state into an infinite loop, explicitly handle cancellation
				case <-ctx.Done():
					break proxyloop
				}
			}

			peerConnection.Close() // TODO: there's an err we should handle here
			return 0, []interface{}{}
		}),
	})
}
