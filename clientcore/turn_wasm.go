//go:build wasm

package clientcore

import (
	"errors"
	"syscall/js"

	"github.com/pion/webrtc/v4"
)

// addICEServers is the browser's (see turn_native.go for the contract). pion's JS bindings
// reject every turn: and turns: URL in SetConfiguration, but the browser's RTCPeerConnection
// supports TURN, so the servers go into its configuration directly. Its existing servers are the
// STUN servers it was built with, which it takes back unchanged.
func addICEServers(pc *webrtc.PeerConnection, servers []webrtc.ICEServer) (err error) {
	if len(servers) == 0 {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			// A js.Error's message could quote the configuration, so it isn't passed on.
			err = errors.New("could not add ICE servers to the browser's peer connection")
		}
	}()
	underlying := pc.JSValue()
	config := underlying.Call("getConfiguration")
	merged := js.Global().Get("Array").New()
	if existing := config.Get("iceServers"); existing.Type() == js.TypeObject {
		for i := 0; i < existing.Length(); i++ {
			merged.Call("push", existing.Index(i))
		}
	}
	for _, s := range servers {
		urls := make([]any, len(s.URLs))
		for i, u := range s.URLs {
			urls[i] = u
		}
		server := map[string]any{"urls": urls}
		if s.Username != "" {
			server["username"] = s.Username
		}
		if c, ok := s.Credential.(string); ok && c != "" {
			server["credential"] = c
		}
		merged.Call("push", js.ValueOf(server))
	}
	config.Set("iceServers", merged)
	underlying.Call("setConfiguration", config)
	return nil
}
