//go:build wasm

package clientcore

import (
	"github.com/pion/transport/v3"
	"github.com/pion/webrtc/v4"
)

func newWebRTCAPI(_ transport.Net) (*webrtc.API, error) {
	panic("Congratulations, you found the unimplemented WebRTC consumer transport injector for Wasm builds!")
	return webrtc.NewAPI(), nil
}
