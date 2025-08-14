package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/elazarl/goproxy"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/egress"
)

func main() {
	ctx := context.Background()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		panic(err)
	}

	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@                                                       @")
	common.Debugf("@ This standalone egress server does not use secure TLS @")
	common.Debugf("@ at the QUIC layer!                                    @")
	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")

	// And here's why it doesn't use secure TLS at the QUIC layer
	tlsConfig := &tls.Config{
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	}

	ll, err := egress.NewListener(ctx, l, tlsConfig)
	if err != nil {
		panic(err)
	}
	defer ll.Close()

	// Instantiate our local HTTP CONNECT proxy
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	common.Debugf("Starting HTTP CONNECT proxy...")

	proxy.OnRequest().DoFunc(
		func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			common.Debug("HTTP proxy just saw a request:")
			// TODO: overriding the context is a hack to prevent "context canceled" errors when proxying
			// HTTP (not HTTPS) requests. It's not yet clear why this is necessary -- it may be a quirk
			// of elazarl/goproxy. See: https://github.com/getlantern/broflake/issues/47
			r = r.WithContext(context.Background())
			common.Debug(r)
			return r, nil
		},
	)

	proxy.OnResponse().DoFunc(
		func(r *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
			// TODO: log something interesting?
			return r
		},
	)

	err = http.Serve(ll, proxy)
	if err != nil {
		panic(err)
	}
}
