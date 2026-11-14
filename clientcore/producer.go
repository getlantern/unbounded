// producer.go defines standard producer behavior over WebRTC, including the discovery process,
// signaling, connection establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=471342300
package clientcore

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/covertdtls"
	"github.com/getlantern/broflake/common/helperticket"
)

func NewProducerWebRTC(options *WebRTCOptions, wg *sync.WaitGroup) *WorkerFSM {
	return newProducerWebRTC(options, wg, false, newVolunteerSignaler(options))
}

// newProducerWebRTC builds a producer worker. A room slot offers itself only to its rooms
// whenever it has room tickets, even while the producer serves the pool. Freddie offers a genesis
// to a room only if one of its consumers is waiting at that moment, and otherwise to the pool,
// so a producer whose every slot serves the pool can be busy with strangers whenever its own
// inviter looks for it. The room slot stays free for the inviter instead. A producer's workers
// share signaler, and with it their socket to Freddie.
func newProducerWebRTC(options *WebRTCOptions, wg *sync.WaitGroup, roomSlot bool, signaler *volunteerSignaler) *WorkerFSM {
	var scache STUNCache
	var preflight preflightState
	var preflightRep preflightReport
	// stunPausedUntil is when a producer with a mapped address next tries STUN after a failed
	// STUN check, and listPausedUntil when it next fetches the STUN list after a failed fetch.
	var stunPausedUntil, listPausedUntil time.Time
	var listBackoff retryBackoff
	// cohortDropped is set once the current attempt has dropped its STUN cohort, or when it used
	// none, so it never drops one it didn't try: at most its own, not also the next, untried.
	var cohortDropped bool
	// roomsWithoutSTUN is set when the current attempt went without STUN only because it serves
	// its rooms alone, so state 2 sends it back to state 0 if it no longer does, or once STUN is
	// due to be tried again.
	var roomsWithoutSTUN bool
	// stunRetryDue reports whether state 0 would try STUN again, after the pause that followed a
	// failed STUN check and the one that followed a failed STUN list fetch, if it needs the list.
	stunRetryDue := func() bool {
		now := preflightNow()
		return !now.Before(stunPausedUntil) && (scache.size() > 0 || !now.Before(listPausedUntil))
	}
	// last is how this worker's previous attempt ended, reported on its next genesis message.
	// A worker's states run on one goroutine, so it needs no lock.
	var last attemptOutcome

	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			slog.Debug("Producer state 0, constructing RTCPeerConnection...")

			// A producer with a mapped address is reached on it whatever STUN does, so for it STUN
			// is best-effort: its server-reflexive candidates are another way in, but if the STUN
			// list can't be fetched or the STUN check fails, it takes offers on its mapped address
			// alone. After a failed check it skips STUN until the check's backoff ends, rather
			// than wait out a failing check before every offer. STUN is best-effort in the same way
			// for an attempt that serves rooms alone and has room tickets, since Freddie may hand it
			// TURN servers with its room's offer: one that gets none gathers only host candidates and gives up in
			// state 3 rather than here.
			mapped := iceNetOf(options).mapped()
			stunOptional := mapped || servesTicketedRoomsOnly(options, roomSlot)
			config := webrtc.Configuration{}
			useSTUN := false
			cohortDropped = false
			if !stunOptional || !preflightNow().Before(stunPausedUntil) {
				// Populate the STUN cache if necessary
				if scache.size() == 0 && (!stunOptional || !preflightNow().Before(listPausedUntil)) {
					allSTUNSrvs, err := options.STUNBatch(math.MaxInt32)
					if err != nil {
						if !stunOptional {
							slog.Debug("Error creating STUN batch", "error", err)
							return 0, []interface{}{}
						}
						wait := listBackoff.failed()
						listPausedUntil = preflightNow().Add(wait)
						slog.Debug("Error creating STUN batch, going on without STUN", "error", err, "retry_in", wait, "mapped", mapped)
					} else if len(allSTUNSrvs) == 0 && stunOptional && !mapped {
						// A room attempt going without STUN would otherwise come straight back here for an
						// empty list, so it waits as for a failed fetch.
						wait := listBackoff.failed()
						listPausedUntil = preflightNow().Add(wait)
						slog.Debug("Empty STUN batch, going on without STUN", "retry_in", wait)
					} else {
						listBackoff.reset()
						scache = newSTUNCache(allSTUNSrvs, float64(options.STUNBatchSize))
						slog.Debug("Populated the STUN cache", "servers", scache.size())
					}
				}

				STUNSrvs := scache.cohort()
				slog.Debug("Using STUN servers", "count", len(STUNSrvs), "total", options.STUNBatchSize, "servers", STUNSrvs)
				slog.Debug("STUN cache size", "size", scache.size())

				// Confirm the cohort answers before asking for an offer, rather than after taking one.
				useSTUN = !stunOptional || len(STUNSrvs) > 0
				if useSTUN && preflight.due(preflightNow(), STUNSrvs) {
					res := stunPreflight(ctx, options.Net, STUNSrvs, stunPreflightTimeout)
					if options.OnSTUNPreflight != nil {
						options.OnSTUNPreflight(res)
					}
					if res.Outcome != STUNPreflightOK {
						if ctx.Err() != nil {
							return 0, []interface{}{}
						}
						preflightRep.record(res.Outcome, res.Cause, vpnStatus(options))
						scache.drop()
						wait := preflight.failed()
						slog.Debug("STUN preflight failed, dropped the current STUN cohort", "outcome", res.Outcome, "servers", res.Servers, "retry_in", wait, "mapped", mapped)
						if !stunOptional {
							sleepOrDone(ctx, wait)
							return 0, []interface{}{}
						}
						stunPausedUntil = preflightNow().Add(wait)
						useSTUN = false
					} else {
						preflight.passed(preflightNow(), STUNSrvs)
						preflightRep.record(res.Outcome, res.Cause, vpnStatus(options))
					}
				}

				if useSTUN {
					config.ICEServers = []webrtc.ICEServer{{URLs: STUNSrvs}}
				}
			}
			if !useSTUN {
				// An attempt without STUN has no pass to expire, so a pass left from an earlier
				// attempt mustn't send it back here from state 2.
				preflight.invalidate()
				cohortDropped = true
			}
			roomsWithoutSTUN = !useSTUN && !mapped

			// Producers are the answerers, which makes them the active peer in
			// the DTLS handshake per RFC 5763 — they send the ClientHello. The
			// default pion fingerprint is DPI-filtered in Russia (see
			// net4people/bbs#603), so we route through a SettingEngine with a
			// covert-dtls hook when enabled.
			peerConnection, err := newProducerPeerConnection(config, options.CovertDTLS, options.Tuning.checkingFor(options.NATFailTimeout), iceNetOf(options))
			if err != nil {
				slog.Debug("Error creating RTCPeerConnection", "error", err)
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
				slog.Debug("Created new datachannel...")

				d.OnOpen(func() {
					slog.Debug("A datachannel has opened!")
					connectionEstablished <- d
				})

				d.OnClose(func() {
					slog.Debug("A datachannel has closed!")
					connectionClosed <- struct{}{}
				})
			})

			// Ditto, but for connection state changes
			connectionChange := make(chan webrtc.PeerConnectionState, 16)
			peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
				slog.Debug("Peer connection state change", "state", s.String())
				connectionChange <- s
			})

			// TODO: right now we listen for ICE connection state changes only to log messages about
			// client behavior. In the future, by passing a channel forward in the same manner as above,
			// we could probably use the ICE connection state change event to determine the precise
			// moment of NAT traversal failure (instead of just waiting on a timer).
			peerConnection.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
				slog.Debug("ICE connection state change", "state", s.String())
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
			slog.Debug("Producer state 1...")

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
			slog.Debug("Producer state 2...")

			// A worker can loop through states 1 and 2 for longer than a preflight pass lasts, so
			// go back to state 0 to check again rather than take an offer on an expired pass.
			if preflight.expired(preflightNow()) {
				if peerConnection != nil {
					peerConnection.Close()
				}
				return 0, []interface{}{}
			}
			// Likewise an attempt that went without STUN only because it served its rooms alone goes
			// back to state 0 for STUN once it no longer does, rather than offer itself without it,
			// or once STUN is due to be tried again, rather than go without it for as long as its
			// rooms keep it here.
			if roomsWithoutSTUN && (!servesTicketedRoomsOnly(options, roomSlot) || stunRetryDue()) {
				if peerConnection != nil {
					peerConnection.Close()
				}
				return 0, []interface{}{}
			}

			// Construct a genesis message
			g, err := json.Marshal(common.GenesisMsg{PathAssertion: pa})
			if err != nil {
				slog.Debug("Error marshaling JSON", "error", err)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Signal the genesis message
			form := genesisForm(options, g, roomSlot)
			last.addTo(form, options.NATFailTimeout)
			preflightRep.addTo(form)

			statusCode, offerBytes, err := signaler.signal(ctx, form)
			if err != nil {
				// Freddie took the genesis and counted the report, but its reply was lost.
				if statusCode == http.StatusOK {
					preflightRep.delivered()
				}
				slog.Debug("Couldn't signal genesis message", "url", options.DiscoverySrv+options.Endpoint, "error", err)
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Handle bad protocol version
			if statusCode == http.StatusTeapot {
				slog.Debug("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			// A Freddie that predates rooms answers a rooms-only genesis (see
			// common.SignalSendToGenesisRooms) with a 404, having offered it to nobody. Back off and
			// try again, as for any other refusal, rather than decode the error body as an offer.
			if statusCode != http.StatusOK {
				slog.Debug("Genesis message refused", "status_code", statusCode)
				sleepOrDone(ctx, options.ErrorBackoff)
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}
			// Only Freddie taking the genesis says it counted the report. Any refusal, which may
			// come from a proxy in front of it, leaves the report for the next genesis.
			preflightRep.delivered()

			// TODO: Freddie sends back a 0-length body when nobody replied to our message. Is that the
			// smartest way to handle this case systemwide?
			if len(offerBytes) == 0 {
				slog.Debug("No answer for genesis message!")
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// Looks like we got some kind of response. It ought to be an offer SDP wrapped in a SignalMsg
			offerMsg, offer, err := common.DecodeSignal(offerBytes)
			replyTo := offerMsg.ReplyTo
			if err != nil {
				// Not the raw message: an offer may carry its consumer's pairing, and Freddie's TURN
				// servers.
				slog.Debug("Error decoding signal message", "error", err, "bytes", len(offerBytes))
				return 1, []interface{}{peerConnection, connectionEstablished, connectionChange, connectionClosed}
			}

			// TODO: here we assume we've received a valid offer SDP, we also need to handle invalid case
			return 3, []interface{}{peerConnection, replyTo, offer, connectionEstablished, connectionChange, connectionClosed, offerMsg.ICEServers}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 3
			// input[0]: *webrtc.PeerConnection
			// input[1]: string (replyTo)
			// input[2]: common.OfferMsg (remote offer)
			// input[3]: chan *webrtc.DataChannel
			// input[4]: chan webrtc.PeerConnectionState
			// input[5]: chan struct{}
			// input[6]: []webrtc.ICEServer (TURN relays for a room pairing, or nil)
			peerConnection := input[0].(*webrtc.PeerConnection)
			replyTo := input[1].(string)
			offer := input[2].(common.OfferMsg)
			connectionEstablished := input[3].(chan *webrtc.DataChannel)
			connectionChange := input[4].(chan webrtc.PeerConnectionState)
			connectionClosed := input[5].(chan struct{})
			iceServers, _ := input[6].([]webrtc.ICEServer)
			slog.Debug("Producer state 3...")
			last.begin(replyTo, vpnStatus(options))

			// Create a channel that's blocked until ICE gathering is complete
			gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

			// XXX: in our present signaling handshake, the *consumer's* ICE candidates are sent "a la carte"
			// as a list in the final segment of the handshake. But here on the producer side, our ICE
			// candidates are attached to our answer SDP and signaled all at once. Thus, the only reason
			// we create this slice of candidates is to evaluate whether STUN worked, which happens below.
			// Believe it or not, this post facto examination of ICE candidates is the idiomatic way to
			// determine whether any of your STUN type ICE agents worked...
			var gathered candidateLog
			peerConnection.OnICECandidate(gathered.add)

			// A room pairing's TURN servers must be in place before SetLocalDescription starts
			// gathering. State 3 is entered once per peer connection (every way out of it goes to
			// state 0 or forward), so they are never added twice. A blocked one could hold gathering
			// past Freddie's window, so with them gathering is bounded (see turnGatherWait).
			var gatherLimit time.Duration
			if err := addICEServers(peerConnection, iceServers); err != nil {
				slog.Debug("Error adding the room's ICE servers, going on without them", "error", err)
			} else if len(iceServers) > 0 {
				gatherLimit = turnGatherWait
			}

			// Assign the offer to our connection
			err := peerConnection.SetRemoteDescription(offer.SDP)
			if err != nil {
				slog.Debug("Error setting remote description", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Generate an answer
			answer, err := peerConnection.CreateAnswer(nil)
			if err != nil {
				slog.Debug("Error creating answer SDP", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// This kicks off ICE candidate gathering
			err = peerConnection.SetLocalDescription(answer)
			if err != nil {
				slog.Debug("Error setting local description", "error", err)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			completed := awaitGathering(gatherComplete, gatherLimit)
			if completed {
				slog.Debug("ICE gathering complete!")
			} else {
				slog.Debug("ICE gathering still running, going on with the candidates gathered so far", "waited", gatherLimit)
			}
			localCandidates := gathered.snapshot()
			slog.Debug("Local candidates", "candidates", localCandidates)
			last.gathered(len(iceServers) > 0, localCandidates, completed)

			// If the STUN server(s) we used for this signaling attempt were blocked or unresponsive,
			// we probably wound up with a slice of valid ICE candidates, but of only the 'host' type.
			// We drop that cohort either way. Unless we have a mapped address to offer instead, we
			// don't want to bother signaling those, so here's our escape hatch. A relay candidate
			// counts as non-host, so a room attempt whose TURN server answered keeps a dead STUN
			// cohort until the next pool attempt finds it.
			if !hasNonHostCandidate(localCandidates) {
				if !cohortDropped {
					scache.drop()
					preflight.invalidate()
					cohortDropped = true
					slog.Debug("Dropped the current STUN cohort (reason: ICE failed)")
				}
				if !reachableCandidates(localCandidates, iceNetOf(options)) {
					slog.Debug("ICE failed to gather any non-host candidates, aborting!")
					last.record(common.OutcomeNoLocalSrflx, peerConnection)

					// Borked!
					peerConnection.Close() // TODO: there's an err we should handle here
					return 0, []interface{}{}
				}
				slog.Debug("ICE gathered no non-host candidates, offering the mapped address alone")
			}

			// Our answer SDP with ICE candidates attached: those gathered so far, if the wait gave up
			finalAnswer := peerConnection.LocalDescription()
			// After a bounded wait, a relay can arrive between the snapshot above and this answer;
			// the TURN result describes what the consumer actually gets.
			if finalAnswer != nil && sdpHasRelay(finalAnswer.SDP) {
				last.turn = common.TURNOK
			}

			a, err := json.Marshal(finalAnswer)
			if err != nil {
				slog.Debug("Error marshaling JSON", "error", err)
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

			statusCode, iceBytes, err := signaler.signal(ctx, form)
			if err != nil {
				slog.Debug("Couldn't signal answer SDP", "url", options.DiscoverySrv+options.Endpoint, "error", err)
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			switch statusCode {
			case http.StatusTeapot:
				slog.Debug("Received 'bad protocol version' response")
				<-time.After(options.ErrorBackoff)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusNotFound:
				slog.Debug("Signaling partner hung up, aborting!")
				last.record(common.OutcomePartnerHungUp, peerConnection)

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
				if !cohortDropped {
					scache.drop()
					slog.Debug("Dropped the current STUN cohort (reason: signaling partner hung up)")
				}

				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			case http.StatusOK:
				// Our signaling message was delivered, proceed
			default:
				slog.Debug("Unexpected http status code", "status_code", statusCode)
				<-time.After(options.ErrorBackoff)
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
				slog.Debug("No ICE candidates from signaling partner!")
				last.record(common.OutcomeNoConsumerICE, peerConnection)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			// Looks like we got some kind of response. Should be an ICEMsg in a SignalMsg
			replyTo, iceMsg, err := common.DecodeSignalMsg(iceBytes)
			if err != nil {
				slog.Debug("Error decoding signal message", "error", err, "msg", string(iceBytes))
				last.record(common.OutcomeBadConsumerICE, peerConnection)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			if iceMsg.(common.ICEMsg).ConsumerSessionID == "" {
				slog.Debug("Missing session ID from signaling partner, aborting!")
				last.record(common.OutcomeBadConsumerICE, peerConnection)
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}

			var remoteAddr net.IP

			// TODO: here we assume valid candidates, but we need to handle the invalid case too
			for _, c := range iceMsg.(common.ICEMsg).Candidates {

				// XXX: webrtc.AddICECandidate accepts ICECandidateInit types, which are apparently
				// just serialized ICECandidates?
				err := peerConnection.AddICECandidate(c.ToJSON())
				if err != nil {
					slog.Debug("Error adding ICE candidate", "error", err)
					last.record(common.OutcomeBadConsumerICE, peerConnection)
					// Borked!
					peerConnection.Close() // TODO: there's an err we should handle here
					return 0, []interface{}{}
				}

				// We extract an address from the remote ICE candidates just to send it to the UI for
				// geolocation purposes. Under the assumption that any public address will suffice, we
				// arbitrarily select the last public address found in the list of candidates
				if isPublicIP(c.Address) {
					remoteAddr = net.ParseIP(c.Address)
				}
			}

			// As of 003c9ef0fe25677ee832e1351fb1474057a3e4c9, our signaling partner should not have sent
			// us ICE candidates unless they contained at least one reachable candidate. However, we
			// perform this check on the producer side because some consumers may still on an old version.
			if !remoteCandidatesReachable(iceMsg.(common.ICEMsg).Candidates) {
				slog.Debug("Signaling partner sent only host type ICE candidates, aborting!")
				last.record(common.OutcomeConsumerHostOnly, peerConnection)
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
			slog.Debug("Producer state 4, signaling complete!")

			d, ok := awaitDataChannel(ctx, peerConnection, connectionEstablished, options.NATFailTimeout, options.ConnectGrace)
			if !ok {
				if ctx.Err() != nil {
					peerConnection.Close()
					return 0, []interface{}{}
				}
				slog.Debug("NAT traversal timeout, aborting!")
				last.record(common.OutcomeNATTimeout, peerConnection)
				// Borked!
				peerConnection.Close() // TODO: there's an err we should handle here
				return 0, []interface{}{}
			}
			slog.Debug("A WebRTC connection has been established!")
			last.record(common.OutcomeConnected, peerConnection)
			last.path = selectedPath(peerConnection)
			last.pair = selectedPairTypes(peerConnection)
			return 5, []interface{}{
				peerConnection,
				d,
				connectionChange,
				connectionClosed,
				remoteAddr,
				offer,
				consumerSessionID,
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
			slog.Debug("Producer state 5...")

			// Announce the new connectivity situation for this slot
			if !sendCtx(ctx, com.tx, IPCMsg{
				IpcType: ConsumerInfoIPC,
				Data: common.ConsumerInfo{
					Addr:      remoteAddr,
					Tag:       offer.Tag,
					SessionID: consumerSessionID,
					Country:   offer.Country,
					Pairing:   offerPairing(options, offer.Pairing),
				},
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
								slog.Debug("widget datachannel 1s", "rx_msgs", dRM, "rx_bytes", dRB, "rx_drops", dRD, "tx_msgs", dTM, "tx_bytes", dTB)

							}
						}
					}
				}()
				defer close(pdcStatsDone)
			}

			// One gate per data channel: its drain rate and round trip are this channel's.
			gate := newSendGate(options.Tuning.DataChannelQueueTarget)
			gate.setRTT(sctpRTT(peerConnection))
			rttTicker := time.NewTicker(gateRTTEvery)
			defer rttTicker.Stop()

		proxyloop:
			for {
				select {
				// XXX: there's likely a race or dead code here, see: https://github.com/getlantern/engineering/issues/2320
				// Handle connection failure
				case s := <-connectionChange:
					if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected {
						slog.Debug("Connection failure, resetting!")
						break proxyloop
					} else if s == webrtc.PeerConnectionStateClosed {
						slog.Debug("Connection closed, resetting!")
						break proxyloop
					}
				// Handle connection failure for Firefox
				case _ = <-connectionClosed:
					slog.Debug("Connection closed, resetting!")
					break proxyloop
				case <-rttTicker.C:
					gate.setRTT(sctpRTT(peerConnection))
				// Handle messages from the router
				case msg := <-com.rx:
					switch msg.IpcType {
					case ChunkIPC:
						payload := msg.Data.([]byte)
						if !gate.allow(len(payload), d.BufferedAmount(), time.Now()) {
							continue
						}
						if err := d.Send(payload); err != nil {
							slog.Debug("Error sending to datachannel, resetting", "bytes", len(payload), "error", err)
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
							slog.Debug("Upstream worker reset, disconnecting downstream peer!")
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

// hasNonHostCandidate reports whether any of cands is a server-reflexive or relay candidate,
// which only a STUN or TURN server that answered could have given us.
func hasNonHostCandidate(cands []webrtc.ICECandidate) bool {
	for _, c := range cands {
		if c.Typ != webrtc.ICECandidateTypeHost {
			return true
		}
	}
	return false
}

// reachableCandidates reports whether a peer outside our LAN could reach any
// of cands: a server-reflexive or relay candidate, or a host candidate on our
// mapped address.
func reachableCandidates(cands []webrtc.ICECandidate, pn iceNet) bool {
	for _, c := range cands {
		if c.Typ != webrtc.ICECandidateTypeHost || (pn.mapped() && c.Address == pn.externalIP) {
			return true
		}
	}
	return false
}

// remoteCandidatesReachable reports whether a producer could reach a consumer
// at any of cands: a server-reflexive or relay candidate, or a host candidate
// on a public address, which is how a consumer advertises a port mapped on its
// router.
func remoteCandidatesReachable(cands []webrtc.ICECandidate) bool {
	for _, c := range cands {
		if c.Typ != webrtc.ICECandidateTypeHost || isPublicIP(c.Address) {
			return true
		}
	}
	return false
}

// isPublicIP reports whether addr is a public IP address.
func isPublicIP(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && common.IsPublicAddr(ip)
}

// iceNet is how a native peer's connections reach the network;
// see WebRTCOptions.Net, ICEUDPMux and ICEUDPMuxExternalIP. The zero value is
// pion's default.
type iceNet struct {
	net        transport.Net
	mux        ice.UDPMux
	externalIP string
}

// mapped reports whether the peer advertises a mapped address of its own,
// which the other side can reach whatever its STUN servers say.
func (pn iceNet) mapped() bool {
	return pn.mux != nil && pn.externalIP != ""
}

// newProducerPeerConnection builds a producer's peer connection. pn, on a
// native build, is the transport, UDP mux and mapped address pion uses, so a
// host can rate-limit, route or port-map a widget's WebRTC traffic.
func newProducerPeerConnection(config webrtc.Configuration, dtls covertdtls.Config, tuning WebRTCTuning, pn iceNet) (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	tuned := tuning.apply(&se)
	netSet, err := applyICENet(&se, pn)
	if err != nil {
		return nil, err
	}
	if !dtls.Enabled() && !tuned && !netSet {
		return webrtc.NewPeerConnection(config)
	}
	if err := covertdtls.Apply(dtls, &se); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(config)
}

// attemptOutcome is how a producer worker's previous connection attempt ended (see
// common/outcome.go).
type attemptOutcome struct {
	outcome  string
	iceState string
	pcState  string
	offerID  string
	vpn      string
	// path is common.PathRelay or common.PathDirect for a connected attempt, if known.
	path string
	// began is when the attempt took its offer, and took how long it ran until its outcome.
	began time.Time
	took  time.Duration
	// turn is one of the common.TURN* values, and gatherCut whether the attempt stopped waiting on
	// ICE gathering at its limit. Both are set once gathering ends, or stay "" and false for an
	// attempt that didn't get that far.
	turn      string
	gatherCut bool
	// pair is the selected candidate pair's types (see common.PairTypes) for a connected attempt.
	pair string
}

// begin notes the ReplyTo of the offer an attempt is answering, the VPN state it began under, and
// when. record keeps all three, and what gathered noted.
func (o *attemptOutcome) begin(offerID, vpn string) {
	*o = attemptOutcome{offerID: offerID, vpn: vpn, began: time.Now()}
}

// record notes the attempt's outcome, how long it took, and the peer connection's states at that
// moment. Call it before the peer connection is closed.
func (o *attemptOutcome) record(outcome string, pc *webrtc.PeerConnection) {
	*o = attemptOutcome{outcome: outcome, offerID: o.offerID, vpn: o.vpn, began: o.began,
		turn: o.turn, gatherCut: o.gatherCut}
	if !o.began.IsZero() {
		o.took = time.Since(o.began)
	}
	if pc != nil {
		o.iceState = pc.ICEConnectionState().String()
		o.pcState = pc.ConnectionState().String()
	}
}

// addTo puts the recorded outcome on a genesis form, once: each outcome is reported with the
// genesis message that follows it and then cleared.
func (o *attemptOutcome) addTo(form url.Values, natFailTimeout time.Duration) {
	if o.outcome == "" {
		return
	}
	form.Set(common.PrevOutcomeField, o.outcome)
	form.Set(common.PrevICEStateField, o.iceState)
	form.Set(common.PrevPCStateField, o.pcState)
	form.Set(common.PrevNATTimeoutField, strconv.Itoa(int(natFailTimeout/time.Second)))
	if o.offerID != "" {
		form.Set(common.PrevOfferIDField, o.offerID)
	}
	if o.vpn != "" {
		form.Set(common.PrevVPNField, o.vpn)
	}
	if o.path != "" {
		form.Set(common.PrevPathField, o.path)
	}
	if !o.began.IsZero() {
		form.Set(common.PrevAttemptMsField, strconv.FormatInt(o.took.Milliseconds(), 10))
	}
	if o.turn != "" {
		form.Set(common.PrevTURNField, o.turn)
	}
	if o.gatherCut {
		form.Set(common.PrevGatherCutField, common.GatherCut)
	}
	if o.pair != "" {
		form.Set(common.PrevPairField, o.pair)
	}
	*o = attemptOutcome{}
}

// gathered notes how the attempt's ICE gathering ended: whether Freddie gave it TURN servers and
// it got a relay candidate from them (turnResult), and whether the wait for it was cut short.
// Servers that couldn't even be configured count as had, so their failure shows as missing.
func (o *attemptOutcome) gathered(hadTURN bool, cands []webrtc.ICECandidate, completed bool) {
	o.turn = turnResult(hadTURN, cands)
	o.gatherCut = !completed
}

// sdpHasRelay reports whether an SDP carries a relay candidate.
func sdpHasRelay(sdp string) bool {
	for _, line := range strings.Split(sdp, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "a=candidate:") && strings.Contains(line, " typ relay") {
			return true
		}
	}
	return false
}

// turnResult is the common.TURN* value for an attempt that gathered cands.
func turnResult(hadTURN bool, cands []webrtc.ICECandidate) string {
	if !hadTURN {
		return common.TURNNone
	}
	for _, c := range cands {
		if c.Typ == webrtc.ICECandidateTypeRelay {
			return common.TURNOK
		}
	}
	return common.TURNMissing
}

// sendsToRoomsOnly reports whether a genesis message from a worker with the given tickets goes to
// its rooms alone (see newProducerWebRTC).
func sendsToRoomsOnly(options *WebRTCOptions, roomSlot bool, tickets []string) bool {
	return !options.ServesPool() || (roomSlot && len(tickets) > 0)
}

// servesTicketedRoomsOnly reports whether a worker's genesis messages go to its rooms alone and
// it has room tickets, so Freddie may give it TURN servers with an offer.
func servesTicketedRoomsOnly(options *WebRTCOptions, roomSlot bool) bool {
	tickets := options.CurrentRoomTickets()
	return len(tickets) > 0 && sendsToRoomsOnly(options, roomSlot, tickets)
}

// genesisForm is a genesis POST's form: the genesis message g, plus the
// producer's target countries and room tickets. Each of those is omitted when
// it says nothing, so a producer that uses none of them sends exactly what
// releases before them did. They are form fields rather than part of the
// payload so Freddie needn't parse signaling messages to honor them.
//
// A producer that serves only its rooms posts to
// common.SignalSendToGenesisRooms rather than GenesisAddr, so that a Freddie
// that predates rooms refuses the genesis instead of broadcasting it.
// So does a room slot (see newProducerWebRTC) while it has room tickets.
func genesisForm(options *WebRTCOptions, g []byte, roomSlot bool) url.Values {
	tickets := options.CurrentRoomTickets()
	sendTo := options.GenesisAddr
	if sendsToRoomsOnly(options, roomSlot, tickets) {
		sendTo = common.SignalSendToGenesisRooms
	}
	form := url.Values{
		"data":    {string(g)},
		"send-to": {sendTo},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	for _, c := range options.targetCountries() {
		form.Add(common.TargetCountryField, c)
	}
	for _, tk := range tickets {
		form.Add(common.RoomTicketField, tk)
	}
	if options.VolunteerKind != "" {
		form.Set(common.VolunteerKindField, options.VolunteerKind)
		mapped := "0"
		if options.VolunteerMapped {
			mapped = "1"
		}
		form.Set(common.VolunteerMappedField, mapped)
	}
	return form
}

// offerPairing returns claimed, the pairing an offer claims, if it is the
// pairing of one of the producer's own current volunteer tickets, and ""
// otherwise. The claim is the consumer's echo of what Freddie stamped on the
// room genesis it answered, so an honest consumer's claim always matches. A
// consumer that lies can at worst name another of this producer's pairings,
// which it would have to know. The tickets were verified by whoever issued
// them to this producer, so they are only peeked at here.
func offerPairing(options *WebRTCOptions, claimed string) string {
	if claimed == "" {
		return ""
	}
	for _, tk := range options.CurrentRoomTickets() {
		if role, pairing := helperticket.Peek(tk); role == helperticket.RoleVolunteer && pairing == claimed {
			return claimed
		}
	}
	return ""
}
