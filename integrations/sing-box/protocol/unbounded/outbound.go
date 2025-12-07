package unbounded

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"

	UBClientcore "github.com/getlantern/broflake/clientcore"
	UBCommon "github.com/getlantern/broflake/common"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	singlog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// WIP usage: edit sing-box/include/registry.go to import and register this protocol

type logAdapter struct {
	singBoxLogger singlog.ContextLogger
}

func (l logAdapter) Write(p []byte) (int, error) {
	l.singBoxLogger.Info(string(p))
	return len(p), nil
}

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
	dial         UBClientcore.SOCKS5Dialer
}

func NewOutbound(
	ctx context.Context,
	router adapter.Router,
	logger singlog.ContextLogger,
	tag string,
	options UnboundedOutboundOptions,
) (adapter.Outbound, error) {
	// TODO: move to UnboundedOutboundOptions and set values correctly
	bfOpt := UBClientcore.NewDefaultBroflakeOptions()
	rtcOpt := UBClientcore.NewDefaultWebRTCOptions()
	egOpt := UBClientcore.NewDefaultEgressOptions()

	la := logAdapter{
		singBoxLogger: logger,
	}

	UBCommon.SetDebugLogger(log.New(la, "", 0))

	BFConn, _, err := UBClientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		return nil, err
	}

	// TODO: we need to get rid of generateSelfSignedTLSConfig() and use a proper TLS cert here. It
	// should *prbably* be a sing-box tls.ServerConfig, though it's not clear how that interface
	// vibes with what the QUIC library expects... alternatively, we could maybe add a tls.Config to
	// the outbound options? But unsure how to get that plumbed through end to end...
	QUICLayer, err := UBClientcore.NewQUICLayer(BFConn, generateSelfSignedTLSConfig())
	if err != nil {
		return nil, err
	}

	dialer := UBClientcore.CreateSOCKS5Dialer(QUICLayer)

	o := &Outbound{
		Adapter: outbound.NewAdapterWithDialerOptions(
			TypeUnbounded,
			tag,
			[]string{N.NetworkTCP}, // XXX: Unbounded only supports TCP (not UDP) for now
			options.DialerOptions,
		),
		logger:       logger,
		broflakeConn: BFConn,
		dial:         dialer,
	}

	go QUICLayer.ListenAndMaintainQUICConnection()
	return o, nil
}

func (h *Outbound) DialContext(
	ctx context.Context,
	network string,
	destination M.Socksaddr,
) (net.Conn, error) {
	// XXX: this is the log pattern for N.NetworkTCP
	h.logger.InfoContext(ctx, "outbound connection to ", destination)

	// XXX: network is ignored by Unbounded's SOCKS5 dialer
	return h.dial(ctx, network, destination.String())
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
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
