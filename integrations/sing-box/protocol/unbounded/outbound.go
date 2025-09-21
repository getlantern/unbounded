package unbounded

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"

	UBClientcore "github.com/getlantern/broflake/clientcore"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// WIP usage: edit sing-box/include/registry.go to import and register this protocol

// TODO: move options types to github.com/sagernet/sing-box/option
type UnboundedOutboundOptions struct {
	option.DialerOptions
	option.ServerOptions
}

// TODO: move this to github.com/sagernet/sing-box/constant/proxy.go
const TypeUnbounded = "unbounded"

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[UnboundedOutboundOptions](registry, TypeUnbounded, NewOutbound)
}

type Outbound struct {
	outbound.Adapter
	logger       logger.ContextLogger
	broflakeConn *UBClientcore.BroflakeConn
	QUICLayer    *UBClientcore.QUICLayer
}

func NewOutbound(
	ctx context.Context,
	router adapter.Router,
	logger log.ContextLogger,
	tag string,
	options UnboundedOutboundOptions,
) (adapter.Outbound, error) {
	// TODO: move to UnboundedOutboundOptions and set values correctly
	bfOpt := UBClientcore.NewDefaultBroflakeOptions()
	rtcOpt := UBClientcore.NewDefaultWebRTCOptions()
	egOpt := UBClientcore.NewDefaultEgressOptions()

	BFConn, _, err := UBClientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		return nil, err
	}

	// TODO: move the TLS cert to UnboundedOutboundOptions and get rid of generateSelfSignedTLSConfig()
	QUICLayer, err := UBClientcore.NewQUICLayer(BFConn, generateSelfSignedTLSConfig())
	if err != nil {
		return nil, err
	}

	o := &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(
			TypeUnbounded,
			tag,
			[]string{N.NetworkTCP},
			options.DialerOptions,
		),
		logger:       logger,
		broflakeConn: BFConn,
		QUICLayer:    QUICLayer,
	}

	go QUICLayer.ListenAndMaintainQUICConnection()
	return o, nil
}

func (h *Outbound) DialContext(
	ctx context.Context,
	network string,
	destination M.Socksaddr,
) (net.Conn, error) {
	// TODO: switch on N.NetworkName to handle both TCP and UDP -- see sing-box/protocol/hysteria2/outbound.go
	// also TODO: log something useful with h.logger?
	return h.QUICLayer.DialContext(ctx)
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	// TODO: Figure out how to carry UDP over Unbounded... maybe with QUIC datagrams?
	return nil, fmt.Errorf("Congrats, you found the missing ListenPacket implementation")
}

// TODO: delete me
func generateSelfSignedTLSConfig() *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"broflake"},
	}
}
