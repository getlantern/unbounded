//go:build js && wasm

package clientcore

import (
	"github.com/pion/transport/v3"
	"github.com/pion/webrtc/v4"
)

func newWebRTCAPI(_ transport.Net) (*webrtc.API, error) {
	return webrtc.NewAPI(), nil
}
