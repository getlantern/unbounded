package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/elazarl/goproxy"

	"github.com/getlantern/broflake/egress"
	egcmdcommon "github.com/getlantern/broflake/egress/cmd/common"
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
	slog.Debug(fmt.Sprintf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@                                                       @"))
	slog.Debug(fmt.Sprintf("@ This standalone egress server does not use secure TLS @"))
	slog.Debug(fmt.Sprintf("@ at the QUIC layer!                                    @"))
	slog.Debug(fmt.Sprintf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n"))

	// And here's why it doesn't use secure TLS at the QUIC layer
	tlsConfig := egcmdcommon.GenerateSelfSignedTLSConfig(true)

	ll, err := egress.NewListener(ctx, l, tlsConfig)
	if err != nil {
		panic(err)
	}
	defer ll.Close()

	// Instantiate our local HTTP CONNECT proxy
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	slog.Debug(fmt.Sprintf("Starting HTTP CONNECT proxy..."))

	proxy.OnRequest().DoFunc(
		func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			slog.Debug(fmt.Sprint("HTTP proxy just saw a request:"))
			// TODO: overriding the context is a hack to prevent "context canceled" errors when proxying
			// HTTP (not HTTPS) requests. It's not yet clear why this is necessary -- it may be a quirk
			// of elazarl/goproxy. See: https://github.com/getlantern/broflake/issues/47
			r = r.WithContext(context.Background())
			slog.Debug(fmt.Sprint(r))
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
