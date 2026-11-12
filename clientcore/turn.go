package clientcore

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// addICEServers adds servers (TURN relays Freddie sent for a room pairing) to pc's ICE servers.
// ICE gathers with the new servers only if this runs before SetLocalDescription. An empty list
// changes nothing.
func addICEServers(pc *webrtc.PeerConnection, servers []webrtc.ICEServer) error {
	if len(servers) == 0 {
		return nil
	}
	config := pc.GetConfiguration()
	config.ICEServers = append(append([]webrtc.ICEServer(nil), config.ICEServers...), servers...)
	return pc.SetConfiguration(config)
}

// turnGatherWait bounds ICE gathering on a peer connection that has a room pairing's TURN servers.
// Gathering completes only once every server has answered or failed, and pion dials a TCP or TLS
// TURN server with no timeout, so a censor that drops its SYNs would hold gathering for the OS
// connect timeout (over a minute), and a blocked UDP TURN server for seconds of allocate
// retransmits. Freddie gives each side remoteICEGatheringTTL (15s) to gather and reply, so the
// attempt would fail rather than fall back to the candidates it has. 8s is past pion's 5s STUN
// gather timeout, so host and server-reflexive candidates are in by then, and leaves 7s of
// Freddie's window for signaling them.
const turnGatherWait = 8 * time.Second

// awaitGathering waits for gatherComplete, or for at most limit if limit is positive, and reports
// whether gathering completed. Gathering goes on after a wait that gives up.
func awaitGathering(gatherComplete <-chan struct{}, limit time.Duration) bool {
	if limit <= 0 {
		<-gatherComplete
		return true
	}
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-gatherComplete:
		return true
	case <-t.C:
		return false
	}
}

// candidateLog collects the local candidates OnICECandidate reports. A bounded wait reads
// them while gathering may still be reporting more, so they are locked.
type candidateLog struct {
	mu    sync.Mutex
	cands []webrtc.ICECandidate
}

// add records c, skipping the null candidate (a nil pointer) that marks the end of gathering.
func (g *candidateLog) add(c *webrtc.ICECandidate) {
	if c == nil {
		return
	}
	g.mu.Lock()
	g.cands = append(g.cands, *c)
	g.mu.Unlock()
}

// snapshot returns a copy of the candidates gathered so far, which later ones don't change.
func (g *candidateLog) snapshot() []webrtc.ICECandidate {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]webrtc.ICECandidate(nil), g.cands...)
}

// selectedPath reports whether pc's selected candidate pair goes through a TURN relay:
// common.PathRelay, common.PathDirect, or "" when no pair is selected or the browser doesn't say.
func selectedPath(pc *webrtc.PeerConnection) (path string) {
	defer func() {
		if recover() != nil {
			path = ""
		}
	}()
	sctp := pc.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return ""
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return ""
	}
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		return common.PathRelay
	}
	return common.PathDirect
}
