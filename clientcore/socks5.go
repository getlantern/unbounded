package clientcore

import (
	"context"
	"net"

	"github.com/armon/go-socks5"
)

func CreateSOCKS5Config(c ReliableStreamLayer) *socks5.Config {
	return &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.DialContext(ctx)
		},
	}
}
