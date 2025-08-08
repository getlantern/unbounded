package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/elazarl/goproxy"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

const (
	ip = "127.0.0.1"
)

func runLocalProxy(port string, bfconn *clientcore.BroflakeConn) {
	// TODO: this is just to prevent a race with client boot processes, it's not worth getting too
	// fancy with an event-driven solution because the local proxy is all mocked functionality anyway
	<-time.After(2 * time.Second)
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	// This tells goproxy to wrap the dial function in a chained CONNECT request
	proxy.ConnectDial = proxy.NewConnectDialToProxy("http://i.do.nothing")

	ql, err := clientcore.NewQUICLayer(bfconn)
	if err != nil {
		common.Debugf("Cannot start local HTTP proxy: failed to create QUIC layer: %v", err)
		return
	}

	go ql.ListenAndMaintainQUICConnection()
	proxy.Tr = clientcore.CreateHTTPTransport(ql)

	proxy.OnRequest().DoFunc(
		func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			common.Debug("HTTP proxy just saw a request:")
			common.Debug(r)
			return r, nil
		},
	)

	addr := fmt.Sprintf("%v:%v", ip, port)

	go func() {
		common.Debugf("Starting HTTP CONNECT proxy on %v...", addr)
		err := http.ListenAndServe(addr, proxy)
		if err != nil {
			common.Debugf("HTTP CONNECT proxy error: %v", err)
		}
	}()
}
