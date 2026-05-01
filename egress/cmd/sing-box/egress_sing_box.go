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
	ctx := context.Background()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		panic(err)
	}
	slog.Debug(fmt.Sprintf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@ DANGER                                                @"))
	slog.Debug(fmt.Sprintf("@                                                       @"))
	slog.Debug(fmt.Sprintf("@ This standalone egress server does not use secure TLS @"))
	slog.Debug(fmt.Sprintf("@ at the QUIC layer!                                    @"))
	slog.Debug(fmt.Sprintf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n"))

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
