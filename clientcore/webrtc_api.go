//go:build !wasm

package clientcore

import (
	"runtime/debug"
	"sync"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
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

// pionDTLSVersion is the github.com/pion/dtls/v3 version this binary links, as the consumer reports
// it in common.PionDTLSHeader.
var pionDTLSVersion = sync.OnceValue(func() string {
	return dtlsVersion(debug.ReadBuildInfo())
})

// dtlsVersion finds pion/dtls in info's dependencies. Test binaries list none, so they report
// common.PionDTLSNoBuildInfo.
func dtlsVersion(info *debug.BuildInfo, ok bool) string {
	if !ok {
		return common.PionDTLSNoBuildInfo
	}
	for _, m := range info.Deps {
		if m.Path != "github.com/pion/dtls/v3" {
			continue
		}
		if m.Replace != nil {
			m = m.Replace
		}
		return m.Version
	}
	return common.PionDTLSNoBuildInfo
}
