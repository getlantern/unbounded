package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"

	"github.com/armon/go-socks5"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/egress"
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

	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@ DANGER                                                @")
	common.Debugf("@                                                       @")
	common.Debugf("@ This standalone egress server does not use secure TLS @")
	common.Debugf("@ at the QUIC layer!                                    @")
	common.Debugf("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")

	// And here's why it doesn't use secure TLS at the QUIC layer
	tlsConfig := &tls.Config{
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	}

	ll, err := egress.NewListener(ctx, l, tlsConfig, false)
	if err != nil {
		panic(err)
	}
	defer ll.Close()

	conf := &socks5.Config{}
	proxy, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}

	common.Debugf("Starting SOCKS5 proxy...")

	err = proxy.Serve(ll)
	if err != nil {
		panic(err)
	}
}
