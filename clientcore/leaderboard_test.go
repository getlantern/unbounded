package clientcore

import (
	"net/url"
	"testing"
)

func TestDonorAttributionRequiresWSS(t *testing.T) {
	for _, scheme := range []string{"ws", "wss", "http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			called := false
			address := egressAddress(&EgressOptions{Addr: scheme + "://egress.example", Endpoint: "/ws?existing=1", DonorID: func() string { called = true; return "installation" }})
			parsed, err := url.Parse(address)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Query().Get("existing") != "1" {
				t.Fatal("lost existing query")
			}
			if scheme == "wss" {
				if !called || parsed.Query().Get("donor_id") != "installation" {
					t.Fatal("missing secure attribution")
				}
			} else if called || parsed.Query().Has("donor_id") {
				t.Fatal("identifier accessed or sent over an unsupported transport")
			}
		})
	}
}
