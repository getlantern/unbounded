//go:build wasm

package clientcore

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/pion/webrtc/v4"
)

// A browser has no bare ICE agent, and a widget never offers a direct exit (WebRTCOptions.Exit is
// nil there) and never runs a consumer, so these are never reached.
var errICEUnavailable = errors.New("direct exit sessions are native only")

var errBadCandidate = errors.New("bad ICE candidate")

const maxRemoteCandidates = 32

type iceSession struct{}

func newICESession(*WebRTCOptions, bool, []webrtc.ICEServer, time.Duration) (*iceSession, error) {
	return nil, errICEUnavailable
}

func (s *iceSession) credentials() (string, string, error) { return "", "", errICEUnavailable }
func (s *iceSession) gather(time.Duration) ([]string, gatherEnd, error) {
	return nil, 0, errICEUnavailable
}
func (s *iceSession) addRemote([]string) error                      { return errICEUnavailable }
func (s *iceSession) connect(context.Context, string, string) error { return errICEUnavailable }
func (s *iceSession) packetConn() net.PacketConn                    { return nil }
func (s *iceSession) relayed() bool                                 { return false }
func (s *iceSession) done() <-chan struct{}                         { return nil }
func (s *iceSession) Close() error                                  { return nil }

func summarizeICECandidates([]string) (bool, net.IP) { return false, nil }
