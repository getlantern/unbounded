//go:build !wasm

package clientcore

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// errBadCandidate refuses a peer's candidate that can't be parsed. It never quotes the candidate,
// which is the peer's address.
var errBadCandidate = errors.New("bad ICE candidate")

// errTooManyCandidates refuses a peer's candidates when there are more than maxRemoteCandidates.
var errTooManyCandidates = errors.New("too many ICE candidates")

// errBadICEServer refuses a STUN or TURN URL that can't be parsed. It never quotes the URL, which
// may name a TURN host.
var errBadICEServer = errors.New("bad ICE server URL")

// maxRemoteCandidates caps a peer's candidates: the agent checks each one from this side's address,
// so a crafted list would otherwise aim checks wherever the peer likes, and cost CPU and memory.
const maxRemoteCandidates = 32

// iceSession is a direct exit session's transport: a bare ICE agent, with no DTLS or SCTP, whose
// conn carries the exit's QUIC datagrams. The consumer controls it and dials, and the producer
// accepts. It punches a hole or relays through TURN as a peer connection's agent would, with the
// same transport, UDP mux, mapped address and tuning.
type iceSession struct {
	agent       *ice.Agent
	controlling bool
	conn        *ice.Conn

	mu        sync.Mutex
	gathered  []ice.Candidate
	complete  chan struct{}
	gatherEnd sync.Once
	relay     chan struct{}
	relayOnce sync.Once
	ended     chan struct{}
	endOnce   sync.Once
}

// newICESession creates a session's agent on options' transport, with servers' STUN and TURN
// URLs. window is how long the session waits to connect, which sizes its checks as checkingFor
// does for a peer connection.
func newICESession(options *WebRTCOptions, controlling bool, servers []webrtc.ICEServer, window time.Duration) (*iceSession, error) {
	urls, err := iceURLs(servers)
	if err != nil {
		return nil, err
	}
	pn := iceNetOf(options)
	n := pn.net
	if n == nil {
		if n, err = stdnet.NewNet(); err != nil {
			return nil, err
		}
	}
	opts := []ice.AgentOption{
		ice.WithUrls(urls),
		ice.WithNet(boundedDialNet{n}),
		// A peer connection's networks. Without them a bare agent gathers no host or server
		// reflexive candidates, only relayed ones.
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}),
		// pion's default logger takes its levels from PION_LOG_* and, at debug, prints the
		// remote ufrag and pwd and candidate addresses. This agent's logs go nowhere.
		ice.WithLoggerFactory(&logging.DefaultLoggerFactory{
			Writer:          io.Discard,
			DefaultLogLevel: logging.LogLevelDisabled,
		}),
	}
	if pn.mux != nil {
		opts = append(opts, ice.WithUDPMux(pn.mux))
		if pn.externalIP != "" {
			// As applyICENet does for a peer connection: a host candidate on the mapped address,
			// appended so LAN peers keep the local one.
			opts = append(opts, ice.WithAddressRewriteRules(ice.AddressRewriteRule{
				External:        []string{pn.externalIP},
				AsCandidateType: ice.CandidateTypeHost,
				Mode:            ice.AddressRewriteAppend,
				Networks:        []ice.NetworkType{ice.NetworkTypeUDP4},
			}))
		}
	}
	opts = append(opts, options.Tuning.checkingFor(window).iceOptions()...)
	agent, err := ice.NewAgentWithOptions(opts...)
	if err != nil {
		return nil, err
	}
	s := &iceSession{
		agent:       agent,
		controlling: controlling,
		complete:    make(chan struct{}),
		relay:       make(chan struct{}),
		ended:       make(chan struct{}),
	}
	if err := agent.OnCandidate(s.onCandidate); err != nil {
		_ = agent.Close()
		return nil, err
	}
	if err := agent.OnConnectionStateChange(s.onState); err != nil {
		_ = agent.Close()
		return nil, err
	}
	return s, nil
}

// iceURLs is servers' URLs as the agent takes them, with each TURN server's credentials.
func iceURLs(servers []webrtc.ICEServer) ([]*stun.URI, error) {
	var urls []*stun.URI
	for _, s := range servers {
		for _, raw := range s.URLs {
			u, err := stun.ParseURI(raw)
			if err != nil {
				return nil, errBadICEServer
			}
			if u.Scheme == stun.SchemeTypeTURN || u.Scheme == stun.SchemeTypeTURNS {
				u.Username = s.Username
				if cred, ok := s.Credential.(string); ok {
					u.Password = cred
				}
			}
			urls = append(urls, u)
		}
	}
	return urls, nil
}

func (s *iceSession) onCandidate(c ice.Candidate) {
	if c == nil {
		s.gatherEnd.Do(func() { close(s.complete) })
		return
	}
	s.mu.Lock()
	s.gathered = append(s.gathered, c)
	s.mu.Unlock()
	if c.Type() == ice.CandidateTypeRelay {
		s.relayOnce.Do(func() { close(s.relay) })
	}
}

func (s *iceSession) onState(state ice.ConnectionState) {
	slog.Debug("ICE session state change", "state", state.String())
	switch state {
	case ice.ConnectionStateFailed, ice.ConnectionStateDisconnected, ice.ConnectionStateClosed:
		s.endOnce.Do(func() {
			close(s.ended)
			// ICE may recover from Disconnected, and until the agent closes its conn's reads
			// block, so an ended session closes it to fail them. Close waits for the agent's
			// current task, so it isn't run in this callback.
			go func() { _ = s.agent.Close() }()
		})
	}
}

// credentials is the session's own ufrag and pwd, for the other side.
func (s *iceSession) credentials() (ufrag, pwd string, err error) {
	return s.agent.GetLocalUserCredentials()
}

// gather gathers the session's candidates and returns them as ice.Candidate.Marshal writes them.
// It waits as awaitGathering does: with limit set, it goes on shortly after the first relay
// candidate, or at the limit. It gives up when ctx ends.
func (s *iceSession) gather(ctx context.Context, limit time.Duration) ([]string, gatherEnd, error) {
	var firstRelay <-chan struct{}
	if limit > 0 {
		firstRelay = s.relay
	}
	if err := s.agent.GatherCandidates(); err != nil {
		return nil, 0, err
	}
	// Gathering complete, or ctx ended: either ends awaitGathering's wait.
	waited := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-s.complete:
		case <-ctx.Done():
		case <-stop:
			return
		}
		close(waited)
	}()
	end := awaitGathering(waited, firstRelay, turnRelayGrace, limit)
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return outboundCandidates(s.gathered), end, nil
}

// outboundCandidates is gathered as ice.Candidate.Marshal writes them, cut to the
// maxRemoteCandidates the other side accepts. A host with many interfaces can gather more, so it
// keeps the candidates most likely to connect across NATs first: relayed, then server and peer
// reflexive, then host candidates on public addresses (a port-mapped one among them), then the
// rest, each in gathering order.
func outboundCandidates(gathered []ice.Candidate) []string {
	rank := func(c ice.Candidate) int {
		switch c.Type() {
		case ice.CandidateTypeRelay:
			return 0
		case ice.CandidateTypeServerReflexive, ice.CandidateTypePeerReflexive:
			return 1
		}
		if isPublicIP(c.Address()) {
			return 2
		}
		return 3
	}
	ordered := slices.Clone(gathered)
	slices.SortStableFunc(ordered, func(a, b ice.Candidate) int { return rank(a) - rank(b) })
	if len(ordered) > maxRemoteCandidates {
		ordered = ordered[:maxRemoteCandidates]
	}
	out := make([]string, len(ordered))
	for i, c := range ordered {
		out[i] = c.Marshal()
	}
	return out
}

// addRemote adds the other side's candidates, refusing more than maxRemoteCandidates.
func (s *iceSession) addRemote(raw []string) error {
	if len(raw) > maxRemoteCandidates {
		return errTooManyCandidates
	}
	for _, r := range raw {
		c, err := ice.UnmarshalCandidate(r)
		if err != nil {
			return errBadCandidate
		}
		if err := s.agent.AddRemoteCandidate(c); err != nil {
			return err
		}
	}
	return nil
}

// connect dials the other side, or accepts its dial, with its credentials, until ctx ends.
func (s *iceSession) connect(ctx context.Context, ufrag, pwd string) error {
	var conn *ice.Conn
	var err error
	if s.controlling {
		conn, err = s.agent.Dial(ctx, ufrag, pwd)
	} else {
		conn, err = s.agent.Accept(ctx, ufrag, pwd)
	}
	if err != nil {
		return err
	}
	s.conn = conn
	return nil
}

// packetConn is the connected session's conn as the packet conn a QUIC endpoint takes.
func (s *iceSession) packetConn() net.PacketConn {
	return newICEPacketConn(s.conn)
}

// relayed reports whether the selected pair goes through a TURN relay on either side.
func (s *iceSession) relayed() bool {
	pair, err := s.agent.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return false
	}
	return pair.Local.Type() == ice.CandidateTypeRelay || pair.Remote.Type() == ice.CandidateTypeRelay
}

// pairTypes is the selected pair as common.PairTypes, its local end first, or "" when no pair is
// selected.
func (s *iceSession) pairTypes() string {
	pair, err := s.agent.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return ""
	}
	return common.PairTypes(pair.Local.Type().String(), pair.Remote.Type().String())
}

// done is closed when the session fails, disconnects or is closed. The session is then over:
// its agent is closed, so its conn's reads and writes fail.
func (s *iceSession) done() <-chan struct{} {
	return s.ended
}

// Close ends the session. It is an io.Closer so closeWorkerResource can close a session a
// stopped worker held.
func (s *iceSession) Close() error {
	return s.agent.Close()
}

// Every session's packet conn gives these addresses: a one-peer conn needs only a stable key for a
// QUIC endpoint to demultiplex on, and these say nothing about either side, so an exit that logs
// a conn's addresses logs no user's IP.
var (
	icePeerAddr  net.Addr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1}
	iceLocalAddr net.Addr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 1}
)

// icePacketConn is an ICE conn as a packet conn with one peer. Its addresses are placeholders
// (see icePeerAddr), the same for every session, and it has no RemoteAddr.
//
// Its write deadline is its own. ice.Conn sets one on the selected pair's local socket, which
// also carries the agent's consent checks and, on a UDP mux, other sessions. A UDP write doesn't
// block, so a write past the deadline fails at once instead.
type icePacketConn struct {
	conn          *ice.Conn
	writeDeadline atomic.Pointer[time.Time]
}

func newICEPacketConn(c *ice.Conn) *icePacketConn {
	return &icePacketConn{conn: c}
}

func (c *icePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.conn.Read(p)
	return n, icePeerAddr, err
}

func (c *icePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if d := c.writeDeadline.Load(); d != nil && !d.IsZero() && !time.Now().Before(*d) {
		return 0, os.ErrDeadlineExceeded
	}
	return c.conn.Write(p)
}

func (c *icePacketConn) Close() error {
	return c.conn.Close()
}

func (c *icePacketConn) LocalAddr() net.Addr {
	return iceLocalAddr
}

func (c *icePacketConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *icePacketConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *icePacketConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.Store(&t)
	return nil
}

// tallyICECandidates counts a session's candidates by type, as candidateCounts does a peer
// connection's. Candidates that can't be parsed are skipped.
func tallyICECandidates(raw []string) candidateTally {
	var t candidateTally
	for _, r := range raw {
		c, err := ice.UnmarshalCandidate(r)
		if err != nil {
			continue
		}
		switch c.Type() {
		case ice.CandidateTypeHost:
			t.host++
		case ice.CandidateTypeServerReflexive:
			t.srflx++
		case ice.CandidateTypePeerReflexive:
			t.prflx++
		case ice.CandidateTypeRelay:
			t.relay++
		}
	}
	return t
}

// summarizeICECandidates reports whether any of a peer's candidates is server reflexive or
// relayed, which only a STUN or TURN server that answered could give, and the last public address
// among them, for the UI's geolocation. Candidates that can't be parsed are skipped.
func summarizeICECandidates(raw []string) (nonHost bool, public net.IP) {
	for _, r := range raw {
		c, err := ice.UnmarshalCandidate(r)
		if err != nil {
			continue
		}
		if c.Type() != ice.CandidateTypeHost {
			nonHost = true
		}
		if isPublicIP(c.Address()) {
			public = net.ParseIP(c.Address())
		}
	}
	return nonHost, public
}
