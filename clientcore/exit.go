package clientcore

import (
	"context"
	"net"
)

// ExitOptions let a producer carry sessions over ICE alone and exit their traffic itself, instead
// of relaying it to the egress server. A browser widget, which can't dial destinations, leaves
// them nil. Ready, Params and OnSession run on the signaling path, so they must be cheap and must
// not block.
type ExitOptions struct {
	// Ready reports whether the producer can exit traffic now. While it can, each genesis offers
	// a direct exit (see common.GenesisMsg.DirectExit).
	Ready func() bool
	// Params mints a session's credentials: the SHA-256 pin of the exit certificate's public key,
	// and a password for this session alone. ok is false when the producer can no longer exit.
	Params func() (certPin, password string, ok bool)
	// Serve serves one session on conn, which carries only that session's consumer. It runs on
	// its own goroutine and must return once ctx ends or conn's reads fail.
	Serve func(ctx context.Context, conn net.PacketConn, password string)
	// OnSession, if set, is called as each session connects, with whether it is a direct exit
	// or relays to the egress server as before.
	OnSession func(directExit bool)
}

// ExitPath is a consumer's connected direct exit session. Conn reaches the producer's exit, which
// takes only the certificate CertPin pins and the session's Password. Conn's addresses are
// placeholders, the same for every session, not either side's: its peer is always the producer.
// Relayed says whether ICE chose a TURN relay. Closing Conn ends the session, and once the session
// ends, Conn's reads fail. Never logged: it carries the session's credentials.
type ExitPath struct {
	Conn     net.PacketConn
	CertPin  string
	Password string
	Relayed  bool
}

// exitReady reports whether a producer can offer a direct exit now. Without Params and Serve,
// state 6 would refuse every offer the genesis drew in.
func exitReady(options *WebRTCOptions) bool {
	e := options.Exit
	return e != nil && e.Ready != nil && e.Params != nil && e.Serve != nil && e.Ready()
}

// exitParams mints a direct exit session's credentials, reporting false if the producer can't
// serve one.
func exitParams(options *WebRTCOptions) (certPin, password string, ok bool) {
	if options.Exit == nil || options.Exit.Params == nil || options.Exit.Serve == nil {
		return "", "", false
	}
	return options.Exit.Params()
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
