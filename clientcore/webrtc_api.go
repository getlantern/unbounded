//go:build !wasm

package clientcore

import (
	"github.com/pion/transport/v3"
	"github.com/pion/transport/v3/stdnet"
	"github.com/pion/webrtc/v4"
)

func newWebRTCAPI(net transport.Net) (*webrtc.API, error) {
	if net == nil {
		var err error
		net, err = stdnet.NewNet()
		if err != nil {
			return nil, err
		}
	}
	se := &webrtc.SettingEngine{}
	se.SetNet(net)
	return webrtc.NewAPI(webrtc.WithSettingEngine(*se)), nil
}
