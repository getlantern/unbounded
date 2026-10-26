//go:build !wasm

package clientcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

func TestOfferCarriesPionDTLSVersion(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(common.PionDTLSHeader)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	runOffer(t, newConsumerWebRTC(o, nil, newRoomPreference(time.Now)), context.Background(), "producer")

	if v := <-got; v == "" || v != pionDTLSVersion() || common.NormalizePionDTLS(v) == "invalid" {
		t.Fatalf("offer sent %s %q, want %q", common.PionDTLSHeader, v, pionDTLSVersion())
	}
}

func TestDTLSVersion(t *testing.T) {
	dtls := &debug.Module{Path: "github.com/pion/dtls/v3", Version: "v3.1.15"}
	other := &debug.Module{Path: "github.com/pion/ice/v4", Version: "v4.4.8"}
	replaced := &debug.Module{Path: "github.com/pion/dtls/v3", Version: "v3.1.2", Replace: &debug.Module{Path: "github.com/getlantern/dtls", Version: "v3.1.16-0.20261020120000-abcdef012345"}}
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"linked", &debug.BuildInfo{Deps: []*debug.Module{other, dtls}}, true, "v3.1.15"},
		{"replaced", &debug.BuildInfo{Deps: []*debug.Module{replaced}}, true, "v3.1.16-0.20261020120000-abcdef012345"},
		{"not linked", &debug.BuildInfo{Deps: []*debug.Module{other}}, true, common.PionDTLSNoBuildInfo},
		{"no build info", nil, false, common.PionDTLSNoBuildInfo},
	} {
		if got := dtlsVersion(tc.info, tc.ok); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
