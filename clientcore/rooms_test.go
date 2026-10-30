package clientcore

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/getlantern/broflake/common"
)

// A producer that uses no rooms sends exactly the form releases before rooms
// sent, so an old Freddie and a new one see the same request.
func TestGenesisFormWithoutRoomsIsUnchanged(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.SetTargetCountries([]string{"IR"})
	got := genesisForm(o, []byte(`{"x":1}`))
	want := url.Values{
		"data":                    {`{"x":1}`},
		"send-to":                 {o.GenesisAddr},
		"type":                    {strconv.Itoa(int(common.SignalMsgGenesis))},
		common.TargetCountryField: {"IR"},
	}
	if got.Encode() != want.Encode() {
		t.Errorf("form %s, want %s", got.Encode(), want.Encode())
	}
}

// preRoomsGenesisForm and preRoomsSubscribeRequest are the producer's form and
// the consumer's request exactly as the FSMs built them before rooms.
func preRoomsGenesisForm(options *WebRTCOptions, g []byte) url.Values {
	form := url.Values{
		"data":    {string(g)},
		"send-to": {options.GenesisAddr},
		"type":    {strconv.Itoa(int(common.SignalMsgGenesis))},
	}
	for _, c := range options.targetCountries() {
		form.Add(common.TargetCountryField, c)
	}
	return form
}

func preRoomsSubscribeRequest(ctx context.Context, options *WebRTCOptions) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", options.DiscoverySrv+options.Endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add(common.VersionHeader, common.Version)
	return req, nil
}

// With no tickets and serve-pool on, the wire is byte-identical to releases
// before rooms: for untouched default options (no setter called), for zero
// options, and with serve-pool set explicitly, with and without target
// countries.
func TestNoRoomsWireIsByteIdenticalToPreRooms(t *testing.T) {
	g := []byte(`{"PathAssertion":{"Allow":null,"Deny":null}}`)
	cases := []func() *WebRTCOptions{
		// Untouched defaults: no setter called at all.
		NewDefaultWebRTCOptions,
		func() *WebRTCOptions { return &WebRTCOptions{} },
	}
	for _, countries := range [][]string{nil, {"IR", "RU"}} {
		cases = append(cases, func() *WebRTCOptions {
			o := NewDefaultWebRTCOptions()
			o.SetTargetCountries(countries)
			o.SetServePool(true)
			return o
		})
	}
	for i, newOptions := range cases {
		o := newOptions()
		countries := o.targetCountries()
		if got, want := genesisForm(o, g).Encode(), preRoomsGenesisForm(o, g).Encode(); got != want {
			t.Errorf("case %d (countries %v): form %s, want %s", i, countries, got, want)
		}

		got, err := newSubscribeRequest(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		want, err := preRoomsSubscribeRequest(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if got.Method != want.Method || got.URL.String() != want.URL.String() ||
			!reflect.DeepEqual(got.Header, want.Header) || got.Body != want.Body ||
			got.ContentLength != want.ContentLength {
			t.Errorf("case %d: request %s %s %v, want %s %s %v", i, got.Method, got.URL, got.Header, want.Method, want.URL, want.Header)
		}
	}
}

func TestGenesisFormCarriesRoomsAndPool(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	o.SetRoomTickets([]string{"a.b", "c.d"})
	f := genesisForm(o, []byte(`{}`))
	if got := f[common.RoomTicketField]; !slices.Equal(got, []string{"a.b", "c.d"}) {
		t.Errorf("room tickets %q", got)
	}
	if got := f.Get("send-to"); got != o.GenesisAddr {
		t.Errorf("a producer serving the pool sends to %q, want %q", got, o.GenesisAddr)
	}
	if _, ok := f[common.PoolField]; ok {
		t.Error("a producer serving the pool must omit the pool field")
	}

	// A rooms-only producer says so in send-to, which an old Freddie refuses,
	// and not in the pool field, which an old Freddie ignores.
	o.SetServePool(false)
	f = genesisForm(o, []byte(`{}`))
	if got := f.Get("send-to"); got != common.SignalSendToGenesisRooms {
		t.Errorf("a rooms-only producer sends to %q, want %q", got, common.SignalSendToGenesisRooms)
	}
	if _, ok := f[common.PoolField]; ok {
		t.Error("a rooms-only producer must not send the pool field")
	}
	if got := f[common.RoomTicketField]; !slices.Equal(got, []string{"a.b", "c.d"}) {
		t.Errorf("rooms-only room tickets %q", got)
	}
}

// oldFreddie is the genesis routing of a Freddie that predates rooms
// (origin/main freddie/freddie.go handleSignalPost): send-to "genesis" is
// broadcast to every waiting consumer whatever the other fields say, and any
// other send-to is a reply to a request ID, which it can't find, so 404.
func oldFreddie(broadcasts *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("send-to") == "genesis" {
			broadcasts.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404\n"))
	})
}

func runProducerGenesis(t *testing.T, o *WebRTCOptions) (int, time.Duration) {
	t.Helper()
	fsm := NewProducerWebRTC(o, nil)
	start := time.Now()
	next, _ := fsm.state[2](context.Background(), fsm.com, []interface{}{
		(*webrtc.PeerConnection)(nil),
		common.PathAssertion{},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	return next, time.Since(start)
}

// Against a Freddie that predates rooms, a rooms-only producer's genesis is
// refused rather than broadcast, and the producer backs off before trying
// again rather than spinning.
func TestRoomsOnlyGenesisFailsClosedOnOldFreddie(t *testing.T) {
	var broadcasts atomic.Int32
	srv := httptest.NewServer(oldFreddie(&broadcasts))
	t.Cleanup(srv.Close)

	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.ErrorBackoff = 100 * time.Millisecond
	o.SetRoomTickets([]string{"a.b"})
	o.SetServePool(false)

	for i := 0; i < 3; i++ {
		next, took := runProducerGenesis(t, o)
		if next != 1 {
			t.Errorf("after a refused genesis the producer went to state %d, want 1", next)
		}
		if took < o.ErrorBackoff {
			t.Errorf("after a refused genesis the producer retried after %v, want at least %v", took, o.ErrorBackoff)
		}
	}
	if n := broadcasts.Load(); n != 0 {
		t.Fatalf("an old Freddie broadcast a rooms-only genesis %d times", n)
	}

	// The same producer serving the pool is broadcast, as before rooms.
	o.SetServePool(true)
	if next, _ := runProducerGenesis(t, o); next != 1 {
		t.Errorf("after an unanswered genesis the producer went to state %d, want 1", next)
	}
	if n := broadcasts.Load(); n != 1 {
		t.Errorf("an old Freddie broadcast a pool genesis %d times, want 1", n)
	}
}

func TestSubscribeRequestCarriesTickets(t *testing.T) {
	o := NewDefaultWebRTCOptions()
	req, err := newSubscribeRequest(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.URL.String() != o.DiscoverySrv+o.Endpoint {
		t.Errorf("request %s %s", req.Method, req.URL)
	}
	if req.Header.Get(common.VersionHeader) != common.Version {
		t.Error("missing version header")
	}
	if v := req.Header.Values(common.RoomTicketsHeader); len(v) > 0 {
		t.Error("a consumer without tickets must not send the header")
	}

	o.RoomTicketSource = func() []string { return []string{"a.b", "c.d"} }
	req, err = newSubscribeRequest(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get(common.RoomTicketsHeader); got != "a.b,c.d" {
		t.Errorf("header %q, want a.b,c.d", got)
	}
}

func TestPickGenesisPrefersRooms(t *testing.T) {
	all := []string{"x", "y", "z"}
	for i := 0; i < 50; i++ {
		if got := pickGenesis(all, []string{"y"}); got != "y" {
			t.Fatalf("picked %q over the room genesis", got)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[pickGenesis(all, nil)] = true
	}
	if len(seen) != 3 {
		t.Errorf("without rooms the pick must stay random over all; saw %v", seen)
	}
}

// An unreachable room helper must not keep the consumer from the pool: each
// pick is one connection attempt, and an attempt that never reaches
// succeeded failed (state 4's NAT timeout returns to state 0, then state 1
// picks again). After roomPreferenceMaxFailures such attempts the consumer
// picks among all for roomPreferenceCooldown, then prefers rooms again.
func TestRoomPreferenceFallsBackToPoolAfterFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := newRoomPreference(func() time.Time { return now })
	all := []string{"helper", "p1", "p2", "p3"}
	room := []string{"helper"}

	// The first attempt and the retries up to the threshold go to the helper.
	for i := 0; i < roomPreferenceMaxFailures; i++ {
		if got := p.pick(context.Background(), all, room); got != "helper" {
			t.Fatalf("attempt %d picked %q, want the room helper", i+1, got)
		}
	}

	// The next pick records the threshold-th failure: from here on, the
	// helper competes with the pool rather than winning every window.
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[p.pick(context.Background(), all, room)] = true
		now = now.Add(roomPreferenceCooldown / 400)
	}
	if len(seen) != len(all) {
		t.Fatalf("during cooldown the pick must be random over all; saw %v", seen)
	}

	// After the cooldown, room preference resumes.
	now = now.Add(roomPreferenceCooldown)
	for i := 0; i < 20; i++ {
		got := p.pick(context.Background(), all, room)
		if got != "helper" {
			t.Fatalf("after cooldown picked %q, want the room helper", got)
		}
		p.succeeded()
	}
}

func TestRoomPreferenceSuccessResetsFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := newRoomPreference(func() time.Time { return now })
	all := []string{"helper", "p1", "p2", "p3"}
	room := []string{"helper"}

	// Fail once short of the threshold, connect, and repeat: failures are
	// consecutive, so preference is never suspended.
	for round := 0; round < 5; round++ {
		for i := 0; i < roomPreferenceMaxFailures; i++ {
			if got := p.pick(context.Background(), all, room); got != "helper" {
				t.Fatalf("round %d attempt %d picked %q", round, i+1, got)
			}
		}
		p.succeeded()
	}

	// Pool-picked failures don't count against rooms.
	p = newRoomPreference(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		p.pick(context.Background(), all, nil)
	}
	if got := p.pick(context.Background(), all, room); got != "helper" {
		t.Errorf("picked %q after pool-only failures, want the room helper", got)
	}
}

func TestRoomPreferenceIsRaceFree(t *testing.T) {
	p := newRoomPreference(time.Now)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			p.succeeded()
		}
	}()
	for i := 0; i < 1000; i++ {
		p.pick(context.Background(), []string{"a", "b"}, []string{"a"})
	}
	<-done
}

// snapshot reads p's attempt state under its lock.
func (p *roomPreference) snapshot() (pending bool, failures int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending, p.failures
}

// genesisLine is a genesis message as Freddie streams it to a consumer.
func genesisLine(t *testing.T, replyTo string, room bool) string {
	t.Helper()
	b, err := json.Marshal(common.SignalMsg{ReplyTo: replyTo, Type: common.SignalMsgGenesis, Payload: `{}`, Room: room})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func newTestPeerConnection(t *testing.T) (*webrtc.PeerConnection, *webrtc.DataChannel) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	d, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		t.Fatal(err)
	}
	return pc, d
}

// The consumer FSM wires the room preference in: state 1 answers the genesis
// that came through a room, through pick, so the attempt is pending; and
// state 5, a connection, records success.
func TestConsumerFSMUsesRoomPreference(t *testing.T) {
	const roomGenesis = "room-genesis"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var lines strings.Builder
		for i := 0; i < 20; i++ {
			lines.WriteString(genesisLine(t, "pool-genesis-"+strconv.Itoa(i), false))
		}
		lines.WriteString(genesisLine(t, roomGenesis, true))
		w.Write([]byte(lines.String()))
	}))
	t.Cleanup(srv.Close)

	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	o.Patience = 2 * time.Second
	now := time.Unix(1_700_000_000, 0)
	rooms := newRoomPreference(func() time.Time { return now })
	fsm := newConsumerWebRTC(o, nil, rooms)
	ctx := context.Background()

	pc, d := newTestPeerConnection(t)
	connectionChange := make(chan webrtc.PeerConnectionState, 1)
	connectionClosed := make(chan struct{}, 1)
	next, out := fsm.state[1](ctx, fsm.com, []interface{}{pc, make(chan *webrtc.DataChannel, 1), connectionChange, connectionClosed})
	if next != 2 {
		t.Fatalf("state 1 went to state %d, want 2", next)
	}
	if got := out[1].(string); got != roomGenesis {
		t.Errorf("state 1 answered %q, want the room genesis", got)
	}
	if pending, _ := rooms.snapshot(); !pending {
		t.Error("state 1 did not pick through the room preference: the room attempt is not pending")
	}

	// A connection: state 5 records success before it proxies. Seed a failure
	// so success is visible in the counter too.
	rooms.mu.Lock()
	rooms.failures = 1
	rooms.mu.Unlock()
	connectionClosed <- struct{}{}
	next, _ = fsm.state[5](ctx, fsm.com, []interface{}{pc, d, connectionChange, connectionClosed})
	if next != 0 {
		t.Fatalf("state 5 went to state %d, want 0", next)
	}
	if pending, failures := rooms.snapshot(); pending || failures != 0 {
		t.Errorf("after state 5: pending=%v failures=%d, want a recorded success", pending, failures)
	}
}

// runOffer runs consumer state 2, the offer, for replyTo against srv.
func runOffer(t *testing.T, fsm *WorkerFSM, ctx context.Context, replyTo string) int {
	t.Helper()
	next, _ := fsm.state[2](ctx, fsm.com, []interface{}{
		(*webrtc.PeerConnection)(nil),
		replyTo,
		webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0"},
		make(chan *webrtc.DataChannel, 1),
		make(chan webrtc.PeerConnectionState, 1),
		make(chan struct{}, 1),
	})
	return next
}

// Losing the genesis race to another consumer (404), or a version refusal
// (418), says nothing about the room helper, so it never trips the room
// cooldown. An offer Freddie accepted that then comes to nothing does count.
func TestConsumerLostGenesisRaceIsNotARoomFailure(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTeapot} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(strconv.Itoa(status) + "\n"))
			}))
			t.Cleanup(srv.Close)
			o := NewDefaultWebRTCOptions()
			o.DiscoverySrv = srv.URL
			o.ErrorBackoff = time.Millisecond
			now := time.Unix(1_700_000_000, 0)
			rooms := newRoomPreference(func() time.Time { return now })
			fsm := newConsumerWebRTC(o, nil, rooms)
			all := []string{"helper", "p1", "p2", "p3"}

			for i := 0; i < 3*roomPreferenceMaxFailures; i++ {
				if got := rooms.pick(context.Background(), all, []string{"helper"}); got != "helper" {
					t.Fatalf("attempt %d picked %q after lost races, want the room helper", i+1, got)
				}
				if next := runOffer(t, fsm, context.Background(), "helper"); next != 1 {
					t.Fatalf("state 2 went to state %d, want 1", next)
				}
				if pending, failures := rooms.snapshot(); pending || failures != 0 {
					t.Fatalf("after a %d: pending=%v failures=%d, want the attempt abandoned", status, pending, failures)
				}
			}
		})
	}

	// Freddie accepted the offer but no answer came: that attempt got past the
	// race, so it stays pending and counts at the next pick.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = srv.URL
	rooms := newRoomPreference(time.Now)
	fsm := newConsumerWebRTC(o, nil, rooms)
	rooms.pick(context.Background(), []string{"helper"}, []string{"helper"})
	runOffer(t, fsm, context.Background(), "helper")
	if pending, _ := rooms.snapshot(); !pending {
		t.Error("an accepted offer that came to nothing was not left pending")
	}
	rooms.pick(context.Background(), []string{"helper"}, nil)
	if _, failures := rooms.snapshot(); failures != 1 {
		t.Errorf("failures = %d after an accepted offer came to nothing, want 1", failures)
	}
}

// waitNotPending waits for p's current attempt to be cleared.
func waitNotPending(t *testing.T, p *roomPreference) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if pending, _ := p.snapshot(); !pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a cancelled attempt is still pending")
		}
		time.Sleep(time.Millisecond)
	}
}

// An attempt cut short by its ctx (the worker stopping) is abandoned, not
// failed, and a stale cancel doesn't touch a later attempt.
func TestRoomPreferenceCancelledAttemptIsAbandoned(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := newRoomPreference(func() time.Time { return now })
	all := []string{"helper", "p1", "p2", "p3"}
	room := []string{"helper"}

	for i := 0; i < 3*roomPreferenceMaxFailures; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		if got := p.pick(ctx, all, room); got != "helper" {
			t.Fatalf("attempt %d picked %q after cancelled attempts, want the room helper", i+1, got)
		}
		cancel()
		waitNotPending(t, p)
		if _, failures := p.snapshot(); failures != 0 {
			t.Fatalf("failures = %d after cancelled attempts, want 0", failures)
		}
	}

	// A ctx already cancelled at pick.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.pick(ctx, all, room)
	waitNotPending(t, p)

	// The first attempt's cancel comes after a second attempt began, both
	// through its ctx and as a callback that was already running when the
	// second pick stopped the watch.
	ctx1, cancel1 := context.WithCancel(context.Background())
	p.pick(ctx1, all, room)
	p.mu.Lock()
	first := p.attempt
	p.mu.Unlock()
	p.pick(context.Background(), all, room)
	cancel1()
	p.cancelled(first)
	time.Sleep(20 * time.Millisecond)
	if pending, failures := p.snapshot(); !pending || failures != 1 {
		t.Errorf("after a stale cancel: pending=%v failures=%d, want the second attempt pending and the first counted", pending, failures)
	}
}

// A room helper attempt, a failure, the suspension and a pool connection made
// while room helpers advertise are each logged at Info, so a helper consumer
// carrying its user's traffic through strangers shows in production logs.
func TestRoomPreferenceLogsWhereTrafficGoes(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := time.Unix(0, 0)
	p := newRoomPreference(func() time.Time { return now })
	all, room := []string{"helper", "stranger"}, []string{"helper"}

	p.pick(context.Background(), all, room)
	p.succeeded()
	for i := 0; i < roomPreferenceMaxFailures+1; i++ {
		p.pick(context.Background(), all, room)
	}
	// A room helper offers, but the suspended preference picks the only other
	// candidate, a stranger: traffic goes through the pool despite the offer.
	p.pick(context.Background(), []string{"stranger"}, []string{"helper"})
	p.succeeded()

	out := buf.String()
	for _, want := range []string{
		"Answering a room helper's offer",
		"Connected through a room helper",
		"Room helper attempt failed",
		"Room helper attempts keep failing",
		"Room preference suspended, picking from all candidates including room helpers",
		"Connected through a pool volunteer although a room helper offered",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// While room preference is suspended, a random pick that lands on a room
// helper is still a connection through that helper.
func TestRoomPreferenceLogsARoomHelperPickedDuringCooldown(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := time.Unix(0, 0)
	p := newRoomPreference(func() time.Time { return now })
	for i := 0; i < roomPreferenceMaxFailures+1; i++ {
		p.pick(context.Background(), []string{"helper"}, []string{"helper"})
	}
	buf.Reset()
	if got := p.pick(context.Background(), []string{"helper"}, []string{"helper"}); got != "helper" {
		t.Fatalf("picked %q", got)
	}
	p.succeeded()
	if out := buf.String(); !strings.Contains(out, "Connected through a room helper") || strings.Contains(out, "pool volunteer") {
		t.Errorf("a room helper picked during cooldown was not logged as one:\n%s", out)
	}
}

// A pool connection is logged as bypassing a helper only when this attempt was
// offered one, not because an earlier attempt was.
func TestRoomPreferenceLogsBypassOnlyWhenOffered(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newRoomPreference(time.Now)
	p.pick(context.Background(), []string{"helper"}, []string{"helper"})
	p.succeeded()
	buf.Reset()
	p.pick(context.Background(), []string{"stranger"}, nil)
	p.succeeded()
	if out := buf.String(); strings.Contains(out, "pool volunteer") {
		t.Errorf("a pool connection with no room offer was logged as bypassing one:\n%s", out)
	}
}

// A room helper picked at random during the cooldown that then fails is logged
// as a failure, without counting toward another suspension.
func TestRoomPreferenceLogsACooldownRoomHelperFailure(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := time.Unix(0, 0)
	p := newRoomPreference(func() time.Time { return now })
	for i := 0; i < roomPreferenceMaxFailures+1; i++ {
		p.pick(context.Background(), []string{"helper"}, []string{"helper"})
	}
	buf.Reset()
	p.pick(context.Background(), []string{"helper"}, []string{"helper"})
	if _, failures := p.snapshot(); failures != 0 {
		t.Fatalf("a cooldown attempt counted toward suspension: %d", failures)
	}
	if out := buf.String(); !strings.Contains(out, "Room helper attempt failed during cooldown") {
		t.Errorf("a failed cooldown room helper attempt was not logged:\n%s", out)
	}
}
