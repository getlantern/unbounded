package clientcore

import (
	"net/url"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

func TestAttemptOutcome_ReportedOnceThenCleared(t *testing.T) {
	var last attemptOutcome
	last.record(common.OutcomeNATTimeout, nil)

	form := url.Values{}
	last.addTo(form, 15*time.Second)
	if got := form.Get(common.PrevOutcomeField); got != common.OutcomeNATTimeout {
		t.Fatalf("first genesis should carry the outcome, got %q", got)
	}
	if got := form.Get(common.PrevNATTimeoutField); got != "15" {
		t.Fatalf("the report should carry the timeout in seconds, got %q", got)
	}

	next := url.Values{}
	last.addTo(next, 15*time.Second)
	if len(next) != 0 {
		t.Fatalf("an outcome must be reported once, but the next genesis carried %v", next)
	}
}

func TestAttemptOutcome_NothingRecordedAddsNothing(t *testing.T) {
	var last attemptOutcome
	form := url.Values{"data": {"x"}}
	last.addTo(form, 15*time.Second)
	if len(form) != 1 {
		t.Fatalf("no outcome must leave the genesis form untouched, got %v", form)
	}
}

func TestAttemptOutcome_ReportsDurationOfBegunAttempt(t *testing.T) {
	var last attemptOutcome
	last.begin("offer-1", "")
	last.record(common.OutcomeNATTimeout, nil)
	form := url.Values{}
	last.addTo(form, 15*time.Second)
	if _, ok := common.ParseAttemptDuration(form.Get(common.PrevAttemptMsField)); !ok {
		t.Fatalf("a begun attempt should report its duration, got %q", form.Get(common.PrevAttemptMsField))
	}

	// An attempt that never took an offer has nothing to time.
	last.record(common.OutcomeNATTimeout, nil)
	next := url.Values{}
	last.addTo(next, 15*time.Second)
	if _, ok := next[common.PrevAttemptMsField]; ok {
		t.Fatalf("an attempt with no offer must not carry a duration, got %v", next)
	}
}

func TestAttemptOutcome_CarriesOfferID(t *testing.T) {
	var last attemptOutcome
	last.begin("offer-1", "")
	last.record(common.OutcomeConnected, nil)

	form := url.Values{}
	last.addTo(form, 15*time.Second)
	if got := form.Get(common.PrevOfferIDField); got != "offer-1" {
		t.Fatalf("the report should carry the answered offer's ID, got %q", got)
	}

	// The next attempt's report must not inherit it.
	last.record(common.OutcomeNATTimeout, nil)
	next := url.Values{}
	last.addTo(next, 15*time.Second)
	if _, ok := next[common.PrevOfferIDField]; ok {
		t.Fatalf("an attempt with no offer must not carry an offer ID, got %v", next)
	}
}

func TestAttemptOutcome_ReportsPath(t *testing.T) {
	var o attemptOutcome
	o.begin("offer", "")
	o.record(common.OutcomeConnected, nil)
	o.path = common.PathRelay
	form := url.Values{}
	o.addTo(form, 5*time.Second)
	if got := form.Get(common.PrevPathField); got != common.PathRelay {
		t.Fatalf("prev-path = %q, want relay", got)
	}

	o.begin("offer2", "")
	o.record(common.OutcomeNATTimeout, nil)
	form = url.Values{}
	o.addTo(form, 5*time.Second)
	if _, ok := form[common.PrevPathField]; ok {
		t.Fatal("an attempt with no path must not send prev-path")
	}
}

// The VPN state an attempt began under rides with its outcome, once, and an attempt begun without
// one reports none.
func TestAttemptOutcome_CarriesVPNState(t *testing.T) {
	var last attemptOutcome
	last.begin("offer-1", "connected")
	last.record(common.OutcomeNATTimeout, nil)

	form := url.Values{}
	last.addTo(form, 15*time.Second)
	if got := form.Get(common.PrevVPNField); got != "connected" {
		t.Fatalf("the report should carry the VPN state the attempt began under, got %q", got)
	}

	last.begin("offer-2", "")
	last.record(common.OutcomeConnected, nil)
	next := url.Values{}
	last.addTo(next, 15*time.Second)
	if _, ok := next[common.PrevVPNField]; ok {
		t.Fatalf("an attempt begun with no VPN state must not report one, got %v", next)
	}
}
