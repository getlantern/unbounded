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
	ca := os.Getenv("CA")
	serverName := os.Getenv("SERVER_NAME")
	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "1080"
	}

	common.Debugf("Welcome to Broflake %v", common.Version)
	common.Debugf("clientType: %v", clientType)
	common.Debugf("freddie: %v", freddie)
	common.Debugf("egress: %v", egress)
	common.Debugf("netstated: %v", netstated)
	common.Debugf("tag: %v", tag)
	common.Debugf("pprof: %v", pprof)
	common.Debugf("ca: %v", ca)
	common.Debugf("serverName: %v", serverName)
	common.Debugf("proxyPort: %v", proxyPort)

	bfOpt := clientcore.NewDefaultBroflakeOptions()
	bfOpt.ClientType = clientType
	bfOpt.Netstated = netstated

	if clientType == "widget" {
		bfOpt.CTableSize = 5
		bfOpt.PTableSize = 5
	}

	rtcOpt := clientcore.NewDefaultWebRTCOptions()
	rtcOpt.Tag = tag

	if freddie != "" {
		rtcOpt.DiscoverySrv = freddie
	}

	egOpt := clientcore.NewDefaultWebTransportEgressOptions()

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
		runLocalProxy(proxyPort, bfconn, ca, serverName)
	}

	select {}
}
