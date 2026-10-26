//go:build wasm

package clientcore

import (
	"github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

func newWebRTCAPI(_ transport.Net, _ WebRTCTuning) (*webrtc.API, error) {
	panic("Congratulations, you found the unimplemented WebRTC consumer transport injector for Wasm builds!")
	return webrtc.NewAPI(), nil
}

func pionDTLSVersion() string {
	return common.PionDTLSBrowser
}
