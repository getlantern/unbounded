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

func TestAttemptOutcome_CarriesOfferID(t *testing.T) {
	var last attemptOutcome
	last.begin("offer-1")
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
