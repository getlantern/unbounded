package egress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageRetryAfterRestart(t *testing.T) {
	var batches [][]usageEvent
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer server-secret", r.Header.Get("Authorization"))
		var events []usageEvent
		require.NoError(t, json.NewDecoder(r.Body).Decode(&events))
		batches = append(batches, events)
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	r := &usageReporter{pending: make(map[usageKey]int64), endpoint: server.URL, credential: "server-secret", salt: "donor-secret", dir: t.TempDir(), client: server.Client()}
	req := httptest.NewRequest("GET", "http://egress/ws?donor_id="+uuid.NewString(), nil)
	req.Header.Set("Origin", "https://example.org")
	count := r.counter(req)
	require.NotNil(t, count)
	count(123)
	count(456)
	require.NoError(t, r.persist())
	require.Empty(t, r.pending)
	require.Error(t, r.send(context.Background()))
	require.Len(t, batches, 1)
	require.EqualValues(t, 579, batches[0][0].Bytes)
	restarted := &usageReporter{endpoint: server.URL, credential: r.credential, dir: r.dir, client: server.Client()}
	fail = false
	require.NoError(t, restarted.send(context.Background()))
	require.Len(t, batches, 2)
	require.Equal(t, batches[0], batches[1])
	files, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestUsageConsentAndOriginIsolation(t *testing.T) {
	r := &usageReporter{pending: make(map[usageKey]int64), salt: "secret"}
	id := uuid.NewString()
	req := httptest.NewRequest("GET", "http://egress/ws?donor_id="+id, nil)
	require.Nil(t, r.counter(req))
	for _, origin := range []string{"https://one.example", "https://two.example"} {
		req.Header.Set("Origin", origin)
		r.counter(req)(10)
	}
	require.Len(t, r.pending, 2)
	donors := map[string]bool{}
	for key := range r.pending {
		donors[key.donor] = true
		require.NotContains(t, key.donor, id)
	}
	require.Len(t, donors, 2)
	for _, origin := range []string{"null", "http://one.example", "https://127.0.0.1", "https://user@one.example", "https://one.example/path", "https://one.example:123"} {
		req.Header.Set("Origin", origin)
		require.Nil(t, r.counter(req), origin)
	}
	req = httptest.NewRequest("GET", "http://egress/ws", nil)
	req.Header.Set("Origin", "https://one.example")
	require.Nil(t, r.counter(req))
}

func TestUsageDiskFailureKeepsCounters(t *testing.T) {
	dir := t.TempDir()
	key := usageKey{"https://example.org", strings.Repeat("a", 64), time.Now().UTC().Format("2006-01-02")}
	r := &usageReporter{dir: dir, pending: map[usageKey]int64{key: 100}}
	require.NoError(t, os.Remove(dir))
	require.Error(t, r.persist())
	require.EqualValues(t, 100, r.pending[key])
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, r.persist())
	require.Empty(t, r.pending)
}

func TestUsageDrainsMultipleBatches(t *testing.T) {
	requests, total := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var events []usageEvent
		require.NoError(t, json.NewDecoder(r.Body).Decode(&events))
		require.LessOrEqual(t, len(events), 100)
		requests++
		total += len(events)
	}))
	defer server.Close()
	r := &usageReporter{pending: make(map[usageKey]int64), endpoint: server.URL, dir: t.TempDir(), client: server.Client()}
	day := time.Now().UTC().Format("2006-01-02")
	for i := 0; i < 205; i++ {
		r.pending[usageKey{"https://example.org", uuid.NewString(), day}] = 1
	}
	require.NoError(t, r.persist())
	require.NoError(t, r.send(context.Background()))
	require.Equal(t, 3, requests)
	require.Equal(t, 205, total)
	entries, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}
