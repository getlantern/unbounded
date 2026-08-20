//go:build !wasm

// client_default_impl.go is the entry point for standalone builds for non-wasm build targets
package main

import (
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

var (
	clientType = "desktop" // Must be "desktop" or "widget"
	proxyMode  = "socks5"  // Must be "socks5" or "http"
)

// logLevelFromEnv resolves the slog level from LOG_LEVEL (debug|info|warn|error).
// This standalone client has historically been debug-by-design, so an unset or
// unrecognized value preserves that behavior; deployments that want a quieter log
// (e.g. the container, which defaults LOG_LEVEL=info) opt in explicitly.
func logLevelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

func main() {
	logLevel := logLevelFromEnv()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	pprof := os.Getenv("PPROF")
	freddie := os.Getenv("FREDDIE")
	egress := os.Getenv("EGRESS")
	netstated := os.Getenv("NETSTATED")
	tag := os.Getenv("TAG")

	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "1080"
	}

	bfOpt := clientcore.NewDefaultBroflakeOptions()
	bfOpt.ClientType = clientType
	bfOpt.Netstated = netstated

	if clientType == "widget" {
		bfOpt.CTableSize = 5
		bfOpt.PTableSize = 5
	}

	// Log every consumer connection delta at INFO. For a widget these are the peers
	// it's proxying for; addr is the consumer's IP on connect and may be nil on
	// disconnect. This is the one connection signal visible when LOG_LEVEL=info.
	bfOpt.OnConnectionChangeFunc = func(state, workerIdx int, addr net.IP) {
		switch state {
		case 1:
			slog.Info("consumer connected", "slot", workerIdx, "addr", addrString(addr))
		case -1:
			slog.Info("consumer disconnected", "slot", workerIdx)
		}
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

	// Emit the resolved (post-override) configuration at INFO so the effective
	// endpoints — including the built-in defaults when an env var is unset — are
	// visible without turning on the debug firehose.
	slog.Info("Welcome to Broflake",
		"version", common.Version,
		"client_type", clientType,
		"log_level", logLevel.String(),
	)
	cfg := []any{
		"discovery", rtcOpt.DiscoverySrv + rtcOpt.Endpoint,
		"egress", egOpt.Addr + egOpt.Endpoint,
		"netstated", orNone(netstated),
		"tag", orNone(tag),
		"pprof", orNone(pprof),
	}
	if clientType == "desktop" {
		// The local proxy (and thus its port and mode) exists only for the desktop
		// client; a widget serves peers over WebRTC and has no local proxy.
		cfg = append(cfg, "proxy_mode", proxyMode, "proxy_port", proxyPort)
	}
	slog.Info("resolved configuration", cfg...)

	bfconn, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		log.Fatal(err)
	}

	if pprof != "" {
		go func() {
			slog.Debug("ListenAndServe returned", "error", http.ListenAndServe("localhost:"+pprof, nil))
		}()
	}

	if clientType == "desktop" {
		runLocalProxy(proxyPort, bfconn)
	}

	select {}
}

// addrString renders a consumer IP for logging, tolerating the nil addr that a
// connection-change callback may carry.
func addrString(addr net.IP) string {
	if addr == nil {
		return "(unknown)"
	}
	return addr.String()
}

// orNone renders an unset optional config value as "(none)" rather than an empty string.
func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}
