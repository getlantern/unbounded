package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/armon/go-socks5"

	"github.com/getlantern/broflake/egress"
)

func main() {
	// Configure slog at debug level — keeps the example's startup messages visible by default.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx := context.Background()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	tlsCertFile := os.Getenv("TLS_CERT_FILE")
	tlsKeyFile := os.Getenv("TLS_KEY_FILE")

	cert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
	if err != nil {
		panic(err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		panic(err)
	}

	ll, err := egress.NewListener(ctx, l, tlsConfig)
	if err != nil {
		panic(err)
	}
	defer ll.Close()

	conf := &socks5.Config{}
	proxy, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}
	slog.Debug(fmt.Sprintf("Starting SOCKS5 proxy..."))

	err = proxy.Serve(ll)
	if err != nil {
		panic(err)
	}
}
