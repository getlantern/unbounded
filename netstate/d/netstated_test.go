package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	netstatecl "github.com/getlantern/broflake/netstate/client"
)

func TestHandleExec_RejectsNonPost(t *testing.T) {
	rec := httptest.NewRecorder()
	handleExec(rec, httptest.NewRequest(http.MethodGet, "/exec", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /exec: status %d, want 405", rec.Code)
	}
}

// A consumer entry with fewer than three fields is skipped rather than indexed out of range.
func TestHandleExec_SkipsShortConsumerArgs(t *testing.T) {
	world = *newMultigraph()
	stream = newStreamBroadcaster()

	body, err := json.Marshal(netstatecl.Instruction{
		Op:   netstatecl.OpConsumerState,
		Args: []string{"203.0.113.9", "203.0.113.10,tag,0"},
		ID:   "reporter",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewReader(body))
	req.RemoteAddr = "198.51.100.1:1234"
	rec := httptest.NewRecorder()
	handleExec(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST /exec: status %d, want 200", rec.Code)
	}
}

func TestHandleExec_RejectsOversizedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewReader(make([]byte, maxExecBodyBytes+1)))
	rec := httptest.NewRecorder()
	handleExec(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized POST /exec: status %d, want 400", rec.Code)
	}
}
