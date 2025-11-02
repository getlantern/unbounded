package clientcore

import (
	"context"
	"net"
	"net/http"
	"net/url"
)

func CreateHTTPTransport(c ReliableStreamLayer) *http.Transport {
	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse("http://i.do.nothing")
		},
		Dial: func(network, addr string) (net.Conn, error) {
			return c.DialContext(context.Background())
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.DialContext(ctx)
		},
	}
}
