package egress

import (
	"net/http"
	"net/url"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const attrURLFull = "url.full"

// withURLFull records url.full on the otelhttp server span for each request.
//
// otelhttp follows the HTTP server conventions and records the URL in parts
// (server.address, url.scheme, url.path), never url.full. SigNoz derives its
// http_url field, which its trace search filters on, only from url.full or
// http.url, so without this a search for the egress's own URL matches nothing
// even though every /ws span carries the host.
func withURLFull(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oteltrace.SpanFromContext(r.Context()).SetAttributes(
			attribute.String(attrURLFull, requestURLFull(r)))
		next.ServeHTTP(w, r)
	})
}

// requestURLFull reconstructs the URL the donor requested.
//
// The scheme comes from X-Forwarded-Proto: Caddy terminates TLS and proxies
// to the egress over plain HTTP, so r.TLS is always nil here. Only "http" and
// "https" are accepted from the header, since it is peer-influenced. The query
// string is dropped so nothing beyond the route reaches telemetry; the host is
// the same Host header otelhttp already records as server.address.
func requestURLFull(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return (&url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path}).String()
}
