//go:build !wasm

package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/getlantern/broflake/clientcore"
)

func runActionModeCLI() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bfOpt := clientcore.NewDefaultBroflakeOptions()
	bfOpt.ClientType = "widget"
	bfOpt.CTableSize = 25
	bfOpt.PTableSize = 25
	if maxConnections := os.Getenv("MAX_CONNECTIONS"); maxConnections != "" {
		value, err := strconv.Atoi(maxConnections)
		if err != nil || value < 1 {
			panic("MAX_CONNECTIONS must be a positive integer")
		}
		bfOpt.CTableSize = value
		bfOpt.PTableSize = value
	}
	var activeConnections atomic.Int64
	var totalConnections atomic.Int64
	bfOpt.OnConnectionChangeFunc = func(state int, workerIdx int, addr net.IP) {
		switch state {
		case 1:
			activeConnections.Add(1)
			totalConnections.Add(1)
		case -1:
			activeConnections.Add(-1)
		default:
			return
		}
		slog.Info("Proxied connections",
			"active", activeConnections.Load(),
			"total", totalConnections.Load(),
		)
	}
	rtcOpt := clientcore.NewDefaultWebRTCOptions()
	rtcOpt.DiscoverySrv = "https://freddie.iantem.io"
	if freddie := os.Getenv("FREDDIE"); freddie != "" {
		rtcOpt.DiscoverySrv = freddie
	}
	rtcOpt.Tag = os.Getenv("TAG")
	egOpt := clientcore.NewDefaultEgressOptions()
	egOpt.Addr = "wss://unbounded.iantem.io"
	if egress := os.Getenv("EGRESS"); egress != "" {
		egOpt.Addr = egress
	}

	slog.Debug("Starting Action Mode CLI", "freddie", rtcOpt.DiscoverySrv, "egress", egOpt.Addr)
	_, _, err := clientcore.NewBroflake(bfOpt, rtcOpt, egOpt)
	if err != nil {
		panic(err)
	}

	<-ctx.Done()
	slog.Debug("Stopping Action Mode CLI")
}
