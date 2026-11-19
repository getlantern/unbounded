package clientcore

import (
	"context"
	"net"
	"sync/atomic"
)

// ExitOptions let a producer carry sessions over ICE alone and exit their traffic itself, instead
// of relaying it to the egress server. A browser widget, which can't dial destinations, leaves
// them nil. Ready, Params and OnSession run on the signaling path, so they must be cheap and must
// not block.
//
// Each session is personal or pool, by the offer that opens it. A personal session's offer claims
// the pairing of one of the producer's own volunteer tickets, which Freddie stamped on the room
// genesis it delivered to that consumer (a friend). Every other session is a pool session: a
// stranger sees only pool genesis messages, which carry no pairing. The embedder decides what each
// kind may reach, and the consumer learns its session's kind (ExitPath.Personal).
//
// The exit is obfuscated: Serve, and the embedder's consumer side that takes each ExitPath, must
// both run Hysteria2 with Salamander keyed from the session password. broflake only carries the
// signal (common.GenesisMsg.DirectExitObfs) and the password; the key derivation lives with the
// embedder (lantern-box). Peers that predate obfuscation never pair with these exits: they relay.
type ExitOptions struct {
	// Ready reports whether the producer can exit a session of this kind now. While it can exit a
	// kind that a consumer reaching a genesis could get, the genesis offers an obfuscated direct
	// exit (see common.GenesisMsg.DirectExitObfs). An offer for a kind it can't exit is dropped.
	Ready func(personal bool) bool
	// Params mints a session's credentials: the SHA-256 pin of the exit certificate's public key,
	// and a password for this session alone. ok is false when the producer can no longer exit a
	// session of this kind.
	Params func(personal bool) (certPin, password string, ok bool)
	// Serve serves one session on conn, which carries only that session's consumer, with the
	// policy for its kind, obfuscated with Salamander keyed from password. It runs on its own goroutine and must return once ctx ends or conn's
	// reads fail.
	Serve func(ctx context.Context, conn net.PacketConn, password string, personal bool)
	// OnSession, if set, is called as each session connects, with whether it is a direct exit
	// or relays to the egress server as before.
	OnSession func(directExit bool)
}

// ExitPath is a consumer's connected direct exit session (see common.GenesisMsg.DirectExitObfs).
// Conn reaches the producer's exit, which takes only the certificate CertPin pins and the
// session's Password, and is obfuscated with Salamander keyed from Password (see ExitOptions). Conn's addresses are
// placeholders, the same for every session, not either side's: its peer is always the producer.
// Relayed says whether ICE chose a TURN relay. Closing Conn ends the session, and once the session
// ends, Conn's reads fail. Never logged: it carries the session's credentials.
type ExitPath struct {
	Conn     net.PacketConn
	CertPin  string
	Password string
	Relayed  bool
	// Personal says whether the producer serves this session under its personal policy, with no
	// allowlist, rather than the pool's (see ExitOptions). A consumer must send a pool session
	// only what the pool policy admits.
	Personal bool
}

// exitReady reports whether a producer can offer a direct exit of this kind now. Without Params
// and Serve, state 6 would refuse every offer the genesis drew in.
func exitReady(options *WebRTCOptions, personal bool) bool {
	e := options.Exit
	return e != nil && e.Ready != nil && e.Params != nil && e.Serve != nil && e.Ready(personal)
}

// exitParams mints a direct exit session's credentials, reporting false if the producer can't
// serve one of this kind. It asks Ready again: an offer can arrive after readiness was withdrawn,
// in reply to a genesis sent before.
func exitParams(options *WebRTCOptions, personal bool) (certPin, password string, ok bool) {
	if !exitReady(options, personal) {
		return "", "", false
	}
	return options.Exit.Params(personal)
}

// observeSession tells the embedder that a session connected, and whether it is a direct exit.
func observeSession(options *WebRTCOptions, directExit bool) {
	if options.Exit != nil && options.Exit.OnSession != nil {
		options.Exit.OnSession(directExit)
	}
}

// takeExit reports whether a consumer takes a direct exit now: its embedder takes sessions, and
// hasn't backed off them.
func takeExit(options *WebRTCOptions) bool {
	return options.OnExitPath != nil && (options.TakeExit == nil || options.TakeExit())
}

// exitBytes is the data of an ExitBytesIPC: the bytes a direct exit session carried since its
// last report. up is what the consumer sent the exit, down what the exit sent back, matching the
// directions of relayed chunks. They are int64 because an int is 32 bits on some platforms, and
// counts held back while the slot's channel is full can grow past that.
type exitBytes struct {
	up, down int64
}

// countingPacketConn counts the bytes a direct exit session's conn carries, so that they reach
// the UI as relayed bytes do.
type countingPacketConn struct {
	net.PacketConn
	up, down atomic.Int64
}

func (c *countingPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if n > 0 {
		c.up.Add(int64(n))
	}
	return n, addr, err
}

func (c *countingPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	if n > 0 {
		c.down.Add(int64(n))
	}
	return n, err
}

// take returns the bytes counted since the last take.
func (c *countingPacketConn) take() exitBytes {
	return exitBytes{up: c.up.Swap(0), down: c.down.Swap(0)}
}

// reportExitBytes sends the bytes conn counted since its last report to the UI, if there are
// any, reporting false if ctx ended first.
func reportExitBytes(ctx context.Context, com *ipcChan, conn *countingPacketConn) bool {
	b := conn.take()
	if b.up == 0 && b.down == 0 {
		return true
	}
	return sendCtx(ctx, com.tx, IPCMsg{IpcType: ExitBytesIPC, Data: b})
}

// tryReportExitBytes is reportExitBytes for the periodic report: when the slot's channel is full
// it keeps the counts for the next report rather than wait.
func tryReportExitBytes(com *ipcChan, conn *countingPacketConn) {
	b := conn.take()
	if b.up == 0 && b.down == 0 {
		return
	}
	select {
	case com.tx <- IPCMsg{IpcType: ExitBytesIPC, Data: b}:
	default:
		conn.up.Add(b.up)
		conn.down.Add(b.down)
	}
}
