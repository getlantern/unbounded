package clientcore

import (
	"context"
	"net"
)

// ExitOptions let a producer carry sessions over ICE alone and exit their traffic itself, instead
// of relaying it to the egress server. A browser widget, which can't dial destinations, leaves
// them nil. Ready, Params and OnSession run on the signaling path, so they must be cheap and must
// not block.
//
// Each session is personal or pool, by the genesis it answers. A personal session's genesis went
// to the producer's rooms alone, so only a consumer in one of them (a friend) could take it. Every
// other genesis may reach any consumer (a stranger), and its sessions are pool sessions. The
// embedder decides what each kind may reach.
type ExitOptions struct {
	// Ready reports whether the producer can exit a session of this kind now. While it can, each
	// genesis of that kind offers a direct exit (see common.GenesisMsg.DirectExit).
	Ready func(personal bool) bool
	// Params mints a session's credentials: the SHA-256 pin of the exit certificate's public key,
	// and a password for this session alone. ok is false when the producer can no longer exit a
	// session of this kind.
	Params func(personal bool) (certPin, password string, ok bool)
	// Serve serves one session on conn, which carries only that session's consumer, with the
	// policy for its kind. It runs on its own goroutine and must return once ctx ends or conn's
	// reads fail.
	Serve func(ctx context.Context, conn net.PacketConn, password string, personal bool)
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
