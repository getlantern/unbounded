package freddie

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/getlantern/broflake/common"
)

// outcomeCounts records one producer report with form and, unless empty, VersionHeader version,
// keyed outcome/ice_state/pc_state/nat_timeout_s/consumer_country/consumer_version/volunteer_version.
func outcomeCounts(t *testing.T, form url.Values, version string) map[string]int64 {
	t.Helper()
	f, err := New(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	f.producerOutcomes, err = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Int64Counter("freddie.producer.outcomes")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/signal", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if version != "" {
		r.Header.Set(common.VersionHeader, version)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")
	f.recordProducerOutcome(context.Background(), span, r)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				o, _ := dp.Attributes.Value("outcome")
				i, _ := dp.Attributes.Value("ice_state")
				p, _ := dp.Attributes.Value("pc_state")
				n, _ := dp.Attributes.Value("nat_timeout_s")
				c, _ := dp.Attributes.Value("consumer_country")
				v, _ := dp.Attributes.Value("consumer_version")
				vv, _ := dp.Attributes.Value("volunteer_version")
				counts[o.AsString()+"/"+i.AsString()+"/"+p.AsString()+"/"+n.AsString()+"/"+c.AsString()+"/"+v.AsString()+"/"+vv.AsString()] += dp.Value
			}
		}
	}
	return counts
}

func TestRecordProducerOutcome_CountsNormalizedReport(t *testing.T) {
	got := outcomeCounts(t, url.Values{
		common.PrevOutcomeField:  {common.OutcomeNATTimeout},
		common.PrevICEStateField: {"connected"},
		common.PrevPCStateField:  {"bogus"},
	}, "")
	if got["nat_timeout/connected/invalid/unreported/unreported/unreported/not_sent"] != 1 || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestRecordProducerOutcome_LabelsTimeout(t *testing.T) {
	got := outcomeCounts(t, url.Values{
		common.PrevOutcomeField:    {common.OutcomeConnected},
		common.PrevICEStateField:   {"connected"},
		common.PrevPCStateField:    {"connected"},
		common.PrevNATTimeoutField: {"15"},
	}, common.Version)
	if got["connected/connected/connected/15/unreported/unreported/"+common.Version] != 1 || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestRecordProducerOutcome_NoReportNoCount(t *testing.T) {
	if got := outcomeCounts(t, url.Values{"data": {"{}"}}, common.Version); len(got) != 0 {
		t.Fatalf("a genesis without an outcome must not be counted, got %v", got)
	}
}

func TestRecordProducerOutcome_LabelsVolunteerVersion(t *testing.T) {
	form := url.Values{common.PrevOutcomeField: {common.OutcomeConnected}}
	for version, want := range map[string]string{
		common.Version: common.Version,
		"v1.0.0":       "invalid",
		"v2.0.0":       "older",
		"bogus":        "invalid",
		"":             "not_sent",
	} {
		got := outcomeCounts(t, form, version)
		if len(got) != 1 {
			t.Fatalf("version %q: got %v", version, got)
		}
		for k := range got {
			if !strings.HasSuffix(k, "/"+want) {
				t.Fatalf("version %q: want volunteer_version %q, got %v", version, want, got)
			}
		}
	}
}
