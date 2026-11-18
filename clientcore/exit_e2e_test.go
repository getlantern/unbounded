//go:build !wasm

package clientcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// exitFreddie relays signaling as Freddie does.
//   - A genesis POST is streamed to the next subscriber and waits for an offer.
//   - An ICE message is delivered and answered at once.
//   - Every other message is delivered to the request it replies to, then waits for that
//     request's reply.
//
// With turn set, it delivers each genesis as a room's and stamps turn on it and on the offer it
// delivers, as Freddie does for a pairing. A rooms-only genesis is delivered as a room's too.
type exitFreddie struct {
	mu      sync.Mutex
	next    int
	waiting map[string]chan string
	geneses chan string
	turn    []webrtc.ICEServer
	// posted gets each genesis POST's form, while it has room.
	posted chan url.Values
}

func newExitFreddie(t *testing.T, turnServers []webrtc.ICEServer) string {
	t.Helper()
	srv, _ := newRecordingExitFreddie(t, turnServers)
	return srv
}

// newRecordingExitFreddie is newExitFreddie, also returning a channel of the genesis forms it
// takes.
func newRecordingExitFreddie(t *testing.T, turnServers []webrtc.ICEServer) (string, <-chan url.Values) {
	t.Helper()
	f := &exitFreddie{waiting: map[string]chan string{}, geneses: make(chan string, 64), turn: turnServers,
		posted: make(chan url.Values, 64)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL, f.posted
}

func (f *exitFreddie) await() (string, chan string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := strconv.Itoa(f.next)
	ch := make(chan string, 1)
	f.waiting[id] = ch
	return id, ch
}

func (f *exitFreddie) deliver(to, msg string) bool {
	f.mu.Lock()
	ch, ok := f.waiting[to]
	delete(f.waiting, to)
	f.mu.Unlock()
	if ok {
		ch <- msg
	}
	return ok
}

func (f *exitFreddie) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		select {
		case g := <-f.geneses:
			_, _ = fmt.Fprintln(w, g)
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	typ, _ := strconv.Atoi(r.Form.Get("type"))
	id, reply := f.await()
	// A message no longer waiting for its reply can't be replied to, as at Freddie.
	defer func() {
		f.mu.Lock()
		delete(f.waiting, id)
		f.mu.Unlock()
	}()
	msg := common.SignalMsg{ReplyTo: id, Type: common.SignalMsgType(typ), Payload: r.Form.Get("data")}
	if sendTo := r.Form.Get("send-to"); sendTo == common.SignalSendToGenesis || sendTo == common.SignalSendToGenesisRooms {
		select {
		case f.posted <- r.PostForm:
		default:
		}
		msg.Room = len(f.turn) > 0 || sendTo == common.SignalSendToGenesisRooms
		msg.ICEServers = f.turn
		b, _ := json.Marshal(msg)
		f.geneses <- string(b)
	} else {
		if msg.Type == common.SignalMsgOffer {
			msg.ICEServers = f.turn
		}
		b, _ := json.Marshal(msg)
		if !f.deliver(r.Form.Get("send-to"), string(b)) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if msg.Type == common.SignalMsgICE {
			return
		}
	}
	// Freddie's TTLs: offers and answers wait out the other side's ICE gathering.
	ttl := 5 * time.Second
	if msg.Type == common.SignalMsgOffer || msg.Type == common.SignalMsgAnswer {
		ttl = 15 * time.Second
	}
	select {
	case m := <-reply:
		_, _ = io.WriteString(w, m)
	case <-time.After(ttl):
	case <-r.Context().Done():
	}
}

// e2eOptions are a peer's options on n, against Freddie at srv, with the vnet's STUN server.
func e2eOptions(srv string, n transport.Net, stunURL string) *WebRTCOptions {
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv
	o.Net = n
	o.STUNBatch = func(uint32) ([]string, error) { return []string{stunURL}, nil }
	o.STUNBatchSize = 1
	o.Patience = 50 * time.Millisecond
	o.ErrorBackoff = 50 * time.Millisecond
	o.NATFailTimeout = 10 * time.Second
	return o
}

// echoExit is an exit that sends every datagram back, and records whether each session is a
// direct exit. served, if set, gets whether each direct exit session it serves is personal.
func echoExit(ready bool, sessions chan<- bool, served chan<- bool) *ExitOptions {
	return &ExitOptions{
		Ready:  func(bool) bool { return ready },
		Params: func(bool) (string, string, bool) { return "pin", "password", ready },
		Serve: func(ctx context.Context, conn net.PacketConn, password string, personal bool) {
			if served != nil {
				select {
				case served <- personal:
				default:
				}
			}
			buf := make([]byte, 1500)
			for {
				n, addr, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				if _, err := conn.WriteTo(buf[:n], addr); err != nil {
					return
				}
			}
		},
		OnSession: func(directExit bool) { sessions <- directExit },
	}
}

// startProducer runs a producer whose upstream always has connectivity to share.
func startProducer(t *testing.T, o *WebRTCOptions) {
	t.Helper()
	p := NewProducerWebRTC(o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case msg := <-p.com.tx:
				if msg.IpcType == ConnectivityCheckIPC {
					p.com.rx <- IPCMsg{IpcType: PathAssertionIPC, Data: common.PathAssertion{Allow: []common.Endpoint{{Host: "*", Distance: 1}}}}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	p.Start()
	t.Cleanup(func() { p.Stop(); cancel() })
}

func startConsumer(t *testing.T, o *WebRTCOptions) {
	t.Helper()
	c := NewConsumerWebRTC(o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-c.com.tx:
			case <-ctx.Done():
				return
			}
		}
	}()
	c.Start()
	t.Cleanup(func() { c.Stop(); cancel() })
}

func TestDirectExitEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior vnet.EndpointDependencyType
		turn     bool
		// personal makes the producer serve its rooms alone, with a ticket, so its sessions are
		// personal.
		personal bool
	}{
		{"punched", vnet.EndpointIndependent, false, false},
		{"relayed", vnet.EndpointAddrPortDependent, true, false},
		{"personal", vnet.EndpointIndependent, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn, pn, stunURL, turnServer := newVNetICE(t, tc.behavior)
			var turnServers []webrtc.ICEServer
			if tc.turn {
				turnServers = []webrtc.ICEServer{turnServer}
			}
			srv := newExitFreddie(t, turnServers)

			sessions := make(chan bool, 4)
			served := make(chan bool, 4)
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(true, sessions, served)
			if tc.personal {
				po.SetRoomTickets([]string{"a.b"})
				po.SetServePool(false)
			}
			startProducer(t, po)

			paths := make(chan ExitPath, 1)
			co := e2eOptions(srv, cn, stunURL)
			co.OnExitPath = func(p ExitPath) {
				// Never block the consumer's worker: a path past the first is closed, ending it.
				select {
				case paths <- p:
				default:
					_ = p.Conn.Close()
				}
			}
			startConsumer(t, co)

			var path ExitPath
			select {
			case path = <-paths:
			case <-time.After(45 * time.Second):
				t.Fatal("no direct exit session")
			}
			select {
			case directExit := <-sessions:
				assert.True(t, directExit, "the producer counts a direct exit")
			case <-time.After(45 * time.Second):
				t.Fatal("the producer counted no session")
			}
			assert.Equal(t, "pin", path.CertPin)
			assert.Equal(t, "password", path.Password)
			assert.Equal(t, tc.turn, path.Relayed)
			select {
			case personal := <-served:
				assert.Equal(t, tc.personal, personal, "the session is served as its genesis's kind")
			case <-time.After(5 * time.Second):
				t.Fatal("the exit served no session")
			}

			msg := bytes.Repeat([]byte{0xcd}, 1200)
			_, err := path.Conn.WriteTo(msg, nil)
			require.NoError(t, err)
			require.NoError(t, path.Conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			buf := make([]byte, 1500)
			n, _, err := path.Conn.ReadFrom(buf)
			require.NoError(t, err)
			assert.Equal(t, msg, buf[:n], "the exit echoed the datagram intact")
		})
	}
}

// A consumer whose embedder can't take a direct exit, or a producer that can't offer one, gets
// today's peer connection.
func TestDirectExitFallsBackToWebRTC(t *testing.T) {
	for _, tc := range []struct {
		name          string
		producerReady bool
		consumerTakes bool
	}{
		{"old consumer", true, false},
		{"flag off", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)
			srv := newExitFreddie(t, nil)

			sessions := make(chan bool, 4)
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(tc.producerReady, sessions, nil)
			startProducer(t, po)

			co := e2eOptions(srv, cn, stunURL)
			if tc.consumerTakes {
				co.OnExitPath = func(ExitPath) { t.Error("a direct exit was taken") }
			}
			startConsumer(t, co)

			select {
			case directExit := <-sessions:
				assert.False(t, directExit)
			case <-time.After(45 * time.Second):
				t.Fatal("no session")
			}
		})
	}
}

// A direct exit session reports what a peer connection's does with the next genesis: its TURN
// result, how gathering ended, the path and the selected pair's types.
func TestDirectExitReportsHandshake(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior vnet.EndpointDependencyType
		turn     bool
	}{
		{"punched", vnet.EndpointIndependent, false},
		{"relayed", vnet.EndpointAddrPortDependent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn, pn, stunURL, turnServer := newVNetICE(t, tc.behavior)
			var turnServers []webrtc.ICEServer
			if tc.turn {
				turnServers = []webrtc.ICEServer{turnServer}
			}
			srv, posted := newRecordingExitFreddie(t, turnServers)

			// An exit that stops serving at once ends each session, so the producer's next
			// genesis reports it.
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(true, make(chan bool, 4), nil)
			po.Exit.Serve = func(context.Context, net.PacketConn, string, bool) {}
			startProducer(t, po)
			co := e2eOptions(srv, cn, stunURL)
			co.OnExitPath = func(p ExitPath) { _ = p.Conn.Close() }
			startConsumer(t, co)

			deadline := time.After(45 * time.Second)
			for {
				select {
				case form := <-posted:
					if form.Get(common.PrevOutcomeField) != common.OutcomeConnected {
						continue
					}
					if tc.turn {
						assert.Equal(t, common.TURNOK, form.Get(common.PrevTURNField))
						assert.Equal(t, common.PathRelay, form.Get(common.PrevPathField))
						assert.Contains(t, form.Get(common.PrevPairField), "relay")
					} else {
						assert.Equal(t, common.TURNNone, form.Get(common.PrevTURNField))
						assert.Equal(t, common.PathDirect, form.Get(common.PrevPathField))
						assert.NotContains(t, form.Get(common.PrevPairField), "relay")
					}
					assert.Equal(t, form.Get(common.PrevPairField), common.NormalizePair(form.Get(common.PrevPairField)),
						"a pair Freddie takes")
					return
				case <-deadline:
					t.Fatal("no genesis reported a connected direct exit")
				}
			}
		})
	}
}
