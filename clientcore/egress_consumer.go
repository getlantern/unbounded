// egress_consumer.go implements egress consumer behavior over WebSockets, including connection
// establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=654426763

package clientcore

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/getlantern/broflake/common"
)

func NewEgressConsumerWebSocket(options *EgressOptions, wg *sync.WaitGroup) *WorkerFSM {
	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			common.Debugf("Egress consumer state 0, opening WebSocket connection...")

			// We're resetting this slot, so send a nil path assertion IPC message
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}

			// TODO: interesting quirk here: if the table router which manages this WorkerFSM implements
			// non-multiplexed just-in-time strategy wherein it creates a new websocket connection for
			// each new censored peer, we've got a chicken and egg deadlock: the consumer table won't
			// start advertising connectivity until it detects a non-nil path assertion, and we won't
			// have a non-nil path assertion until a censored peer connects to us. 3 poss solutions: make
			// this egress consumer WorkerFSM always emit a (*, 1) path assertion, even when it doesn't
			// have upstream connectivity... OR invent another special case for the host field which
			// indicates "on request", as an escape hatch which indicates to a consumer table that it
			// can use that slot to dial a lantern-controlled exit node, so we'd be emitting something
			// like ($, 1)... OR just disallow just-in-time strategies, and make egress consumers
			// pre-establish N websocket connections

			ctx, cancel := context.WithTimeout(context.Background(), options.ConnectTimeout)
			defer cancel()

			// TODO: WSS
			// Skip certificate verification. This should go away once we have a certificate from a trusted CA instead of our own self-signed one
			httpClient := &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}

			opts := &websocket.DialOptions{HTTPClient: httpClient}

			c, _, err := websocket.Dial(ctx, options.Addr+options.Endpoint, opts)
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
			common.Debugf("Egress consumer state 1, WebSocket connection established!")

			// Send a path assertion IPC message representing the connectivity now provided by this slot
			// TODO: post-MVP we shouldn't be hardcoding (*, 1) here...
			allowAll := []common.Endpoint{{Host: "*", Distance: 1}}
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: allowAll}}

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

			// Main loop:
			// 1. handle chunks from the bus, write them to the WebSocket, detect and handle write errors
			// 2. listen for errors from the read goroutine and handle them

			// On read or write error, we counterintuitively close the websocket with StatusNormalClosure.
			// This is to ensure that the egress server detects closed connections while respecting a
			// quirk in our WS library's net.Conn wrapper: https://pkg.go.dev/nhooyr.io/websocket#NetConn
			for {
				select {
				case msg := <-com.rx:
					// Write the chunk to the websocket, detect and handle error
					// TODO: is it safe to assume the message is a chunk type? Do we trust the router?
					err := c.Write(context.Background(), websocket.MessageBinary, msg.Data.([]byte))
					if err != nil {
						c.Close(websocket.StatusNormalClosure, err.Error())
						common.Debugf("Egress consumer WebSocket write error: %v", err)
						return 0, []interface{}{}
					}
				case err := <-readStatus:
					c.Close(websocket.StatusNormalClosure, err.Error())
					common.Debugf("Egress consumer WebSocket read error: %v", err)
					return 0, []interface{}{}

					// Ordinarily it would be incorrect to put a worker into an infinite loop without including
					// a case to listen for context cancellation, but here we handle context cancellation in a
					// non-explicit way. Since the worker context bounds the call to websocket.Read, worker
					// context cancellation results in a Read error, which we trap to stop the child read
					// goroutine, close the websocket, and return from this state, at which point the worker
					// stop logic in protocol.go takes over and kills this goroutine.
				}
			}

			// TODO: We shouldn't reach this code path, right?
			return 0, []interface{}{}
		}),
	})
}
