//go:build wasm

package clientcore

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/quicwrapper/webt"
)

var wtCounter int32

type JSWebTransportConn struct {
	send    js.Value
	chunker *webt.DatagramChunker
}

func newJSWebTransportConn(id int) *JSWebTransportConn {
	conn := &JSWebTransportConn{
		send:    js.Global().Get("sendWebTransportDatagramJS" + strconv.Itoa(id)),
		chunker: webt.NewDatagramChunker(),
	}

	// setup the receive callback for JS
	js.Global().Set("receiveWebTransportDatagramGo"+strconv.Itoa(id), js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		buf := make([]byte, args[0].Get("length").Int())
		//common.Debugf("[Go WT:%v]: receive datagram of %v", id, len(buf))
		js.CopyBytesToGo(buf, args[0])
		conn.chunker.Receive(buf)
		return nil
	}))

	return conn
}

func (c *JSWebTransportConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	data, err := c.chunker.Read()
	if err != nil {
		return 0, nil, err
	}
	n = copy(p, data)
	addr = nil
	return
}

func (c *JSWebTransportConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	chunks := c.chunker.Chunk(p)
	for _, chunk := range chunks {
		arr := js.Global().Get("Uint8Array").New(len(chunk))
		js.CopyBytesToJS(arr, chunk)
		promise := c.send.Invoke(arr)

		done := make(chan struct{})
		promise.Call("then", js.FuncOf(func(this js.Value, args []js.Value) any {
			close(done)
			return nil
		}))
		<-done
	}
	return len(p), nil
}

func (c *JSWebTransportConn) Close() error {
	c.chunker.Close()
	return nil
}

func (d *JSWebTransportConn) LocalAddr() net.Addr                { return nil }
func (d *JSWebTransportConn) SetDeadline(t time.Time) error      { return nil }
func (d *JSWebTransportConn) SetReadDeadline(t time.Time) error  { return nil }
func (d *JSWebTransportConn) SetWriteDeadline(t time.Time) error { return nil }

func NewEgressConsumerWebTransport(options *EgressOptions, wg *sync.WaitGroup) *WorkerFSM {
	return NewWorkerFSM(wg, []FSMstate{
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 0
			// (no input data)
			wtId := atomic.LoadInt32(&wtCounter)
			atomic.AddInt32(&wtCounter, 1)
			common.Debugf("Egress consumer state 0, opening WebTransport connection %v in JS...", wtId)

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

			url := options.Addr + options.Endpoint

			// create a briding connection
			pconn := newJSWebTransportConn(int(wtId))

			// call into the JS to initialize the WebTransport
			initWT := js.Global().Get("initWebTransportJS" + strconv.Itoa(int(wtId)))
			if initWT.Type() != js.TypeFunction {
				common.Debugf("initWebTransportJS%d is not a function!", wtId)
				return 0, []interface{}{}
			}

			promise := initWT.Invoke(js.ValueOf(url))
			resultCh := make(chan bool)
			then := js.FuncOf(func(this js.Value, args []js.Value) any {
				common.Debugf("WebTransport %d initialized successfully", wtId)
				resultCh <- true
				return nil
			})
			catch := js.FuncOf(func(this js.Value, args []js.Value) any {
				common.Debugf("WebTransport %d init error:%v", wtId, args[0].String())
				resultCh <- false
				return nil
			})
			promise.Call("then", then).Call("catch", catch)

			// wait for completion in JS
			if <-resultCh {
				<-time.After(1 * time.Second) // TODO: find out why this wait is needed or the FSM enters an infinite loop that calls this state
				return 1, []interface{}{pconn}
			} else {
				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 1
			pconn := input[0].(net.PacketConn)
			common.Debugf("Egress consumer state 1, WebTransport connection established from JS!")

			// Send a path assertion IPC message representing the connectivity now provided by this slot
			// TODO: post-MVP we shouldn't be hardcoding (*, 1) here...
			allowAll := []common.Endpoint{{Host: "*", Distance: 1}}
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: allowAll}}

			// WebTransport read loop:
			readStatus := make(chan error)
			go func(ctx context.Context) {
				for {
					buf := make([]byte, 1280)
					bytesRead, _, err := pconn.ReadFrom(buf)
					if err != nil {
						readStatus <- err
						return
					}
					//common.Debugf("Egress consumer WebTransport received %v bytes", bytesRead)

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
					data, ok := msg.Data.([]byte)
					if !ok {
						common.Debugf("Egress consumer WebTransport received non-byte chunk: %v", msg.Data)
						return 0, []interface{}{}
					}
					_, err := pconn.WriteTo(data, nil)
					if err != nil {
						common.Debugf("Egress consumer WebTransport write error: %v", err)
						return 0, []interface{}{}
					}
					//common.Debugf("Egress consumer WebTransport sent %v/%v bytes", bytesWritten, len(msg.Data.([]byte)))

					// At this point the chunks are written, so loop around and wait for the next chunk
				case err := <-readStatus:
					common.Debugf("Egress consumer WebTransport read error: %v", err)
					pconn.Close()
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
