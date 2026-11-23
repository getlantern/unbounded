//go:build !wasm

package clientcore

import (
	"github.com/pion/webrtc/v4"
)

// newWebRTCAPI builds the consumer's WebRTC API on pn's transport, UDP mux and
// mapped address.
func newWebRTCAPI(pn iceNet, tuning WebRTCTuning) (*webrtc.API, error) {
	se := &webrtc.SettingEngine{}
	if _, err := applyICENet(se, pn); err != nil {
		return nil, err
	}
	tuning.apply(se)
	return webrtc.NewAPI(webrtc.WithSettingEngine(*se)), nil
}
