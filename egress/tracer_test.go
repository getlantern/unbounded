package egress

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The package-level `tracer` is created at init, before NewListener calls
// telemetry.EnableOTELTracing (which installs the real TracerProvider via
// otel.SetTracerProvider). That ordering looks like it should leave every session
// span permanently no-op, and it would if we had captured a provider directly.
//
// It doesn't, because otel's global TracerProvider hands out delegating tracers:
// SetTracerProvider walks the tracers it has already returned and repoints them
// at the new provider (otel/internal/global/trace.go, tracerProvider.setDelegate).
// A package-level tracer var is the pattern that machinery exists to support.
//
// This test pins that behaviour, because the failure mode is silent — spans would
// simply never appear, with nothing logged — and it depends on a guarantee from a
// dependency rather than on our own code.
func TestPackageLevelTracerRecordsAfterProviderInstalledLater(t *testing.T) {
	// Sanity check the premise: before any provider is installed, the global
	// no-op provider produces non-recording spans.
	_, preSpan := tracer.Start(context.Background(), "before-provider")
	preRecording := preSpan.IsRecording()
	preSpan.End()

	// Now do what NewListener does, after `tracer` was already created at init.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSyncer(exporter),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	_, span := tracer.Start(context.Background(), spanWebSocketSession)
	if !span.IsRecording() {
		t.Fatal("package-level tracer produced a non-recording span after a provider was installed; " +
			"global tracer delegation is not working and session spans would be silently dropped")
	}
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	if spans[0].Name != spanWebSocketSession {
		t.Errorf("span name = %q, want %q", spans[0].Name, spanWebSocketSession)
	}

	t.Logf("pre-provider span recording=%v (expected false), post-provider span exported OK", preRecording)
}
