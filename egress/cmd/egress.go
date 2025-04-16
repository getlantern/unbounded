package main

import (
	"context"
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

	certFile := os.Getenv("TLS_CERT")
	keyFile := os.Getenv("TLS_KEY")

	webTransport, webTransportEnabled := os.Getenv("WEBTRANSPORT"), false
	if webTransport == "1" {
		webTransportEnabled = true
	}

	var tlsCert string
	var tlsKey string

	// XXX: in the process of delivering the cert and key to egress.NewListener, we suboptimally
	// cast back and forth between []string and []byte... it's just a byproduct of the API
	if certFile != "" && keyFile != "" {
		cert, err := os.ReadFile(certFile)
		if err != nil {
			panic(err)
		}
		tlsCert = string(cert)

		key, err := os.ReadFile(keyFile)
		if err != nil {
			panic(err)
		}
		tlsKey = string(key)
	}

	addr := fmt.Sprintf(":%v", port)
	var ll net.Listener
	var err error
	if webTransportEnabled {
		ll, err = egress.NewWebTransportListener(ctx, addr, tlsCert, tlsKey)
	} else {
		baseListen, err := net.Listen("tcp", addr)
		if err != nil {
			panic(err)
		}
		ll, err = egress.NewWebSocketListener(ctx, baseListen, tlsCert, tlsKey)
	}
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
