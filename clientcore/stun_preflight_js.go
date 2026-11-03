//go:build wasm

package clientcore

import (
	"context"
	"time"

	"github.com/pion/transport/v5"
)

// stunPreflight always passes in the browser, which can't open a UDP socket of its own. Browser
// volunteers rarely fail to gather a server reflexive candidate anyway.
func stunPreflight(_ context.Context, _ transport.Net, servers []string, _ time.Duration) STUNPreflightResult {
	return STUNPreflightResult{Outcome: STUNPreflightOK, Servers: len(servers), Skipped: true}
}
