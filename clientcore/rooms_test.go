package clientcore

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"testing"

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
// before rooms, with and without target countries.
func TestNoRoomsWireIsByteIdenticalToPreRooms(t *testing.T) {
	g := []byte(`{"PathAssertion":{"Allow":null,"Deny":null}}`)
	for _, countries := range [][]string{nil, {"IR", "RU"}} {
		o := NewDefaultWebRTCOptions()
		o.SetTargetCountries(countries)
		o.SetServePool(true)
		if got, want := genesisForm(o, g).Encode(), preRoomsGenesisForm(o, g).Encode(); got != want {
			t.Errorf("countries %v: form %s, want %s", countries, got, want)
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
			t.Errorf("request %s %s %v, want %s %s %v", got.Method, got.URL, got.Header, want.Method, want.URL, want.Header)
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
	if _, ok := req.Header[common.RoomTicketsHeader]; ok {
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
