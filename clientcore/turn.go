package clientcore

import (
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
