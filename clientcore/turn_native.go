//go:build !wasm

package clientcore

import (
	"errors"
	"net"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"
)

// addICEServers adds servers (TURN relays Freddie sent for a room pairing) to pc's ICE servers.
// ICE gathers with the new servers only if this runs before SetLocalDescription. An empty list
// changes nothing, servers is never modified, and no server, username or credential is logged.
// The browser build has its own, with the same contract (see turn_wasm.go).
func addICEServers(pc *webrtc.PeerConnection, servers []webrtc.ICEServer) error {
	if len(servers) == 0 {
		return nil
	}
	config := pc.GetConfiguration()
	config.ICEServers = append(append([]webrtc.ICEServer(nil), config.ICEServers...), servers...)
	return pc.SetConfiguration(config)
}

// turnDialTimeout bounds pion's TCP dial to a TURN server (turn:?transport=tcp and turns:), which
// it makes with no timeout or context. Closing a peer connection waits for gathering to finish,
// so without it a censor that drops the server's SYNs holds Close, and the worker's next attempt,
// for the OS connect timeout (over a minute). 5s covers the first SYN and two retransmits (at 1s
// and 3s on Linux and macOS), so a reachable server connects on a slow, lossy link. It is under
// turnGatherWait, so every dial has returned by the time the bounded wait gives up, and Close
// doesn't wait on one, and it leaves 3s of that wait for the TLS handshake and the allocation. A
// var so tests can shorten it.
var turnDialTimeout = 5 * time.Second

var errTURNDialTimeout = errors.New("TCP dial to TURN server timed out")

// boundedDialNet is a transport.Net whose DialTCP gives up after turnDialTimeout. Everything else
// goes to the Net it wraps unchanged.
type boundedDialNet struct {
	transport.Net
}

func (n boundedDialNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	type result struct {
		conn transport.TCPConn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := n.Net.DialTCP(network, laddr, raddr)
		done <- result{conn, err}
	}()
	t := time.NewTimer(turnDialTimeout)
	defer t.Stop()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-t.C:
		// The dial goes on until the OS gives up; a connection it makes after all is unwanted.
		go func() {
			if r := <-done; r.err == nil && r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, errTURNDialTimeout
	}
}
