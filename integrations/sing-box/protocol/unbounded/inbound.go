package unbounded

import (
	"net"

	UBEgresslib "github.com/getlantern/broflake/egress"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing/common/logger"
)

// WIP usage: edit sing-box/include/registry.go to import and register this protocol

// TODO: move options types to github.com/sagernet/sing-box/option
type UnboundedInboundOptions struct {
	// TODO: what lives here?
}

// TODO: move this to github.com/sagernet/sing-box/constant/proxy.go
const TypeUnbounded = "unbounded"

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[UnboundedInboundOptions](registry, TypeUnbounded, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	router   adapter.ConnectionRouterEx // XXX: is this what we want, or an adapter.Router, or...?
	logger   log.ContextLogger
	listener *net.Listener // XXX: this is concretely an egress.proxyListener
}

func NewInbound(
	ctx context.Context,
	router adapter.Router,
	logger log.ContextLogger,
	tag string,
	options UnboundedInboundOptions,
) (adapter.Inbound, error) {

}
