package freddie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/getlantern/broflake/common"
)

func TestOfferConsumerCache_TimesRelayedPhases(t *testing.T) {
	now := time.Unix(0, 0)
	c := newOfferConsumerCache(func() time.Time { return now })
	c.add("offer", offerConsumer{genesisID: "genesis"})

	now = now.Add(2 * time.Second)
	took, genesisID, ok := c.answered("offer", "answer", "", "")
	if !ok || took != 2*time.Second || genesisID != "genesis" {
		t.Fatalf("answer: got %v, %q, %v, want 2s, genesis, true", took, genesisID, ok)
	}
	if _, _, ok := c.answered("offer", "answer2", "", ""); ok {
		t.Fatal("only the first answer, the one the consumer received, is timed")
	}

	// The consumer's ICE message is timed whether or not its candidates gave a NAT label.
	now = now.Add(3 * time.Second)
	took, genesisID, ok = c.consumerICE("answer", "", "")
	if !ok || took != 3*time.Second || genesisID != "genesis" {
		t.Fatalf("consumer ICE: got %v, %q, %v, want 3s, genesis, true", took, genesisID, ok)
	}
	if _, _, ok := c.consumerICE("answer", "port_preserved", ""); ok {
		t.Fatal("only the consumer's first ICE message is timed")
	}
	if got := c.take("offer").consumerNAT; got != "port_preserved" {
		t.Fatalf("a later ICE message's NAT label is still recorded, got %q", got)
	}

	if _, _, ok := c.answered("unknown", "a", "", ""); ok {
		t.Fatal("an answer to an unknown offer is not timed")
	}
	if _, _, ok := c.consumerICE("unknown", "", ""); ok {
		t.Fatal("ICE for an unknown answer is not timed")
	}
}

// phaseCounts collects reader's handshake histogram, counting samples by phase.
func phaseCounts(t *testing.T, reader sdkmetric.Reader) map[string]uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				v, _ := dp.Attributes.Value("phase")
				counts[v.AsString()] += dp.Count
			}
		}
	}
	return counts
}

// An answer is timed only once it reaches the consumer: one that arrives after the consumer's
// offer request has ended still finds the offer cached, but completes no handshake.
func TestHandshakePhase_RecordedOnlyWhenDelivered(t *testing.T) {
	s := startTestServer(t, nil)
	reader := sdkmetric.NewManualReader()
	var err error
	s.f.handshakeTimes, err = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Float64Histogram("freddie.handshake.duration")
	if err != nil {
		t.Fatal(err)
	}

	s.f.offerConsumers.add("gone-offer", offerConsumer{genesisID: "g1"})
	if status, _ := s.post("gone-offer", common.SignalMsgAnswer, "{}", nil); status != http.StatusNotFound {
		t.Fatalf("an answer to an ended offer request: status %d, want 404", status)
	}
	if got := phaseCounts(t, reader); len(got) != 0 {
		t.Fatalf("an undelivered answer must not be timed, got %v", got)
	}

	s.f.offerConsumers.add("live-offer", offerConsumer{genesisID: "g2"})
	consumer := signalTable.Add("live-offer")
	defer signalTable.Delete("live-offer")
	go s.post("live-offer", common.SignalMsgAnswer, "{}", nil)
	select {
	case <-consumer:
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the consumer")
	}
	deadline := time.Now().Add(5 * time.Second)
	for phaseCounts(t, reader)[handshakePhaseAnswer] != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("a delivered answer must be timed once, got %v", phaseCounts(t, reader))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// attemptDurations records one producer report with form and returns the handshake histogram's
// data points, keyed phase/delivery/outcome, with their counts and sums.
func attemptDurations(t *testing.T, form url.Values) map[string]metricdata.HistogramDataPoint[float64] {
	t.Helper()
	f, err := New(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	f.handshakeTimes, err = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Float64Histogram("freddie.handshake.duration")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/signal", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")
	f.recordProducerOutcome(context.Background(), span, r)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	points := map[string]metricdata.HistogramDataPoint[float64]{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				var vals []string
				for _, k := range []string{"phase", "delivery", "outcome"} {
					v, _ := dp.Attributes.Value(attribute.Key(k))
					vals = append(vals, v.AsString())
				}
				points[strings.Join(vals, "/")] = dp
			}
		}
	}
	return points
}

func TestRecordProducerOutcome_RecordsAttemptDuration(t *testing.T) {
	got := attemptDurations(t, url.Values{
		common.PrevOutcomeField:   {common.OutcomeNATTimeout},
		common.PrevAttemptMsField: {"15250"},
	})
	dp, ok := got["attempt/unreported/nat_timeout"]
	if !ok || len(got) != 1 || dp.Count != 1 || dp.Sum != 15.25 {
		t.Fatalf("got %v", got)
	}
}

func TestRecordProducerOutcome_SkipsBadAttemptDuration(t *testing.T) {
	for _, ms := range []string{"", "-1", "abc", "1.5", "0100", "180001"} {
		got := attemptDurations(t, url.Values{
			common.PrevOutcomeField:   {common.OutcomeConnected},
			common.PrevAttemptMsField: {ms},
		})
		if len(got) != 0 {
			t.Fatalf("duration %q must not be recorded, got %v", ms, got)
		}
	}
}
