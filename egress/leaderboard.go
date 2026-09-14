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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const usageCapacity = 100000

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
	counters                        map[*usageCounter]struct{}
	capacity                        int
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
		capacity := usageCapacity
		if raw := os.Getenv("LEADERBOARD_CAPACITY"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 100 || n > 1000000 {
				slog.Error("Leaderboard reporting disabled: LEADERBOARD_CAPACITY must be 100..1000000")
				return nil, func() {}
			}
			capacity = n
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			slog.Error("Leaderboard spool unavailable", "error", err)
			return nil, func() {}
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
					slog.Warn("Leaderboard stale temporary file cleanup failed", "error", err)
				}
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		r := &usageReporter{capacity: capacity, pending: make(map[usageKey]int64), endpoint: endpoint, credential: key, salt: salt, dir: dir, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cancel: cancel, done: make(chan struct{})}
		sharedUsage.reporter = r
		go r.run(ctx)
	}
	sharedUsage.refs++
	reporter := sharedUsage.reporter
	return reporter, usageRelease(reporter)
}

func usageRelease(reporter *usageReporter) func() {
	var once sync.Once
	return func() {
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

// Accepted handlers own a reporter reference until their packet connection closes.
func retainUsage(reporter *usageReporter) (func(), bool) {
	if reporter == nil {
		return func() {}, true
	}
	sharedUsage.Lock()
	defer sharedUsage.Unlock()
	if sharedUsage.reporter != reporter || sharedUsage.refs == 0 {
		return nil, false
	}
	sharedUsage.refs++
	return usageRelease(reporter), true
}

type usageCounter struct {
	reporter      *usageReporter
	origin, donor string
	ops           sync.RWMutex
	mu            sync.Mutex
	daily         map[int64]int64
	once          sync.Once
}

func (c *usageCounter) add(n int) {
	if n <= 0 {
		return
	}
	day := time.Now().Unix() / 86400
	c.mu.Lock()
	c.daily[day] += int64(n)
	c.mu.Unlock()
}

// The reporter mutex is held only while merging snapshots, never on packet I/O.
func (r *usageReporter) collect(c *usageCounter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for day, n := range c.daily {
		key := usageKey{c.origin, c.donor, time.Unix(day*86400, 0).UTC().Format("2006-01-02")}
		if _, ok := r.pending[key]; !ok && len(r.pending) >= r.limit() {
			r.dropped += n
		} else {
			r.pending[key] += n
		}
	}
	clear(c.daily)
}

func (c *usageCounter) close() {
	c.once.Do(func() {
		// Close the socket before waiting here, so blocked reads/writes can finish.
		c.ops.Lock()
		defer c.ops.Unlock()
		c.reporter.mu.Lock()
		defer c.reporter.mu.Unlock()
		c.reporter.collect(c)
		delete(c.reporter.counters, c)
	})
}

func (r *usageReporter) counter(req *http.Request) *usageCounter {
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters == nil {
		r.counters = make(map[*usageCounter]struct{})
	}
	c := &usageCounter{reporter: r, origin: origin, donor: donor, daily: make(map[int64]int64)}
	r.counters[c] = struct{}{}
	return c
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
	r.mu.Lock()
	for c := range r.counters {
		r.collect(c)
	}
	r.mu.Unlock()
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
		if count >= r.limit() {
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

func (r *usageReporter) limit() int {
	if r.capacity > 0 {
		return r.capacity
	}
	return usageCapacity
}

func (r *usageReporter) send(ctx context.Context) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	var sendErr error
	batch := []usageEvent{}
	paths := []string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.dir, entry.Name())
		if ctx.Err() != nil {
			return errors.Join(sendErr, ctx.Err())
		}
		data, err := os.ReadFile(path)
		var e usageEvent
		if err == nil {
			err = json.Unmarshal(data, &e)
		}
		if err == nil && !validUsage(e) {
			err = errors.New("invalid usage event")
		}
		if err != nil {
			slog.Warn("Discarding unreadable or invalid leaderboard spool event", "file", entry.Name(), "error", err)
			if removeErr := os.Remove(path); removeErr != nil {
				sendErr = errors.Join(sendErr, fmt.Errorf("remove invalid usage: %w", removeErr))
			}
			continue
		}
		if e.At.Before(time.Now().Add(-7 * 24 * time.Hour)) {
			_ = os.Remove(path)
			continue
		}
		batch = append(batch, e)
		paths = append(paths, path)
		if len(batch) == 100 {
			if err := r.sendBatch(ctx, batch, paths); err != nil {
				sendErr = errors.Join(sendErr, err)
			}
			batch = nil
			paths = nil
		}
	}
	return errors.Join(sendErr, r.sendBatch(ctx, batch, paths))
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
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("usage API status %d", res.StatusCode)
	}
	var ack struct {
		IDs []string `json:"acknowledged_event_ids"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 16385))
	if err = decoder.Decode(&ack); err != nil {
		return fmt.Errorf("decode usage acknowledgement: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid usage acknowledgement body")
	}
	if len(ack.IDs) == 0 {
		return errors.New("usage API returned no acknowledged_event_ids; deploy the lantern-cloud leaderboard API before egress")
	}
	acknowledged := make(map[string]bool, len(ack.IDs))
	for _, id := range ack.IDs {
		acknowledged[id] = true
	}
	remaining := 0
	for i, event := range batch {
		if !acknowledged[event.ID] {
			remaining++
			continue
		}
		if err = os.Remove(paths[i]); err != nil {
			return err
		}
	}
	if remaining > 0 {
		return fmt.Errorf("usage API left %d events unacknowledged", remaining)
	}
	return nil
}

func validUsage(e usageEvent) bool {
	id, err := uuid.Parse(e.ID)
	u, originErr := url.Parse(e.Origin)
	_, donorErr := hex.DecodeString(e.Donor)
	return err == nil && id != uuid.Nil && id.String() == e.ID && originErr == nil && u.Scheme == "https" && u.User == nil && u.Host == u.Hostname() && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && len(u.Host) <= 245 && usageHost.MatchString(u.Hostname()) && donorErr == nil && len(e.Donor) == 64 && strings.ToLower(e.Donor) == e.Donor && e.Bytes > 0 && e.Bytes <= 1_000_000_000_000 && !e.At.IsZero() && !e.At.After(time.Now().Add(5*time.Minute))
}
