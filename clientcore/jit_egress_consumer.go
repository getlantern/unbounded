package clientcore

import (
	"context"
	"sync"
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
					return 0, []interface{}{}
				}
			}

			ctx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
			defer cancel()

			dialOpts := &websocket.DialOptions{
				Subprotocols: common.NewSubprotocolsRequest(consumerInfoMsg.SessionID, common.Version),
			}

			// TODO: WSS
			c, _, err := websocket.Dial(ctx, options.Addr+options.Endpoint, dialOpts)
			if err != nil {
				common.Debugf("Couldn't connect to egress server at %v: %v", options.Addr, err)
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
						// Do nothing, msg sent
					default:
						// Drop the chunk if we can't keep up with the data rate
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
						err := c.Write(ctx, websocket.MessageBinary, msg.Data.([]byte))
						if err != nil {
							c.Close(websocket.StatusNormalClosure, err.Error())
							common.Debugf("JIT egress consumer WebSocket write error: %v", err)
							break proxyloop
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
