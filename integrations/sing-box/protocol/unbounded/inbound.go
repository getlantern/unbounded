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

	UBEgresslib "github.com/getlantern/broflake/egress"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	// "github.com/sagernet/sing-box/common/uot"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// WIP usage: edit sing-box/include/registry.go to import and register this protocol

// TODO: move options types to github.com/sagernet/sing-box/option
type UnboundedInboundOptions struct {
	// TODO: what lives here? You should be able to plumb through two configuration options for
	// the WebSocket listener from config.json -- "listen" (the IP addr) and "listen_port" (the port).
	// This matches the shape of config.json options for the sing-box HTTP inbound...
}

// TODO: move this to github.com/sagernet/sing-box/constant/proxy.go
const TypeUnbounded = "unbounded"

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[UnboundedInboundOptions](registry, TypeUnbounded, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	router   adapter.Router // XXX: is this what we want, or an adapter.Router, or...?
	logger   log.ContextLogger
	listener net.Listener // XXX: this is concretely an egress.proxyListener
}

func NewInbound(
	ctx context.Context,
	router adapter.Router,
	logger log.ContextLogger,
	tag string,
	options UnboundedInboundOptions,
) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(TypeUnbounded, tag),
		router:  router,
		logger:  logger,
	}

	// TODO: get the port from UnboundedInboundOptions
	l, err := net.Listen("tcp", fmt.Sprintf(":%v", 8000))
	if err != nil {
		return nil, err
	}

	// TODO: get this from a sing-box proprietary tls.ServerConfig on the Inbound struct, probably
	tlsConfig := generateSelfSignedTLSConfig()

	ll, err := UBEgresslib.NewListener(ctx, l, tlsConfig)
	if err != nil {
		return nil, err
	}

	inbound.listener = ll

	// TODO: it seems more sensible to implement this loop in the Start function (below), but it
	// seems that sing-box calls Start more than once (?!) at boot
	go func() {
		for {
			// egresslib.proxyListener always returns a nil error, so we ignore it
			conn, _ := inbound.listener.Accept()
			source := M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
			dest := M.SocksaddrFromNet(conn.LocalAddr()).Unwrap()

			// TODO: is there a better context to use here?
			// TODO: we should pass an N.CloseHandlerFunc, but what should it be/do?
			inbound.NewConnectionEx(context.Background(), conn, dest, source, nil)
			select {}
		}
	}()

	return inbound, nil
}

func (i *Inbound) Start(stage adapter.StartStage) error {
	// TODO: start stuff, see existing protocol examples

	// TODO: there must be a way to shut this down with Close()

	return nil
}

func (i *Inbound) Close() error {
	// TODO: close everything down, see existing protocol examples
	return nil
}

func (i *Inbound) NewConnectionEx(
	ctx context.Context,
	conn net.Conn,
	source M.Socksaddr,
	destination M.Socksaddr,
	onClose N.CloseHandlerFunc,
) {
	var metadata adapter.InboundContext
	metadata.Inbound = i.Tag()
	metadata.InboundType = i.Type()

	// TODO: InboundDetour, InboundOptions

	metadata.OriginDestination = M.SocksaddrFromNet(i.listener.Addr()).Unwrap()
	metadata.Source = source
	metadata.Destination = destination

	i.router.RouteConnectionEx(ctx, conn, metadata, onClose)
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
		Certificates:       []tls.Certificate{tlsCert},
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	}
}
