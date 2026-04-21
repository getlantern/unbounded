package clientcore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/getlantern/broflake/common"
)

func NewJITEgressConsumer(options *EgressOptions, wg *sync.WaitGroup) *WorkerFSM {
	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			common.Debugf("JIT egress consumer state 0, waiting for downstream connection...")

			// The ($, 1) path assertion indicates that all hosts can be reached,
			// one hop away, *upon request*. This is distinct from (*, 1), which means that all hosts can
			// be reached, one hop away, but with the understanding that the network resource is *already
			// connected*. Functionally, they do exactly the same thing -- they signal to workers in the
			// downstream table that resources are available, and that they should begin offering connectivity
			// opportunities. The differing syntax is just to help humans grok the system behavior.
			allUponReq := common.Endpoint{Host: "$", Distance: 1}
			com.tx <- IPCMsg{
				IpcType: PathAssertionIPC,
				Data:    common.PathAssertion{Allow: []common.Endpoint{allUponReq}},
			}

			// Now we wait for a ConsumerInfo IPC message from the corresponding downstream worker indicating
			// that we've got a consumer connected for this connection slot.
			var consumerInfoMsg common.ConsumerInfo

		waitforconsumerloop:
			for {
				select {
				case msg := <-com.rx:
					if msg.IpcType == ConsumerInfoIPC && !msg.Data.(common.ConsumerInfo).Nil() {
						// Before doing anything else, send a "JIT unavailable" path assertion to indicate no
						// connectivity opportunity for this slot, since it's been claimed. TODO nelson 07/25/2025:
						// the JIT unavailable flag was invented to work alongside the ($, 1) path
						// assertion, as a means to signal to downstream workers that there's no connectivity
						// opportunity while also disambiguating from a nil path assertion. It's wacky and
						// should be cleaned up here: https://github.com/getlantern/engineering/issues/2402
						com.tx <- IPCMsg{
							IpcType: PathAssertionIPC,
							Data:    common.PathAssertion{Allow: []common.Endpoint{allUponReq}, JITUnavailable: true},
						}
						consumerInfoMsg = msg.Data.(common.ConsumerInfo)
						break waitforconsumerloop
					}
				// Since we're putting this state into an infinite loop, explicitly handle cancellation
				case <-ctx.Done():
					// XXX nelson 09/14/2025: it is technically correct to send a nil path assertion to
					// indicate a reset here -- however, it seems to create a race which regresses the
					// signaling meltdown issue (https://github.com/getlantern/engineering/issues/2505).
					// The reason for regression is likely because this is the context cancellation case, and
					// so the client quiesces and stops consuming IPC messages from the bus -- and so this
					// nil path assertion winds up sitting in some channel buffer somewhere, and when the
					// widget is toggled on again, it gets consumed at the wrong time and causes an unexpected
					// reset. This should be addressed here: https://github.com/getlantern/engineering/issues/2371
					// com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}
					return 0, []interface{}{}
				}
			}

			ctx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
			defer cancel()

			dialOpts := &websocket.DialOptions{
				Subprotocols: common.NewSubprotocolsRequest(consumerInfoMsg.SessionID, common.Version),
			}
			// Log only a short prefix of the CSID — enough to correlate
			// with the egress's connection record in the same time
			// window without exposing the full session identifier in
			// shipped logs.
			common.Debugf("JIT egress consumer dialing with CSID=%s…", csidPrefix(consumerInfoMsg.SessionID))

			// TODO: WSS
			c, _, err := websocket.Dial(ctx, options.Addr+options.Endpoint, dialOpts)
			if err != nil {
				common.Debugf("Couldn't connect to egress server at %v: %v", options.Addr, err)

				// We're resetting this slot, so send a nil path assertion
				com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}

				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}

			return 1, []interface{}{c}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 1
			// input[0]: *websocket.Conn
			c := input[0].(*websocket.Conn)
			common.Debugf("JIT egress consumer state 1, WebSocket connection established!")

			// Per-direction counters for the widget↔egress WebSocket.
			// If datachannel metrics show bytes arriving at widget's
			// com.rx (consumer → widget) but this socket is writing
			// zero bytes toward egress, the bug is in the router. If
			// the socket is writing but egress isn't sending back (zero
			// bytes read), the egress side isn't completing the QUIC
			// handshake. If both are zero, bytes never even reached
			// the widget.
			//
			// Gated on BROFLAKE_STATS; zero cost in production.
			var wsRxBytes, wsRxMsgs, wsRxDrops atomic.Uint64
			var wsTxBytes, wsTxMsgs atomic.Uint64
			wsStatsDone := make(chan struct{})
			if bfStatsEnabled {
				go func() {
					t := time.NewTicker(time.Second)
					defer t.Stop()
					var lastRB, lastRM, lastRD, lastTB, lastTM uint64
					for {
						select {
						case <-wsStatsDone:
							return
						case <-t.C:
							rb, rm, rd := wsRxBytes.Load(), wsRxMsgs.Load(), wsRxDrops.Load()
							tb, tm := wsTxBytes.Load(), wsTxMsgs.Load()
							dRB, dRM, dRD := rb-lastRB, rm-lastRM, rd-lastRD
							dTB, dTM := tb-lastTB, tm-lastTM
							lastRB, lastRM, lastRD, lastTB, lastTM = rb, rm, rd, tb, tm
							if dRB+dTB+dRD > 0 {
								common.Debugf(
									"widget↔egress ws 1s: rx %d msgs %d bytes (drops %d), "+
										"tx %d msgs %d bytes",
									dRM, dRB, dRD, dTM, dTB,
								)
							}
						}
					}
				}()
				defer close(wsStatsDone)
			}

			// WebSocket read loop:
			readStatus := make(chan error)
			go func(ctx context.Context) {
				for {
					_, b, err := c.Read(ctx)
					if err != nil {
						readStatus <- err
						return
					}

					// Wrap the chunk and send it on to the router
					select {
					case com.tx <- IPCMsg{IpcType: ChunkIPC, Data: b}:
						if bfStatsEnabled {
							wsRxBytes.Add(uint64(len(b)))
							wsRxMsgs.Add(1)
						}
					default:
						// Drop the chunk if we can't keep up with the data rate
						if bfStatsEnabled {
							wsRxDrops.Add(1)
						}
					}
				}
			}(ctx)

			// On read or write error, we counterintuitively close the websocket with StatusNormalClosure.
			// This is to ensure that the egress server detects closed connections while respecting a
			// quirk in our WS library's net.Conn wrapper: https://pkg.go.dev/nhooyr.io/websocket#NetConn
		proxyloop:
			for {
				select {
				case msg := <-com.rx:
					switch msg.IpcType {
					case ChunkIPC:
						payload := msg.Data.([]byte)
						err := c.Write(ctx, websocket.MessageBinary, payload)
						if err != nil {
							c.Close(websocket.StatusNormalClosure, err.Error())
							common.Debugf("JIT egress consumer WebSocket write error (%d bytes): %v", len(payload), err)
							break proxyloop
						}
						if bfStatsEnabled {
							wsTxBytes.Add(uint64(len(payload)))
							wsTxMsgs.Add(1)
						}
					case ConsumerInfoIPC:
						if msg.Data.(common.ConsumerInfo).Nil() {
							c.Close(websocket.StatusNormalClosure, "downstream peer disconnected")
							common.Debug("JIT egress consumer downstream peer disconnected")
							break proxyloop
						}
					default:
						common.Debugf("JIT egress consumer received unexpected IPC message type: %v\n", msg.IpcType)
						// We don't know what to do with this message type, so silently discard it
					}
				case err := <-readStatus:
					c.Close(websocket.StatusNormalClosure, err.Error())
					common.Debugf("JIT egress consumer WebSocket read error: %v", err)
					break proxyloop

					// Ordinarily it would be incorrect to put a worker into an infinite loop without including
					// a case to listen for context cancellation, but here we handle context cancellation in a
					// non-explicit way. Since the worker context bounds the call to websocket.Read, worker
					// context cancellation results in a Read error, which we trap to stop the child read
					// goroutine, close the websocket, and return from this state, at which point the worker
					// stop logic in protocol.go takes over and kills this goroutine.
				}
			}

			// We're resetting this slot, so send a nil path assertion
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}
			return 0, []interface{}{}
		}),
	})
}

// csidPrefix returns the first 8 characters of a CSID (or the whole
// thing if shorter). UUIDs at v4 are 36 chars; 8 is enough prefix to
// correlate a widget dial with a matching egress-side connection record
// in a shared time window, without leaking the full session identifier.
func csidPrefix(csid string) string {
	if len(csid) > 8 {
		return csid[:8]
	}
	return csid
}
