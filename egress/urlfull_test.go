package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestWithURLFull_RecordsURLOnServerSpan(t *testing.T) {
	for _, tc := range []struct {
		name, target, proto, want string
	}{
		{"behind Caddy", "/ws", "https", "https://unbounded.iantem.io/ws"},
		{"no proxy header", "/ws", "", "http://unbounded.iantem.io/ws"},
		{"unexpected proto ignored", "/ws", "javascript", "http://unbounded.iantem.io/ws"},
		{"query string dropped", "/ws?token=secret", "https", "https://unbounded.iantem.io/ws"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSampler(sdktrace.AlwaysSample()),
				sdktrace.WithSyncer(exporter),
			)
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			h := otelhttp.NewHandler(withURLFull(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
				"/ws", otelhttp.WithTracerProvider(tp))
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.Host = "unbounded.iantem.io"
			if tc.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("exported %d spans, want 1", len(spans))
			}
			var got string
			for _, kv := range spans[0].Attributes {
				if string(kv.Key) == attrURLFull {
					got = kv.Value.AsString()
				}
			}
			if got != tc.want {
				t.Fatalf("%s = %q, want %q", attrURLFull, got, tc.want)
			}
		})
	}
}
