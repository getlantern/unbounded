package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/elazarl/goproxy"
	"golang.org/x/sync/errgroup"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/egress"
)

type customListener struct {
	// This is a placeholder for any custom listener configuration if needed in the future.
	l net.Listener
}

func (cl *customListener) Accept() (net.Conn, error) {
	common.Debug("CUSTOM listener accepting connection...")
	time.Sleep(100 * time.Millisecond) // Simulate some delay if needed
	return cl.l.Accept()
}
func (cl *customListener) Close() error {
	return cl.l.Close()
}

func (cl *customListener) Addr() net.Addr {
	return cl.l.Addr()
}

func main() {
	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8000"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("Invalid port %v: %v", portStr, err)
	}

	certFile := os.Getenv("TLS_CERT")
	keyFile := os.Getenv("TLS_KEY")

	var tlsCert string
	var tlsKey string

	// XXX: in the process of delivering the cert and key to egress.NewListener, we suboptimally
	// cast back and forth between []string and []byte... it's just a byproduct of the API
	if certFile != "" && keyFile != "" {
		cert, err := os.ReadFile(certFile)
		if err != nil {
			log.Fatalf("Failed to read certfile %v: %v", certFile, err)
		}
		tlsCert = string(cert)

		key, err := os.ReadFile(keyFile)
		if err != nil {
			log.Fatalf("Failed to read keyfile %v: %v", keyFile, err)
		}
		tlsKey = string(key)
	}

	// cancels on SIGINT or SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// for running the websocket and webtransport listeners
	g, ctx := errgroup.WithContext(ctx)

	// listen websocket on PORT, and webtransport on PORT+1
	wsAddr := fmt.Sprintf(":%v", port)
	ll, err := net.Listen("tcp", wsAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %v: %v", wsAddr, err)
	}
	lll := &customListener{l: ll}

	l, err := egress.NewWebSocketWebTransportListener(ctx, lll, tlsCert, tlsKey)
	if err != nil {
		log.Fatalf("Failed to start websocket listener: %v", err)
	}
	defer l.Close()

	// Instantiate our local HTTP CONNECT proxy
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	common.Debugf("Starting HTTP CONNECT proxy...")

	proxy.OnRequest().DoFunc(
		func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
			common.Debug("HTTP proxy just saw a request:")
			// TODO: overriding the context is a hack to prevent "context canceled" errors when proxying
			// HTTP (not HTTPS) requests. It's not yet clear why this is necessary -- it may be a quirk
			// of elazarl/goproxy. See: https://github.com/getlantern/broflake/issues/47
			r = r.WithContext(context.Background())
			common.Debug(r)
			return r, nil
		},
	)

	proxy.OnResponse().DoFunc(
		func(r *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
			// TODO: log something interesting?
			return r
		},
	)

	// start a server to serve
	server := &http.Server{Handler: proxy}
	g.Go(func() error {
		if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("Egress server error: %w", err)
		}
		return nil
	})

	// handle graceful shutdown
	g.Go(func() error {
		<-ctx.Done()
		common.Debug("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, net.ErrClosed) {
			common.Debugf("Error shutting down Egress server: %v", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		log.Fatalf("Egress server exited with error: %v", err)
	}
	common.Debug("Egress server exited.")
}
