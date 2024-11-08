// egress_consumer.go implements egress consumer behavior over WebSockets, including connection
// establishment, connection error detection, and reset. See:
// https://docs.google.com/spreadsheets/d/1qM1gwPRtTKTFfZZ0e51R7AdS6qkPlKMuJX3D3vmpG_U/edit#gid=654426763

package clientcore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/quicwrapper/webt"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func NewEgressConsumerWebTransport(options *EgressOptions, wg *sync.WaitGroup) *WorkerFSM {
	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			common.Debugf("Egress consumer state 0, opening WebTransport connection...")

			// We're resetting this slot, so send a nil path assertion IPC message
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{}}

			// TODO: interesting quirk here: if the table router which manages this WorkerFSM implements
			// non-multiplexed just-in-time strategy wherein it creates a new WebTransport connection for
			// each new censored peer, we've got a chicken and egg deadlock: the consumer table won't
			// start advertising connectivity until it detects a non-nil path assertion, and we won't
			// have a non-nil path assertion until a censored peer connects to us. 3 poss solutions: make
			// this egress consumer WorkerFSM always emit a (*, 1) path assertion, even when it doesn't
			// have upstream connectivity... OR invent another special case for the host field which
			// indicates "on request", as an escape hatch which indicates to a consumer table that it
			// can use that slot to dial a lantern-controlled exit node, so we'd be emitting something
			// like ($, 1)... OR just disallow just-in-time strategies, and make egress consumers
			// pre-establish N WebTransport connections

			ctx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
			defer cancel()

			rootCAs, _ := x509.SystemCertPool()
			if rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}
			if ok := rootCAs.AppendCertsFromPEM(options.CACert); !ok {
				common.Debugf("Couldn't add root certificate: %v", options.CACert)
			}

			var d webtransport.Dialer = webtransport.Dialer{}
			d.RoundTripper = &http3.RoundTripper{
				TLSClientConfig: &tls.Config{
					RootCAs: rootCAs,
				},
			}

			url := options.Addr + options.Endpoint

			// TODO: We ideally should create a single session and reuse it for all streams.
			httpResponse, session, err := d.Dial(ctx, url, nil)
			if err != nil {
				common.Debugf("Couldn't connect to egress server at %v: %v", url, err)
				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}
			stream, err := session.OpenStream()
			if err != nil {
				common.Debugf("Couldn't open stream to egress server at %v: %v", url, err)
				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}

			// We convert this to a net.Conn here because it's well understood interface but also
			// allows us to encapsulate the relevant methods of both the session and the stream.
			c := webt.NewConn(stream, session, httpResponse, func() {
				common.Debugf("Egress consumer WebTransport connection closed")
			})

			return 1, []interface{}{&c}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 1
			c := *input[0].(*net.Conn)
			common.Debugf("Egress consumer state 1, WebTransport connection established!")

			// Send a path assertion IPC message representing the connectivity now provided by this slot
			// TODO: post-MVP we shouldn't be hardcoding (*, 1) here...
			allowAll := []common.Endpoint{{Host: "*", Distance: 1}}
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: allowAll}}

			// WebTransport read loop:
			readStatus := make(chan error)
			go func(ctx context.Context) {
				for {
					buf := make([]byte, 1024)
					bytesRead, err := c.Read(buf)
					if err != nil {
						readStatus <- err
						return
					}

					// Wrap the chunk and send it on to the router
					select {
					case com.tx <- IPCMsg{IpcType: ChunkIPC, Data: buf[:bytesRead]}:
						// Do nothing, msg sent
					default:
						// Drop the chunk if we can't keep up with the data rate
					}
				}
			}(ctx)

			// Main loop:
			// 1. handle chunks from the bus, write them to the WebTransport, detect and handle write errors
			// 2. listen for errors from the read goroutine and handle them
			// On read or write error, we close the WebTransport to ensure that the egress server detects
			// closed connections.
			for {
				select {
				case msg := <-com.rx:
					// Write the chunk to the WebTransport, detect and handle error
					// TODO: what if the bytes written is less than the chunk size? Do we need to loop?
					if data, ok := msg.Data.([]byte); !ok {
						common.Debugf("Egress consumer WebTransport received non-byte chunk: %v", msg.Data)
						c.Close()
						return 0, []interface{}{}
					} else if bytesWritten, err := c.Write(data); err != nil {
						common.Debugf("Egress consumer WebTransport write error: %v", err)
						c.Close()
						return 0, []interface{}{}
					} else if bytesWritten != len(data) {
						// See https://pkg.go.dev/io#Writer for the contract of io.Writer.
						// Theoretically we should never hit this code because any writer should return an error if
						// it doesn't write the full chunk, but we check anyway just to be safe.
						common.Debugf("Egress consumer WebTransport write error: wrote %v bytes, expected %v",
							bytesWritten, len(data))
						c.Close()
						return 0, []interface{}{}
					}
					// At this point the chunk is written, so loop around and wait for the next chunk
				case err := <-readStatus:
					common.Debugf("Egress consumer WebTransport read error: %v", err)
					c.Close()
					return 0, []interface{}{}

					// Ordinarily it would be incorrect to put a worker into an infinite loop without including
					// a case to listen for context cancellation, but here we handle context cancellation in a
					// non-explicit way. Since the worker context bounds the call to net.Conn.Read, worker
					// context cancellation results in a Read error, which we trap to stop the child read
					// goroutine, close the connection, and return from this state, at which point the worker
					// stop logic in protocol.go takes over and kills this goroutine.
				}
			}
		}),
	})
}

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

			c, _, err := websocket.Dial(ctx, options.Addr+options.Endpoint, nil)
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
		}),
	})
}
