package clientcore

import (
	"net"

	"github.com/pion/ice/v2"
	"github.com/pion/webrtc/v3"
)

// createPeerConnection creates a new RTCPeerConnection using the given PacketConn
func createPeerConnection(pconn net.PacketConn, config webrtc.Configuration) (*webrtc.PeerConnection, error) {
	if pconn == nil {
		return webrtc.NewPeerConnection(config)
	}
	udpMux := ice.NewUDPMuxDefault(ice.UDPMuxParams{
		UDPConn: pconn,
	})
	se := webrtc.SettingEngine{}
	se.SetICEUDPMux(udpMux)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	se.SetInterfaceFilter(func(string) bool { return false })
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(config)
}
