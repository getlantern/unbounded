//go:build !wasm

// client_default_impl.go is the entry point for standalone builds for non-wasm build targets
package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

var (
	clientType = "desktop" // Must be "desktop" or "widget"
)

func main() {
	pprof := os.Getenv("PPROF")
	freddie := os.Getenv("FREDDIE")
	egress := os.Getenv("EGRESS")
	netstated := os.Getenv("NETSTATED")
	tag := os.Getenv("TAG")

	// TODO nelson 07/24/2025: in reversing the QUIC client and server while implementing connection
	// migration, we no longer need to plumb through a CA cert for QUIC layer TLS, because this client
	// now acts as the QUIC server rather than the QUIC client. But the CA value has been entangled
	// with WebTransport in a way that makes it difficult to extract. It should be removed as part
	// of this ticket: https://github.com/getlantern/engineering/issues/2334

	// the path to the crt file.
	// In "desktop" it will be used to run local proxy, and in "widget" it will be used to connect to WebTransport (if enabled)
	ca := os.Getenv("CA")
	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "1080"
	}
	webTransport, webTransportEnabled := os.Getenv("WEBTRANSPORT"), false
	if webTransport == "1" {
		webTransportEnabled = true
	}

	common.Debugf("Welcome to Broflake %v", common.Version)
	common.Debugf("clientType: %v", clientType)
	common.Debugf("freddie: %v", freddie)
	common.Debugf("egress: %v", egress)
	common.Debugf("netstated: %v", netstated)
	common.Debugf("tag: %v", tag)
	common.Debugf("pprof: %v", pprof)
	common.Debugf("ca: %v", ca)
	common.Debugf("proxyPort: %v", proxyPort)
	common.Debugf("webtransport: %v", webTransportEnabled)

	bfOpt := clientcore.NewDefaultBroflakeOptions()
	bfOpt.ClientType = clientType
	bfOpt.Netstated = netstated
	bfOpt.WebTransport = webTransportEnabled

	if clientType == "widget" {
		bfOpt.CTableSize = 5
		bfOpt.PTableSize = 5
	}

	rtcOpt := clientcore.NewDefaultWebRTCOptions()
	rtcOpt.Tag = tag

	if freddie != "" {
		rtcOpt.DiscoverySrv = freddie
	}

	var egOpt *clientcore.EgressOptions
	if webTransportEnabled {
		// load ca file if any
		var caBytes []byte
		if ca != "" {
			pem, err := os.ReadFile(ca)
			if err != nil {
				log.Fatal(err)
			}
			caBytes = pem
		}
		egOpt = clientcore.NewDefaultWebTransportEgressOptions(caBytes)
	} else {
		egOpt = clientcore.NewDefaultWebSocketEgressOptions()
	}

	if egress != "" {
		egOpt.Addr = egress
	}

	bfconn, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		log.Fatal(err)
	}

	if pprof != "" {
		go func() {
			common.Debug(http.ListenAndServe("localhost:"+pprof, nil))
		}()
	}

	if clientType == "desktop" {
		runLocalProxy(proxyPort, bfconn)
	}

	select {}
}
