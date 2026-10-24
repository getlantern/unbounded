//go:build !wasm

package clientcore

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestApplyICETiming_ZeroKeepsPionDefaults(t *testing.T) {
	var se webrtc.SettingEngine
	if applyICETiming(&se, &WebRTCOptions{}) {
		t.Fatal("zero options must not touch the setting engine")
	}
	if applyICETiming(&se, nil) {
		t.Fatal("nil options must not touch the setting engine")
	}
}

func TestApplyICETiming_SetsEachKnob(t *testing.T) {
	for _, opts := range []*WebRTCOptions{
		{ICEMaxBindingRequests: 50},
		{STUNGatherTimeout: 1500 * time.Millisecond},
	} {
		var se webrtc.SettingEngine
		if !applyICETiming(&se, opts) {
			t.Fatalf("%+v should have been applied", opts)
		}
	}
}

func TestNewProducerPeerConnection_WithAndWithoutICETiming(t *testing.T) {
	for _, opts := range []*WebRTCOptions{
		{},
		{ICEMaxBindingRequests: 50, STUNGatherTimeout: time.Second},
	} {
		pc, err := newProducerPeerConnection(webrtc.Configuration{}, opts)
		if err != nil {
			t.Fatalf("%+v: %v", opts, err)
		}
		if err := pc.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
