package freddie

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/getlantern/broflake/common"
)

// outcomeCounts records one producer report with form and, unless empty, VersionHeader version,
// keyed outcome/ice_state/pc_state/nat_timeout_s/consumer_country/consumer_version/volunteer_version.
func outcomeCounts(t *testing.T, form url.Values, version string) map[string]int64 {
	t.Helper()
	return outcomeLabels(t, form, version, "outcome", "ice_state", "pc_state", "nat_timeout_s", "consumer_country", "consumer_version", "volunteer_version")
}

// outcomeLabels records one producer report like outcomeCounts, keyed by the given labels.
func outcomeLabels(t *testing.T, form url.Values, version string, keys ...string) map[string]int64 {
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
	return sumByLabels(t, reader, keys...)
}

// sumByLabels collects reader's counters, summing data points keyed by the values of keys joined
// with "/".
func sumByLabels(t *testing.T, reader sdkmetric.Reader, keys ...string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				vals := make([]string, len(keys))
				for i, k := range keys {
					v, _ := dp.Attributes.Value(attribute.Key(k))
					vals[i] = v.AsString()
				}
				counts[strings.Join(vals, "/")] += dp.Value
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

// preflightCounts records one genesis with form, keyed result/vpn/volunteer_version.
func preflightCounts(t *testing.T, form url.Values) map[string]int64 {
	t.Helper()
	return preflightLabels(t, form, "result", "vpn", "volunteer_version")
}

// preflightLabels records one genesis like preflightCounts, keyed by the given labels.
func preflightLabels(t *testing.T, form url.Values, keys ...string) map[string]int64 {
	t.Helper()
	f, err := New(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	f.stunPreflights, err = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Int64Counter("freddie.producer.stun_preflights")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/signal", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set(common.VersionHeader, common.Version)
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	f.recordSTUNPreflights(context.Background(), r)
	return sumByLabels(t, reader, keys...)
}

func TestRecordSTUNPreflights_PassOnly(t *testing.T) {
	got := preflightCounts(t, url.Values{common.PreflightVPNField: {"connected"}})
	if got["ok/connected/"+common.Version] != 1 || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

// A pass reported with an empty VPN state, as from a browser or an app without a VPN, still counts.
func TestRecordSTUNPreflights_PassWithoutVPN(t *testing.T) {
	got := preflightCounts(t, url.Values{common.PreflightVPNField: {""}})
	if got["ok/unknown/"+common.Version] != 1 || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestRecordSTUNPreflights_FailuresThenPass(t *testing.T) {
	got := preflightCounts(t, url.Values{
		common.PreflightVPNField:          {"disconnected"},
		common.PrevPreflightFailuresField: {"12"},
		common.PrevPreflightOutcomeField:  {common.PreflightNoReply},
		common.PrevPreflightVPNField:      {"connected"},
	})
	want := map[string]int64{
		"ok/disconnected/" + common.Version:    1,
		"no_reply/connected/" + common.Version: 12,
	}
	if len(got) != len(want) || got["ok/disconnected/"+common.Version] != 1 || got["no_reply/connected/"+common.Version] != 12 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A report can't mint label values or inflate the count.
func TestRecordSTUNPreflights_Normalizes(t *testing.T) {
	got := preflightCounts(t, url.Values{
		common.PreflightVPNField:          {"bogus"},
		common.PrevPreflightFailuresField: {"3"},
		common.PrevPreflightOutcomeField:  {"made_up"},
		common.PrevPreflightVPNField:      {"also bogus"},
	})
	if got["ok/invalid/"+common.Version] != 1 || got["invalid/invalid/"+common.Version] != 3 || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	got = preflightCounts(t, url.Values{
		common.PreflightVPNField:          {"connected"},
		common.PrevPreflightFailuresField: {"99999999"},
		common.PrevPreflightOutcomeField:  {common.PreflightNoReply},
	})
	if got["ok/connected/"+common.Version] != 1 || len(got) != 1 {
		t.Fatalf("an out-of-range failure count must be dropped, got %v", got)
	}
}

func TestRecordSTUNPreflights_NoReportNoCount(t *testing.T) {
	if got := preflightCounts(t, url.Values{
		common.PrevPreflightFailuresField: {"5"},
		common.PrevPreflightOutcomeField:  {common.PreflightNoReply},
	}); len(got) != 0 {
		t.Fatalf("failures without the pass that reports them must not be counted, got %v", got)
	}
}

// A producer's kind and mapped flag label its outcomes, normalized so a report can't mint values,
// and a producer that predates them is counted as unreported.
func TestRecordProducerOutcome_LabelsVolunteerKind(t *testing.T) {
	for _, tc := range []struct {
		kind, mapped []string
		want         string
	}{
		{[]string{common.VolunteerKindServer}, []string{"1"}, "server/true"},
		{[]string{common.VolunteerKindBrowser}, []string{"0"}, "browser/false"},
		{nil, nil, "unreported/unreported"},
		{[]string{"toaster"}, []string{"yes"}, "invalid/invalid"},
	} {
		form := url.Values{common.PrevOutcomeField: {common.OutcomeConnected}}
		if tc.kind != nil {
			form[common.VolunteerKindField] = tc.kind
			form[common.VolunteerMappedField] = tc.mapped
		}
		got := outcomeLabels(t, form, common.Version, "volunteer_kind", "volunteer_mapped")
		if got[tc.want] != 1 || len(got) != 1 {
			t.Errorf("kind %v mapped %v: want %s, got %v", tc.kind, tc.mapped, tc.want, got)
		}
	}
}

// Both the pass and the failures it reports carry the producer's kind.
func TestRecordSTUNPreflights_LabelsVolunteerKind(t *testing.T) {
	got := preflightLabels(t, url.Values{
		common.PreflightVPNField:          {"disconnected"},
		common.PrevPreflightFailuresField: {"4"},
		common.PrevPreflightOutcomeField:  {common.PreflightNoReply},
		common.VolunteerKindField:         {common.VolunteerKindApp},
		common.VolunteerMappedField:       {"0"},
	}, "result", "volunteer_kind", "volunteer_mapped")
	if got["ok/app/false"] != 1 || got["no_reply/app/false"] != 4 || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
