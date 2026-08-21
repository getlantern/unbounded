//go:build !wasm

// client_default_impl.go is the entry point for standalone builds for non-wasm build targets
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/getlantern/broflake/clientcore"
	"github.com/getlantern/broflake/common"
)

// defaultStatsInterval is how often the widget logs its aggregate stats when
// STATS_INTERVAL is unset.
const defaultStatsInterval = 60 * time.Second

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

	// NAT_FAIL_TIMEOUT bounds how long a producer waits for a WebRTC connection to
	// come up before abandoning the attempt. The default (5s) frequently cuts ICE
	// off while it is still in the "checking" state, so expose it as a knob for
	// diagnosing/curing premature timeouts without a rebuild. Any Go duration.
	if v := strings.TrimSpace(os.Getenv("NAT_FAIL_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			rtcOpt.NATFailTimeout = d
		} else {
			slog.Warn("ignoring invalid NAT_FAIL_TIMEOUT", "value", v, "default", rtcOpt.NATFailTimeout)
		}
	}

	// HANDSHAKE_TIMEOUT bounds the post-ICE phase (DTLS/SCTP/datachannel). See
	// WebRTCOptions.HandshakeTimeout. Any Go duration.
	if v := strings.TrimSpace(os.Getenv("HANDSHAKE_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			rtcOpt.HandshakeTimeout = d
		} else {
			slog.Warn("ignoring invalid HANDSHAKE_TIMEOUT", "value", v, "default", rtcOpt.HandshakeTimeout)
		}
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
		"nat_fail_timeout", rtcOpt.NATFailTimeout,
		"handshake_timeout", rtcOpt.HandshakeTimeout,
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

	if clientType == "widget" {
		go logNATCheck(rtcOpt)
		go logWidgetStats(statsInterval())
	}

	if clientType == "desktop" {
		slog.Info("running local proxy")
		runLocalProxy(proxyPort, bfconn)
	}

	select {}
}

// statsInterval resolves the widget stats logging cadence from STATS_INTERVAL
// (any Go duration string, e.g. "30s", "5m"). An unset, unparseable, or
// non-positive value falls back to defaultStatsInterval.
func statsInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("STATS_INTERVAL"))
	if v == "" {
		return defaultStatsInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("ignoring invalid STATS_INTERVAL, using default", "value", v, "default", defaultStatsInterval)
		return defaultStatsInterval
	}
	return d
}

// logWidgetStats periodically logs a snapshot of the engine's lifetime counters:
// WebRTC session-attempt outcomes (total, succeeded, and failures by reason),
// currently-active and distinct peers served, total bytes relayed each way, and
// the average bytes relayed per successful session. Runs until the process exits.
func logWidgetStats(interval time.Duration) {
	slog.Info("widget stats logging enabled", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s := clientcore.Stats()
		total := s.BytesToPeers + s.BytesFromPeers
		succeeded := s.Succeeded()

		// Averages are per successful session — the widget's unit of service.
		var avgTo, avgFrom uint64
		if succeeded > 0 {
			avgTo = s.BytesToPeers / succeeded
			avgFrom = s.BytesFromPeers / succeeded
		}

		slog.Info("widget stats",
			"attempts", s.Attempts(),
			"succeeded", succeeded,
			"failed", s.Failed(),
			"failures", failuresByReason(s.Outcomes),
			"active_peers", s.ActivePeers,
			"distinct_peers", s.DistinctPeers,
			"egress_dials", s.EgressDials,
			"to_peers", humanBytes(s.BytesToPeers),
			"from_peers", humanBytes(s.BytesFromPeers),
			"total", humanBytes(total),
			"avg_to_peer", humanBytes(avgTo),
			"avg_from_peer", humanBytes(avgFrom),
		)
	}
}

// failuresByReason renders the non-success outcome tallies as a compact, sorted,
// stable string like "nat-traversal-timeout=6 ice-failed=1", or "(none)".
func failuresByReason(outcomes map[string]uint64) string {
	reasons := make([]string, 0, len(outcomes))
	for r := range outcomes {
		if r != clientcore.OutcomeSuccess {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) == 0 {
		return "(none)"
	}
	sort.Strings(reasons)
	var b strings.Builder
	for i, r := range reasons {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%d", r, outcomes[r])
	}
	return b.String()
}

// logNATCheck probes this host's NAT mapping behavior at startup and logs the
// verdict. A widget can only serve peers over STUN (no TURN), so a symmetric NAT
// is the difference between "can help" and "cannot help" — worth surfacing once,
// up front, rather than leaving the operator to infer it from failed attempts.
func logNATCheck(rtcOpt *clientcore.WebRTCOptions) {
	servers, err := rtcOpt.STUNBatch(8)
	if err != nil || len(servers) < 2 {
		slog.Warn("NAT mapping self-check skipped", "error", err, "stun_servers", len(servers))
		return
	}
	if len(servers) > 5 {
		servers = servers[:5]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := clientcore.CheckNATMapping(ctx, servers, 3*time.Second)

	switch res.Mapping {
	case clientcore.NATMappingEndpointIndependent:
		slog.Info("NAT mapping self-check",
			"result", res.Mapping.String(),
			"public", res.PublicAddrs,
			"samples", res.Samples,
			"detail", "stable public mapping across STUN servers; STUN hole-punching is viable from this host",
		)
	case clientcore.NATMappingEndpointDependent:
		slog.Warn("NAT mapping self-check",
			"result", res.Mapping.String(),
			"public", res.PublicAddrs,
			"samples", res.Samples,
			"detail", "SYMMETRIC NAT: the public port varies by destination, so STUN-only P2P (Unbounded has no TURN) will fail for most peers; put this host on a public IP or a cone NAT to serve reliably",
		)
	default:
		slog.Warn("NAT mapping self-check inconclusive",
			"samples", res.Samples,
			"detail", "not enough STUN responses to determine NAT mapping behavior",
		)
	}
}

// humanBytes renders a byte count as a human-readable string (e.g. "1.5 MiB").
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
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
