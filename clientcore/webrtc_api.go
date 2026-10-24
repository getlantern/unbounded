//go:build !wasm

package clientcore

import (
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

func newWebRTCAPI(net transport.Net, tuning WebRTCTuning) (*webrtc.API, error) {
	if net == nil {
		var err error
		net, err = stdnet.NewNet()
		if err != nil {
			return nil, err
		}
	}
	se := &webrtc.SettingEngine{}
	se.SetNet(net)
	tuning.apply(se)
	return webrtc.NewAPI(webrtc.WithSettingEngine(*se)), nil
}
