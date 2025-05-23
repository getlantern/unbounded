//go:build !wasm

package clientcore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"sync"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/quicwrapper/webt"
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

			block, _ := pem.Decode(options.CACert)
			if block == nil {
				common.Debugf("Failed to parse PEM block containing the certificate")
				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				common.Debugf("Couldn't parse CACert: %v", err)
			}
			d := webt.NewClient(&webt.ClientOptions{
				Addr: options.Addr,
				Path: options.Endpoint,
				TLSConfig: &tls.Config{
					RootCAs: rootCAs,
				},
				PinnedCert: cert,
			})

			url := options.Addr + options.Endpoint

			// Get a net.PacketConn from the WebTransport session (datagram mode)
			pconn, err := d.PacketConn(ctx)
			if err != nil {
				common.Debugf("Couldn't connect to egress server at %v: %v", url, err)
				<-time.After(options.ErrorBackoff)
				return 0, []interface{}{}
			}

			return 1, []interface{}{pconn}
		}),
		FSMstate(func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{}) {
			// State 1
			pconn := input[0].(net.PacketConn)
			common.Debugf("Egress consumer state 1, WebTransport connection established!")

			// Send a path assertion IPC message representing the connectivity now provided by this slot
			// TODO: post-MVP we shouldn't be hardcoding (*, 1) here...
			allowAll := []common.Endpoint{{Host: "*", Distance: 1}}
			com.tx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: allowAll}}

			// WebTransport read loop:
			readStatus := make(chan error)
			go func(ctx context.Context) {
				for {
					buf := make([]byte, 2048)
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
