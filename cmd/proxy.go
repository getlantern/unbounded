package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/elazarl/goproxy"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

const (
	ip = "127.0.0.1"
)

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

func runLocalProxy(port string, bfconn *clientcore.BroflakeConn) {
	// TODO: this is just to prevent a race with client boot processes, it's not worth getting too
	// fancy with an event-driven solution because the local proxy is all mocked functionality anyway
	<-time.After(2 * time.Second)
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	// This tells goproxy to wrap the dial function in a chained CONNECT request
	proxy.ConnectDial = proxy.NewConnectDialToProxy("http://i.do.nothing")

	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@                                                       @")
	common.Debugf("@ This peer uses an ephemeral self-signed TLS           @")
	common.Debugf("@ certificate at the QUIC layer!                        @")
	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")

	// And here's the ephemeral self-signed TLS certificate at the QUIC layer
	tlsConfig := generateSelfSignedTLSConfig()

	ql, err := clientcore.NewQUICLayer(bfconn, tlsConfig)
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
