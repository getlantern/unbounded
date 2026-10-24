package clientcore

import (
	"net/url"
	"testing"

	"github.com/getlantern/broflake/common"
)

func TestAttemptOutcome_ReportedOnceThenCleared(t *testing.T) {
	var last attemptOutcome
	last.record(common.OutcomeNATTimeout, nil)

	form := url.Values{}
	last.addTo(form)
	if got := form.Get(common.PrevOutcomeField); got != common.OutcomeNATTimeout {
		t.Fatalf("first genesis should carry the outcome, got %q", got)
	}

	next := url.Values{}
	last.addTo(next)
	if len(next) != 0 {
		t.Fatalf("an outcome must be reported once, but the next genesis carried %v", next)
	}
}

func TestAttemptOutcome_NothingRecordedAddsNothing(t *testing.T) {
	var last attemptOutcome
	form := url.Values{"data": {"x"}}
	last.addTo(form)
	if len(form) != 1 {
		t.Fatalf("no outcome must leave the genesis form untouched, got %v", form)
	}
}
