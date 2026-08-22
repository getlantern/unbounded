package egress

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// recordingHandler captures what a leg of the tee actually received.
type recordingHandler struct {
	level   slog.Level
	records *[]slog.Record
	attrs   []slog.Attr
	groups  []string
	// seenGroups captures the group chain in force when a record arrived, so a
	// WithGroup that fails to reach a leg is observable rather than merely
	// assumed.
	seenGroups *[][]string
}

func (h recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	for _, a := range h.attrs {
		r.AddAttrs(a)
	}
	*h.records = append(*h.records, r)
	if h.seenGroups != nil {
		*h.seenGroups = append(*h.seenGroups, append([]string{}, h.groups...))
	}
	return nil
}

func (h recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return h
}

func (h recordingHandler) WithGroup(name string) slog.Handler {
	h.groups = append(append([]string{}, h.groups...), name)
	return h
}

func newTee(t *testing.T) (*teeHandler, *[]slog.Record, *[]slog.Record) {
	t.Helper()
	localRecs, remoteRecs := &[]slog.Record{}, &[]slog.Record{}
	return &teeHandler{
		local:  recordingHandler{level: slog.LevelDebug, records: localRecs},
		remote: recordingHandler{level: slog.LevelDebug, records: remoteRecs},
	}, localRecs, remoteRecs
}

func msgs(recs *[]slog.Record) []string {
	out := make([]string, 0, len(*recs))
	for _, r := range *recs {
		out = append(out, r.Message)
	}
	return out
}

// The cost control this whole change rests on. The stderr handler runs at Debug
// and logs per connection, per QUIC stream and per keepalive ping, so a tee
// that exported Debug would ship millions of lines a day. Everything below
// otelLogLevel must stay on the box.
func TestTeeHandler_DoesNotExportBelowInfo(t *testing.T) {
	h, local, remote := newTee(t)
	log := slog.New(h)

	log.Debug("PING")
	log.Info("Refused WebSocket connection")
	log.Warn("Metrics shutdown failed")
	log.Error("boom")

	if got, want := len(*local), 4; got != want {
		t.Fatalf("local received %d records, want %d: %v", got, want, msgs(local))
	}
	if got := msgs(remote); len(got) != 3 {
		t.Fatalf("remote received %v, want the 3 records at Info and above", got)
	}
	for _, m := range msgs(remote) {
		if m == "PING" {
			t.Error("a Debug record reached the exporter; per-ping volume would be shipped")
		}
	}
}

// Enabled must be true whenever *either* leg wants the record, or slog skips
// building it and the local journal silently loses its Debug lines.
func TestTeeHandler_EnabledCoversTheLocalLegToo(t *testing.T) {
	h := &teeHandler{
		local:  recordingHandler{level: slog.LevelDebug, records: &[]slog.Record{}},
		remote: recordingHandler{level: slog.LevelDebug, records: &[]slog.Record{}},
	}
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Debug reported disabled; the stderr leg would stop receiving it")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error reported disabled")
	}
}

// A remote leg that declines the level must not stop the local one, and vice
// versa — the two are independently gated.
func TestTeeHandler_LegsAreIndependentlyGated(t *testing.T) {
	localRecs, remoteRecs := &[]slog.Record{}, &[]slog.Record{}
	h := &teeHandler{
		local:  recordingHandler{level: slog.LevelDebug, records: localRecs},
		remote: recordingHandler{level: slog.LevelError, records: remoteRecs},
	}
	log := slog.New(h)
	log.Info("info line")
	log.Error("error line")

	if got := msgs(localRecs); len(got) != 2 {
		t.Errorf("local got %v, want both", got)
	}
	if got := msgs(remoteRecs); len(got) != 1 || got[0] != "error line" {
		t.Errorf("remote got %v, want only the error line", got)
	}
}

// WithAttrs has to reach both legs, or the exported copy would arrive without
// the peer attributes that make it worth exporting: donor_country, user_agent,
// page_url. It does reach both, and this pins that — the phrasing below is a
// statement about what would break, not about current behaviour.
func TestTeeHandler_WithAttrsReachesBothLegs(t *testing.T) {
	h, local, remote := newTee(t)
	log := slog.New(h).With("csid", "abc123")
	log.Info("Refused WebSocket connection")

	for name, recs := range map[string]*[]slog.Record{"local": local, "remote": remote} {
		if len(*recs) != 1 {
			t.Fatalf("%s got %d records, want 1", name, len(*recs))
		}
		var found bool
		(*recs)[0].Attrs(func(a slog.Attr) bool {
			if a.Key == "csid" && a.Value.String() == "abc123" {
				found = true
			}
			return true
		})
		if !found {
			t.Errorf("%s leg lost the csid attr", name)
		}
	}

}

// WithGroup has to reach both legs too, and "both legs got a record" does not
// show that — a WithGroup that returned the unwrapped remote handler would pass
// such a check. Assert the group chain each leg actually saw.
func TestTeeHandler_WithGroupReachesBothLegs(t *testing.T) {
	localGroups, remoteGroups := &[][]string{}, &[][]string{}
	h := &teeHandler{
		local:  recordingHandler{level: slog.LevelDebug, records: &[]slog.Record{}, seenGroups: localGroups},
		remote: recordingHandler{level: slog.LevelDebug, records: &[]slog.Record{}, seenGroups: remoteGroups},
	}

	slog.New(h).WithGroup("peer").WithGroup("tls").Info("grouped")

	want := []string{"peer", "tls"}
	for name, seen := range map[string]*[][]string{"local": localGroups, "remote": remoteGroups} {
		if len(*seen) != 1 {
			t.Fatalf("%s leg recorded %d records, want 1", name, len(*seen))
		}
		got := (*seen)[0]
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s leg saw groups %v, want %v", name, got, want)
		}
	}
}

// Both legs must receive every attr, including the spilled ones. slog.Record
// inlines its first 5 attrs and puts the rest in a backing slice, so a record
// with more than 5 is the case where a naive fanout could deliver a truncated
// copy to the second leg.
//
// Note on Clone: Handle passes the remote leg r.Clone() because the slog
// Handler contract requires it of anything that may retain or mutate the
// record, and the local leg has already been handed this one. That is contract
// compliance rather than a property this test can demonstrate — whether the
// aliasing is observable depends on unspecified slice-growth behaviour, so a
// test asserting it passes with or without the Clone. Kept deliberately, not
// asserted dishonestly.
func TestTeeHandler_BothLegsGetEveryAttrIncludingSpilled(t *testing.T) {
	h, local, remote := newTee(t)

	slog.New(h).Info("refused",
		slog.String("a", "1"), slog.String("b", "2"), slog.String("c", "3"),
		slog.String("d", "4"), slog.String("e", "5"), slog.String("f", "6"),
		slog.String("g", "7"))

	for name, recs := range map[string]*[]slog.Record{"local": local, "remote": remote} {
		if len(*recs) != 1 {
			t.Fatalf("%s got %d records, want 1", name, len(*recs))
		}
		got := map[string]string{}
		(*recs)[0].Attrs(func(a slog.Attr) bool {
			got[a.Key] = a.Value.String()
			return true
		})
		for _, k := range []string{"a", "b", "c", "d", "e", "f", "g"} {
			if got[k] == "" {
				t.Errorf("%s leg is missing attr %q (got %d of 7)", name, k, len(got))
			}
		}
	}
}

// Sanity check against a real handler rather than a fake: the local leg must
// still produce ordinary text output on stderr-shaped writers.
func TestTeeHandler_LocalLegStillFormatsNormally(t *testing.T) {
	var buf bytes.Buffer
	h := &teeHandler{
		local:  slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		remote: recordingHandler{level: slog.LevelDebug, records: &[]slog.Record{}},
	}
	slog.New(h).Debug("PING", "total", 7)

	if out := buf.String(); !strings.Contains(out, "PING") || !strings.Contains(out, "total=7") {
		t.Errorf("local text output looks wrong: %q", out)
	}
}

// The guard that keeps an unconfigured egress from building an exporter it can
// never reach. Without it the SDK targets the default localhost:4318, queues
// every Info record, and blocks on shutdown flushing to nothing — which hung
// NewListener's own shutdown test for the full 600s timeout and would do the
// same to a production egress on a host with no collector.
func TestOTLPLogsEndpoint_RequiresALogsEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, generic, logs string
		want                bool
	}{
		{name: "neither set", want: false},
		{name: "generic endpoint", generic: "http://localhost:4318", want: true},
		{name: "logs-specific endpoint", logs: "http://localhost:4318/v1/logs", want: true},
		{name: "both set", generic: "http://a", logs: "http://b", want: true},
		{name: "empty strings are not configuration", generic: "", logs: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.generic)
			t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", tc.logs)
			name, _ := otlpLogsEndpoint()
			if got := name != ""; got != tc.want {
				t.Errorf("otlpLogsEndpoint() name = %q (configured=%v), want configured=%v", name, got, tc.want)
			}
		})
	}
}

// With no endpoint configured, enabling must be inert: no exporter, and the
// binary's own stderr handler left exactly as it was.
func TestEnableOTELLogs_NoEndpointLeavesLoggingUntouched(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")

	before := slog.Default()
	shutdown := enableOTELLogs(context.Background())
	t.Cleanup(func() { slog.SetDefault(before) })

	if slog.Default() != before {
		t.Error("the default logger was replaced despite no configured collector")
	}
	// Must return promptly rather than blocking on a flush that cannot happen.
	done := make(chan error, 1)
	go func() { done <- shutdown(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked with no exporter configured")
	}
}

// Whether export is on must be visible on stderr. Otherwise the only evidence
// is the absence of logs in the collector, which looks identical to a healthy
// egress that had nothing to say — so a misconfigured deploy is unfalsifiable.
func TestEnableOTELLogs_SaysWhyItIsDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_ = enableOTELLogs(context.Background())

	out := buf.String()
	if !strings.Contains(out, "Log export disabled") {
		t.Errorf("no line explaining that export is off: %q", out)
	}
	// Naming the variables it looked at is the actionable part — otherwise the
	// operator knows it is off but not what to set.
	for _, v := range otlpLogsEndpointVars {
		if !strings.Contains(out, v) {
			t.Errorf("the disabled line does not name %s, so it is not actionable: %q", v, out)
		}
	}
}

// A metrics-only collector configuration must not be mistaken for a logs
// endpoint. otlploghttp would fall back to localhost:4318 and queue records for
// something that is not listening.
func TestOTLPLogsEndpoint_IgnoresTheMetricsEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://collector:4318/v1/metrics")

	if name, _ := otlpLogsEndpoint(); name != "" {
		t.Errorf("treated %s as a logs endpoint", name)
	}
}

// And when one is configured, the enabled line has to say so, with the source
// variable — that is what makes a deploy verifiable from the journal.
func TestOTLPLogsEndpoint_ReportsWhichVariableSuppliedIt(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://shared:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	if name, val := otlpLogsEndpoint(); name != "OTEL_EXPORTER_OTLP_ENDPOINT" || val != "http://shared:4318" {
		t.Errorf("got (%q, %q), want the shared variable", name, val)
	}

	// Signal-specific wins, matching OTEL's precedence.
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://logs:4318/v1/logs")
	if name, val := otlpLogsEndpoint(); name != "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT" || val != "http://logs:4318/v1/logs" {
		t.Errorf("got (%q, %q), want the logs-specific variable to win", name, val)
	}
}

// The enabled path, end to end against a real OTLP receiver so shutdown does
// not have to time out. Without this, deleting the "Log export enabled" line
// leaves the endpoint-detection tests passing while the only signal that export
// is on disappears.
func TestEnableOTELLogs_AnnouncesItselfAndRedactsTheEndpoint(t *testing.T) {
	got := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case got <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	// Credentials in the endpoint: a legal OTLP value, and the thing that must
	// not reach the journal.
	u, err := url.Parse(collector.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("collector", "s3cr3t")
	u.Path = "/v1/logs"
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", u.String())
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	var stderr bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	shutdown := enableOTELLogs(context.Background())

	out := stderr.String()
	if !strings.Contains(out, "Log export enabled") {
		t.Errorf("nothing announced that export is on: %q", out)
	}
	if !strings.Contains(out, "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") {
		t.Errorf("the enabled line does not say which variable supplied the endpoint: %q", out)
	}
	for _, secret := range []string{"s3cr3t", "collector:s3cr3t"} {
		if strings.Contains(out, secret) {
			t.Errorf("the endpoint's credentials reached the log: %q", out)
		}
	}
	// The useful part survives redaction.
	if !strings.Contains(out, "/v1/logs") {
		t.Errorf("the redacted endpoint lost its path, so the line is not diagnostic: %q", out)
	}

	// The handler really was swapped: an Info record now reaches the collector.
	slog.Info("a record that should be exported")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Error("no OTLP request reached the collector; export is announced but not wired")
	}
}
