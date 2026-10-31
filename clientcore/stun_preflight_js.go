//go:build wasm

package clientcore

import (
	"context"
	"time"
)

// stunPreflight always passes in the browser, which can't open a UDP socket of its own. Browser
// volunteers rarely fail to gather a server reflexive candidate anyway.
func stunPreflight(_ context.Context, servers []string, _ time.Duration) STUNPreflightResult {
	return STUNPreflightResult{Outcome: STUNPreflightOK, Servers: len(servers)}
}
