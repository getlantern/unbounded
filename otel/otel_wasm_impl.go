//go:build wasm

package otel

// SendNATBehaviorTelemetry is a noop for wasm build targets because OpenTelemetry's Go
// implementation abuses the call stack in ways that mobile Safari does not tolerate.
func SendNATBehaviorTelemetry(_ NATSummary, _ string) {}
