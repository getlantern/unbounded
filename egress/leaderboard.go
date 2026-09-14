package egress

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const usageCapacity = 4096

var usageHost = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$`)

type usageEvent struct {
	ID     string    `json:"event_id"`
	Origin string    `json:"origin"`
	Donor  string    `json:"donor_id"`
	At     time.Time `json:"observed_at"`
	Bytes  int64     `json:"bytes"`
}
type usageKey struct{ origin, donor, day string }
type usageReporter struct {
	mu                              sync.Mutex
	pending                         map[usageKey]int64
	dropped                         int64
	endpoint, credential, salt, dir string
	client                          *http.Client
	cancel                          context.CancelFunc
	done                            chan struct{}
}

var sharedUsage struct {
	sync.Mutex
	reporter *usageReporter
	refs     int
}

func startUsage() (*usageReporter, func()) {
	sharedUsage.Lock()
	defer sharedUsage.Unlock()
	if sharedUsage.reporter == nil {
		endpoint := os.Getenv("LEADERBOARD_ENDPOINT")
		if endpoint == "" {
			return nil, func() {}
		}
		u, err := url.Parse(endpoint)
		key, salt, dir := os.Getenv("LEADERBOARD_INGEST_KEY"), os.Getenv("LEADERBOARD_DONOR_KEY"), os.Getenv("LEADERBOARD_SPOOL_DIR")
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(key) < 32 || len(salt) < 32 || dir == "" {
			slog.Error("Leaderboard reporting disabled: invalid endpoint, keys, or spool directory")
			return nil, func() {}
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			slog.Error("Leaderboard spool unavailable", "error", err)
			return nil, func() {}
		}
		ctx, cancel := context.WithCancel(context.Background())
		r := &usageReporter{pending: make(map[usageKey]int64), endpoint: endpoint, credential: key, salt: salt, dir: dir, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cancel: cancel, done: make(chan struct{})}
		sharedUsage.reporter = r
		go r.run(ctx)
	}
	sharedUsage.refs++
	reporter := sharedUsage.reporter
	var once sync.Once
	return reporter, func() {
		once.Do(func() {
			sharedUsage.Lock()
			defer sharedUsage.Unlock()
			sharedUsage.refs--
			if sharedUsage.refs == 0 {
				reporter.cancel()
				<-reporter.done
				sharedUsage.reporter = nil
			}
		})
	}
}

func (r *usageReporter) counter(req *http.Request) func(int) {
	if r == nil {
		return nil
	}
	origin := req.Header.Get("Origin")
	u, err := url.Parse(origin)
	id, e := uuid.Parse(req.URL.Query().Get("donor_id"))
	if err != nil || e != nil || id == uuid.Nil || u.Scheme != "https" || u.User != nil || u.Host != u.Hostname() || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !usageHost.MatchString(u.Hostname()) || len(u.Host) > 245 {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(r.salt))
	_, _ = io.WriteString(mac, origin+"\x00"+id.String())
	donor := hex.EncodeToString(mac.Sum(nil))
	return func(n int) {
		if n <= 0 {
			return
		}
		key := usageKey{origin, donor, time.Now().UTC().Format("2006-01-02")}
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.pending[key]; !ok && len(r.pending) >= usageCapacity {
			r.dropped += int64(n)
			return
		}
		r.pending[key] += int64(n)
	}
}

func (r *usageReporter) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := r.persist(); err != nil {
				slog.Error("Leaderboard final flush failed", "error", err)
			}
			return
		case <-ticker.C:
			r.mu.Lock()
			dropped := r.dropped
			r.dropped = 0
			r.mu.Unlock()
			if dropped > 0 {
				slog.Warn("Leaderboard memory queue full; contributions undercounted", "dropped_bytes", dropped)
			}
			if err := r.persist(); err != nil {
				slog.Warn("Leaderboard spool write failed", "error", err)
			}
			sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := r.send(sendCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("Leaderboard export will retry", "error", err)
			}
		}
	}
}

func (r *usageReporter) persist() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	r.mu.Lock()
	pending := make(map[usageKey]int64, len(r.pending))
	for k, n := range r.pending {
		pending[k] = n
	}
	r.mu.Unlock()
	complete := func(k usageKey, n int64) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.pending[k] -= n
		if r.pending[k] == 0 {
			delete(r.pending, k)
		}
	}
	for k, n := range pending {
		day, err := time.Parse("2006-01-02", k.day)
		if err != nil {
			continue
		}
		// Discard expired observations; the API retains deduplication receipts longer.
		if day.Before(time.Now().UTC().Add(-6 * 24 * time.Hour)) {
			complete(k, n)
			continue
		}
		if count >= usageCapacity {
			return errors.New("leaderboard disk queue full")
		}
		if n > 1_000_000_000_000 {
			n = 1_000_000_000_000
		}
		event := usageEvent{uuid.NewString(), k.origin, k.donor, day, n}
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		tmp := filepath.Join(r.dir, event.ID+".tmp")
		file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.Write(data)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = os.Rename(tmp, filepath.Join(r.dir, event.ID+".json"))
		}
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("persist usage: %w", err)
		}
		count++
		complete(k, n)
	}
	return nil
}

func (r *usageReporter) send(ctx context.Context) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	batch := []usageEvent{}
	paths := []string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var e usageEvent
		if err = json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("read usage batch: %w", err)
		}
		if e.At.Before(time.Now().Add(-7 * 24 * time.Hour)) {
			_ = os.Remove(path)
			continue
		}
		batch = append(batch, e)
		paths = append(paths, path)
		if len(batch) == 100 {
			if err := r.sendBatch(ctx, batch, paths); err != nil {
				return err
			}
			batch = nil
			paths = nil
		}
	}
	return r.sendBatch(ctx, batch, paths)
}

func (r *usageReporter) sendBatch(ctx context.Context, batch []usageEvent, paths []string) error {
	if len(batch) == 0 {
		return nil
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.credential)
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("export usage: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("usage API status %d", res.StatusCode)
	}
	for _, path := range paths {
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
