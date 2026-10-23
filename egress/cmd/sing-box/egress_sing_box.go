package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/armon/go-socks5"

	"github.com/getlantern/broflake/egress"
	egcmdcommon "github.com/getlantern/broflake/egress/cmd/common"
)

func main() {
	// Configure slog at debug level — preserves the prior always-on stderr behavior of common.Debugf
	// for the operational debug logs in this standalone binary.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx := context.Background()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		panic(err)
	}
	// See egress/cmd/common/tls.go for where the certificate comes from. A
	// configured certificate that cannot be used is fatal: falling back to the
	// self-signed one would leave every verifying consumer rejecting this egress.
	tlsConfig, source, err := egcmdcommon.LoadEgressTLSConfig(os.Getenv)
	if err != nil {
		slog.Error("Cannot load the egress QUIC client certificate", "source", source, "error", err)
		os.Exit(1)
	}
	if source == egcmdcommon.SourceSelfSigned {
		slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
		slog.Warn("@ DANGER                                                @")
		slog.Warn("@                                                       @")
		slog.Warn("@ No egress TLS certificate is configured, so this      @")
		slog.Warn("@ egress uses a throwaway self-signed one at the QUIC   @")
		slog.Warn("@ layer. Consumers cannot verify it, and consumers that @")
		slog.Warn("@ require verification will reject it.                  @")
		slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	} else {
		slog.Info("Loaded the egress QUIC client certificate", "source", source)
	}

	ll, err := egress.NewListener(ctx, l, tlsConfig)
	if err != nil {
		panic(err)
	}
	defer ll.Close()

	conf := &socks5.Config{
		Dial:     UoTDialer(),
		Resolver: &UoTResolver{},
	}
	proxy, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}
	slog.Debug("Starting SOCKS5 UoT proxy...")

	err = proxy.Serve(ll)
	if err != nil {
		panic(err)
	}
}
