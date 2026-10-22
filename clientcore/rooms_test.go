package clientcore

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

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
	if _, ok := f[common.PoolField]; ok {
		t.Error("a producer serving the pool must omit the pool field")
	}
	o.SetServePool(false)
	if got := genesisForm(o, []byte(`{}`)).Get(common.PoolField); got != "0" {
		t.Errorf("pool = %q, want 0", got)
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
		if got := p.pick(all, room); got != "helper" {
			t.Fatalf("attempt %d picked %q, want the room helper", i+1, got)
		}
	}

	// The next pick records the threshold-th failure: from here on, the
	// helper competes with the pool rather than winning every window.
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[p.pick(all, room)] = true
		now = now.Add(roomPreferenceCooldown / 400)
	}
	if len(seen) != len(all) {
		t.Fatalf("during cooldown the pick must be random over all; saw %v", seen)
	}

	// After the cooldown, room preference resumes.
	now = now.Add(roomPreferenceCooldown)
	for i := 0; i < 20; i++ {
		got := p.pick(all, room)
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
			if got := p.pick(all, room); got != "helper" {
				t.Fatalf("round %d attempt %d picked %q", round, i+1, got)
			}
		}
		p.succeeded()
	}

	// Pool-picked failures don't count against rooms.
	p = newRoomPreference(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		p.pick(all, nil)
	}
	if got := p.pick(all, room); got != "helper" {
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
		p.pick([]string{"a", "b"}, []string{"a"})
	}
	<-done
}
