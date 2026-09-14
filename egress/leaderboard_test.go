package egress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		ackUsage(w, events)
	}))
	defer server.Close()
	r := &usageReporter{pending: make(map[usageKey]int64), endpoint: server.URL, credential: "server-secret", salt: "donor-secret", dir: t.TempDir(), client: server.Client()}
	req := httptest.NewRequest("GET", "http://egress/ws?donor_id="+uuid.NewString(), nil)
	req.Header.Set("Origin", "https://example.org")
	count := r.counter(req)
	require.NotNil(t, count)
	count.add(123)
	count.add(456)
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
		r.counter(req).add(10)
	}
	r.mu.Lock()
	for c := range r.counters {
		r.collect(c)
	}
	r.mu.Unlock()
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
		ackUsage(w, events)
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

func ackUsage(w http.ResponseWriter, events []usageEvent) {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"acknowledged_event_ids": ids})
}

func TestUsageRequiresExplicitAcknowledgement(t *testing.T) {
	first, second := usageEvent{ID: uuid.NewString()}, usageEvent{ID: uuid.NewString()}
	for _, tc := range []struct {
		name, body   string
		removedFirst bool
	}{
		{"empty", "", false}, {"legacy", `{"ok":true}`, false}, {"malformed", "{", false},
		{"unknown", `{"acknowledged_event_ids":["unknown"]}`, false},
		{"partial", `{"acknowledged_event_ids":["` + first.ID + `"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "first.json"), filepath.Join(dir, "second.json")}
			for _, p := range paths {
				require.NoError(t, os.WriteFile(p, []byte("saved"), 0600))
			}
			r := &usageReporter{endpoint: server.URL, client: server.Client()}
			require.Error(t, r.sendBatch(context.Background(), []usageEvent{first, second}, paths))
			_, err := os.Stat(paths[0])
			if tc.removedFirst {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
			}
			_, err = os.Stat(paths[1])
			require.NoError(t, err)
		})
	}
}

func TestReporterSurvivesListenerUntilPacketOperationsDrain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEADERBOARD_ENDPOINT", "https://example.org/usage")
	t.Setenv("LEADERBOARD_INGEST_KEY", strings.Repeat("k", 32))
	t.Setenv("LEADERBOARD_DONOR_KEY", strings.Repeat("s", 32))
	t.Setenv("LEADERBOARD_SPOOL_DIR", dir)
	r, releaseListener := startUsage()
	require.NotNil(t, r)
	releaseHandler, ok := retainUsage(r)
	require.True(t, ok)
	req := httptest.NewRequest("GET", "https://egress/ws?donor_id="+uuid.NewString(), nil)
	req.Header.Set("Origin", "https://example.org")
	c := r.counter(req)
	require.NotNil(t, c)
	releaseListener()
	select {
	case <-r.done:
		t.Fatal("reporter stopped while a handler owns it")
	default:
	}
	c.ops.RLock()
	closing := make(chan struct{})
	closed := make(chan struct{})
	go func() { close(closing); c.close(); releaseHandler(); close(closed) }()
	<-closing
	// A successful packet operation can finish after listener closure.
	c.add(987)
	c.ops.RUnlock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("reporter shutdown did not drain")
	}
	<-r.done
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	require.NoError(t, err)
	var event usageEvent
	require.NoError(t, json.Unmarshal(data, &event))
	require.EqualValues(t, 987, event.Bytes)
	_, ok = retainUsage(r)
	require.False(t, ok)
}

func TestUsageCountersDoNotTakeReporterLockOnPackets(t *testing.T) {
	r := &usageReporter{pending: make(map[usageKey]int64)}
	req := httptest.NewRequest("GET", "https://egress/ws?donor_id="+uuid.NewString(), nil)
	req.Header.Set("Origin", "https://example.org")
	a, b := r.counter(req), r.counter(req)
	r.mu.Lock()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, c := range []*usageCounter{a, b} {
			wg.Add(1)
			go func(c *usageCounter) {
				defer wg.Done()
				for i := 0; i < 1000; i++ {
					c.add(1)
				}
			}(c)
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		r.mu.Unlock()
		t.Fatal("packet accounting blocked on reporter mutex")
	}
	r.collect(a)
	r.collect(b)
	require.Len(t, r.pending, 1)
	for _, n := range r.pending {
		require.EqualValues(t, 2000, n)
	}
	r.mu.Unlock()
}
