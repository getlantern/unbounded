package freddie

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pion/webrtc/v4"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/getlantern/broflake/common"
)

func sdpJSON(t *testing.T, lines ...string) string {
	t.Helper()
	sdp := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\n"
	for _, l := range lines {
		sdp += l + "\r\n"
	}
	b, err := json.Marshal(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClassifyNAT_VolunteerSymmetricWithHiddenBase(t *testing.T) {
	// Chrome reports srflx with raddr 0.0.0.0 rport 0; two STUN servers mapped the one socket to
	// different public ports.
	data := sdpJSON(t,
		"a=candidate:1 1 udp 2122260223 abc.local 51000 typ host generation 0",
		"a=candidate:2 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 0.0.0.0 rport 0 generation 0",
		"a=candidate:3 1 udp 1686052607 203.0.113.7 40002 typ srflx raddr 0.0.0.0 rport 0 generation 0",
	)
	cands, ok := sdpCandidates(data)
	if !ok || len(cands) != 3 {
		t.Fatalf("want 3 candidates, got %d (ok=%v)", len(cands), ok)
	}
	got := classifyNAT(cands)
	want := natClass{nat: natSymmetric, baseHidden: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestClassifyNAT_VolunteerSingleMapping(t *testing.T) {
	data := sdpJSON(t,
		"a=candidate:2 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 0.0.0.0 rport 0 generation 0",
	)
	cands, _ := sdpCandidates(data)
	if got := classifyNAT(cands).nat; got != natUndetermined {
		t.Fatalf("got %q, want %q", got, natUndetermined)
	}
}

func TestClassifyNAT_HostOnly(t *testing.T) {
	data := sdpJSON(t, "a=candidate:1 1 udp 2122260223 192.168.1.5 51000 typ host generation 0")
	cands, _ := sdpCandidates(data)
	if got := classifyNAT(cands); got != (natClass{nat: natNoSrflx}) {
		t.Fatalf("got %+v, want no_srflx", got)
	}
}

func TestClassifyNAT_TCPSrflxIgnored(t *testing.T) {
	cands := []iceCandidate{
		{typ: "srflx", protocol: "tcp", address: "203.0.113.7", port: 1, relAddr: "10.0.0.2", relPort: 9},
	}
	if got := classifyNAT(cands).nat; got != natNoSrflx {
		t.Fatalf("got %q, want %q", got, natNoSrflx)
	}
}

func TestClassifyNAT_ConsumerPortPreserving(t *testing.T) {
	// pion opens one socket per STUN server, so each srflx has its own known base.
	msg := common.ICEMsg{Candidates: []webrtc.ICECandidate{
		{Typ: webrtc.ICECandidateTypeHost, Protocol: webrtc.ICEProtocolUDP, Address: "192.168.4.78", Port: 61621},
		{Typ: webrtc.ICECandidateTypeSrflx, Protocol: webrtc.ICEProtocolUDP, Address: "97.118.36.142", Port: 61621, RelatedAddress: "0.0.0.0", RelatedPort: 61621},
		{Typ: webrtc.ICECandidateTypeSrflx, Protocol: webrtc.ICEProtocolUDP, Address: "97.118.36.142", Port: 55077, RelatedAddress: "0.0.0.0", RelatedPort: 55077},
	}}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := iceMsgCandidates(string(b))
	if !ok || len(cands) != 3 {
		t.Fatalf("want 3 candidates, got %d (ok=%v)", len(cands), ok)
	}
	got := classifyNAT(cands)
	want := natClass{nat: natUndetermined, portPreserved: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestClassifyNAT_ConsumerPortTranslatedAndSymmetric(t *testing.T) {
	cands := []iceCandidate{
		{typ: "srflx", protocol: "udp", address: "198.51.100.9", port: 30001, relAddr: "0.0.0.0", relPort: 50000},
		{typ: "srflx", protocol: "udp", address: "198.51.100.9", port: 30002, relAddr: "0.0.0.0", relPort: 50000},
	}
	got := classifyNAT(cands)
	want := natClass{nat: natSymmetric}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRecordICENAT_SkipsPayloadsWithoutCandidates(t *testing.T) {
	f, err := New(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	f.iceNAT, err = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Int64Counter("freddie.ice.nat")
	if err != nil {
		t.Fatal(err)
	}
	_, span := noop.NewTracerProvider().Tracer("test").Start(context.Background(), "test")
	delivered := attribute.Bool("reply.delivered", true)

	f.recordICENAT(context.Background(), span, common.SignalMsgICE, "{}", delivered)
	f.recordICENAT(context.Background(), span, common.SignalMsgAnswer, "{}", delivered)
	f.recordICENAT(context.Background(), span, common.SignalMsgAnswer, sdpJSON(t), delivered)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		if len(sm.Metrics) > 0 {
			t.Fatalf("payloads without candidates must not be counted, got %+v", sm.Metrics)
		}
	}
}

func TestSDPCandidates_RejectsNonJSON(t *testing.T) {
	if _, ok := sdpCandidates("not json"); ok {
		t.Fatal("want ok=false for a payload that isn't a session description")
	}
}
