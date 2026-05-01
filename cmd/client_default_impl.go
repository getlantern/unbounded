//go:build !wasm

// client_default_impl.go is the entry point for standalone builds for non-wasm build targets
package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

var (
	clientType = "desktop" // Must be "desktop" or "widget"
	proxyMode  = "socks5"  // Must be "socks5" or "http"
)

func main() {
	// Configure slog to log at debug level by default — this standalone client is debug-by-design,
	// preserving the prior always-on stderr behavior of common.Debugf/Debug.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	pprof := os.Getenv("PPROF")
	freddie := os.Getenv("FREDDIE")
	egress := os.Getenv("EGRESS")
	netstated := os.Getenv("NETSTATED")
	tag := os.Getenv("TAG")

	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "1080"
	}
	slog.Debug(fmt.Sprintf("Welcome to Broflake %v", common.Version))
	slog.Debug(fmt.Sprintf("clientType: %v", clientType))
	slog.Debug(fmt.Sprintf("freddie: %v", freddie))
	slog.Debug(fmt.Sprintf("egress: %v", egress))
	slog.Debug(fmt.Sprintf("netstated: %v", netstated))
	slog.Debug(fmt.Sprintf("tag: %v", tag))
	slog.Debug(fmt.Sprintf("pprof: %v", pprof))
	slog.Debug(fmt.Sprintf("proxyPort: %v", proxyPort))

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

	egOpt := clientcore.NewDefaultEgressOptions()

	if egress != "" {
		egOpt.Addr = egress
	}

	bfconn, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		log.Fatal(err)
	}

	if pprof != "" {
		go func() {
			slog.Debug(fmt.Sprint(http.ListenAndServe("localhost:"+pprof, nil)))
		}()
	}

	if clientType == "desktop" {
		runLocalProxy(proxyPort, bfconn)
	}

	select {}
}
