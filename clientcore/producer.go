// producer.go defines standard producer behavior over WebRTC, including the discovery process,
// signaling, connection establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=471342300
package clientcore

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

func NewProducerWebRTC(options *WebRTCOptions, wg *sync.WaitGroup) *WorkerFSM {
	var scache STUNCache

	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			common.Debugf("Producer state 0, constructing RTCPeerConnection...")

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
				common.Debugf("Created new datachannel...")

				d.OnOpen(func() {
					common.Debugf("A datachannel has opened!")
					connectionEstablished <- d
				})

				d.OnClose(func() {
					common.Debugf("A datachannel has closed!")
					connectionClosed <- struct{}{}
				})
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
			common.Debugf("Producer state 1...")

			// Do we have a non-nil path assertion, indicating that we have upstream connectivity to share?
			// We find out by sending an ConnectivityCheckIPC message, which asks the process responsible
			// for path assertions to send a message reflecting the current state of our path assertion.
			// If yes, we can proceed right now! If no, just wait for the next non-nil path assertion message...
			com.tx <- IPCMsg{IpcType: ConnectivityCheckIPC}

			for {
				select {
				// Handle inbound IPC messages, wait for a non-nil path assertion
				case msg := <-com.rx:
					if msg.IpcType == PathAssertionIPC && !msg.Data.(common.PathAssertion).Nil() {
						pa := msg.Data.(common.PathAssertion)
						return 2, []interface{}{peerConnection, pa, connectionEstablished, connectionChange, connectionClosed}
					}
				// Since we're putting this state into an infinite loop, explicitly handle cancellation
				case <-ctx.Done():
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
			common.Debugf("Producer state 2...")

			// Construct a genesis message
			g, err := json.Marshal(common.GenesisMsg{PathAssertion: pa})
			if err != nil {
				common.Debugf("Error marshaling JSON: %v", err)
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
				common.Debugf("Error constructing request")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HttpClient.Do(req)
			if err != nil {
				common.Debugf("Couldn't signal genesis message to %v: %v", options.DiscoverySrv+options.Endpoint, err)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			defer res.Body.Close()

			// Freddie never returns 404s for genesis messages, so we're not catching that case here

			// Handle bad protocol version
			if res.StatusCode == http.StatusTeapot {
				common.Debugf("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// The HTTP request is complete
			offerBytes, err := io.ReadAll(res.Body)
			if err != nil {
				common.Debugf("Error reading body: %v\n", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: Freddie sends back a 0-length body when nobody replied to our message. Is that the
			// smartest way to handle this case systemwide?
			if len(offerBytes) == 0 {
				common.Debugf("No answer for genesis message!")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Looks like we got some kind of response. It ought to be an offer SDP wrapped in a SignalMsg
			replyTo, offer, err := common.DecodeSignalMsg(offerBytes)
			if err != nil {
				common.Debugf("Error decoding signal message: %v (msg: %v)", err, string(offerBytes))
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
			common.Debugf("Producer state 3...")

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
				common.Debugf("Error setting remote description: %v", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Generate an answer
			answer, err := peerConnection.CreateAnswer(nil)
			if err != nil {
				common.Debugf("Error creating answer SDP: %v", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// This kicks off ICE candidate gathering
			err = peerConnection.SetLocalDescription(answer)
			if err != nil {
				common.Debugf("Error setting local description: %v", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			<-gatherComplete
			common.Debug("ICE gathering complete!")
			common.Debugf("Local candidates: %v", localCandidates)

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
				common.Debugf("ICE failed to gather any non-host candidates, aborting!")
				scache.drop()
				common.Debugf("Dropped the current STUN cohort (reason: ICE failed)")

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Our answer SDP with ICE candidates attached
			finalAnswer := peerConnection.LocalDescription()

			a, err := json.Marshal(finalAnswer)
			if err != nil {
				common.Debugf("Error marshaling JSON: %v", err)
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
				common.Debugf("Error constructing request")
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Add(common.VersionHeader, common.Version)

			res, err := options.HttpClient.Do(req)
			if err != nil {
				common.Debugf("Couldn't signal answer SDP to %v: %v", options.DiscoverySrv+options.Endpoint, err)
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
				// Our signaling message was delivered, proceed
			default:
				// Unexpected bad stuff
				common.Debugf("Unexpected http status code: %v", res.StatusCode)
				<-time.After(options.ErrorBackoff)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// The HTTP request is complete
			iceBytes, err := io.ReadAll(res.Body)
			if err != nil {
				common.Debugf("Error reading body: %v\n", err)
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
				common.Debugf("No ICE candidates from signaling partner!")
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Looks like we got some kind of response. Should be a slice of ICE candidates in a SignalMsg
			replyTo, remoteCandidates, err := common.DecodeSignalMsg(iceBytes)
			if err != nil {
				common.Debugf("Error decoding signal message: %v (msg: %v)", err, string(iceBytes))
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			var remoteAddr net.IP
			var remoteHasNonHostCandidate bool

			// TODO: here we assume valid candidates, but we need to handle the invalid case too
			for _, c := range remoteCandidates.([]webrtc.ICECandidate) {
				if c.Typ != webrtc.ICECandidateTypeHost {
					remoteHasNonHostCandidate = true
				}

				// XXX: webrtc.AddICECandidate accepts ICECandidateInit types, which are apparently
				// just serialized ICECandidates?
				err := peerConnection.AddICECandidate(c.ToJSON())
				if err != nil {
					common.Debugf("Error adding ICE candidate: %v", err)
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

			// As of 003c9ef0fe25677ee832e1351fb1474057a3e4c9, our signaling partner should not have sent
			// us ICE candidates unless they contained at least one non-host type candidate. However, we
			// perform this check on the producer side because some consumers may still on an old version.
			if !remoteHasNonHostCandidate {
				common.Debugf("Signaling partner sent only host type ICE candidates, aborting!")
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
			peerConnection := input[0].(*webrtc.PeerConnection)
			connectionEstablished := input[1].(chan *webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			remoteAddr := input[4].(net.IP)
			offer := input[5].(common.OfferMsg)
			common.Debugf("Producer state 4, signaling complete!")

			select {
			case d := <-connectionEstablished:
				common.Debugf("A WebRTC connection has been established!")
				return 5, []interface{}{
					peerConnection,
					d,
					connectionChange,
					connectionClosed,
					remoteAddr,
					offer,
				}
			case <-time.After(options.NATFailTimeout):
				common.Debugf("NAT traversal timeout, aborting!")
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
			peerConnection := input[0].(*webrtc.PeerConnection)
			d := input[1].(*webrtc.DataChannel)
			connectionChange := input[2].(chan webrtc.PeerConnectionState)
			connectionClosed := input[3].(chan struct{})
			remoteAddr := input[4].(net.IP)
			offer := input[5].(common.OfferMsg)
			common.Debugf("Producer state 5...")

			// Announce the new connectivity situation for this slot
			com.tx <- IPCMsg{
				IpcType: ConsumerInfoIPC,
				Data:    common.ConsumerInfo{Addr: remoteAddr, Tag: offer.Tag},
			}

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

			// We've reset this slot, so announce the nil connectivity situation
			com.tx <- IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{}}
			return 0, []interface{}{}
		}),
	})
}
