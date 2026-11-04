//go:build !wasm

package clientcore

import (
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"
)

// newWebRTCAPI builds the consumer's WebRTC API on pn's transport, UDP mux and
// mapped address.
func newWebRTCAPI(pn iceNet, tuning WebRTCTuning) (*webrtc.API, error) {
	if pn.net == nil {
		var err error
		pn.net, err = stdnet.NewNet()
		if err != nil {
			return nil, err
		}
	}
	se := &webrtc.SettingEngine{}
	if _, err := applyICENet(se, pn); err != nil {
		return nil, err
	}
	tuning.apply(se)
	return webrtc.NewAPI(webrtc.WithSettingEngine(*se)), nil
}
