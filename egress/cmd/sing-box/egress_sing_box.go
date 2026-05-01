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
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@                                                       @")
	slog.Warn("@ This standalone egress server does not use secure TLS @")
	slog.Warn("@ at the QUIC layer!                                    @")
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")

	// And here's why it doesn't use secure TLS at the QUIC layer
	tlsConfig := egcmdcommon.GenerateSelfSignedTLSConfig(true)

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
	slog.Debug(fmt.Sprintf("Starting SOCKS5 UoT proxy..."))

	err = proxy.Serve(ll)
	if err != nil {
		panic(err)
	}
}
