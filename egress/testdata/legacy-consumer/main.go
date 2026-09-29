package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/quic-go/quic-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: legacy-consumer cert.pem key.pem")
	}
	cert, err := tls.LoadX509KeyPair(os.Args[1], os.Args[2])
	if err != nil {
		return err
	}
	listener, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"broflake"}}, &quic.Config{
		MaxIncomingStreams: 2 << 16, MaxIncomingUniStreams: 2 << 16,
		MaxIdleTimeout: time.Minute, KeepAlivePeriod: 15 * time.Second,
	})
	if err != nil {
		return err
	}
	defer listener.Close()
	var version string
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/quic-go/quic-go" && dep.Replace != nil {
				version = dep.Replace.Path + "@" + dep.Replace.Version
			}
		}
	}
	if version == "" {
		return fmt.Errorf("legacy QUIC dependency missing from build info")
	}
	out := json.NewEncoder(os.Stdout)
	if err := out.Encode(map[string]string{"addr": listener.Addr().String(), "version": version}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := listener.Accept(ctx)
	if err != nil {
		return err
	}
	defer conn.CloseWithError(0, "test complete")
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	if _, err := stream.Write([]byte("ready")); err != nil {
		return err
	}
	in := json.NewDecoder(os.Stdin)
	for {
		var command struct {
			Direction string
			Round     int
		}
		if err := in.Decode(&command); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := stream.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			return err
		}
		payload := bytes.Repeat([]byte(fmt.Sprintf("mixed-version transfer %d\n", command.Round)), 65536)
		switch command.Direction {
		case "upload":
			if _, err := stream.Write(payload); err != nil {
				return err
			}
		case "download":
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(stream, got); err != nil {
				return err
			}
			if !bytes.Equal(got, payload) {
				return fmt.Errorf("download %d corrupted", command.Round)
			}
		default:
			return fmt.Errorf("unknown direction %q", command.Direction)
		}
		if err := out.Encode(map[string]int{"round": command.Round}); err != nil {
			return err
		}
	}
}
