//go:build !wasm

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"time"

	"github.com/armon/go-socks5"
	"github.com/elazarl/goproxy"

	"github.com/getlantern/broflake/clientcore"
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
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@                                                       @")
	slog.Warn("@ This peer uses an ephemeral self-signed TLS           @")
	slog.Warn("@ certificate at the QUIC layer!                        @")
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")

	// And here's the ephemeral self-signed TLS certificate at the QUIC layer
	tlsConfig := generateSelfSignedTLSConfig()

	ql, err := clientcore.NewQUICLayer(bfconn, tlsConfig)
	if err != nil {
		slog.Debug(fmt.Sprintf("Cannot start local proxy: failed to create QUIC layer: %v", err))
		return
	}

	addr := fmt.Sprintf("%v:%v", ip, port)

	switch proxyMode {
	case "socks5":
		go ql.ListenAndMaintainQUICConnection()
		conf := &socks5.Config{
			Dial: clientcore.CreateSOCKS5Dialer(ql),
		}
		socks5, err := socks5.New(conf)
		if err != nil {
			panic(err)
		}

		go func() {
			slog.Debug(fmt.Sprintf("Starting SOCKS5 proxy on %v...", addr))
			err := socks5.ListenAndServe("tcp", addr)
			if err != nil {
				panic(err)
			}
		}()
	case "http":
		proxy := goproxy.NewProxyHttpServer()
		proxy.Verbose = true
		// This tells goproxy to wrap the dial function in a chained CONNECT request
		proxy.ConnectDial = proxy.NewConnectDialToProxy("http://i.do.nothing")

		go ql.ListenAndMaintainQUICConnection()
		proxy.Tr = clientcore.CreateHTTPTransport(ql)

		proxy.OnRequest().DoFunc(
			func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
				slog.Debug(fmt.Sprint("HTTP proxy just saw a request:"))
				slog.Debug(fmt.Sprint(r))
				return r, nil
			},
		)

		go func() {
			slog.Debug(fmt.Sprintf("Starting HTTP CONNECT proxy on %v...", addr))
			err := http.ListenAndServe(addr, proxy)
			if err != nil {
				panic(err)
			}
		}()
	}
}
