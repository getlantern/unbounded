//go:build !wasm

package clientcore

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pion/transport/v5/vnet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common/helperticket"
	"github.com/getlantern/broflake/freddie"
)

// startRealFreddie runs a real Freddie that verifies room tickets signed by priv, and returns its
// URL.
func startRealFreddie(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	f, err := freddie.New(context.Background(), addr)
	require.NoError(t, err)
	keys := func() []helperticket.Key {
		return []helperticket.Key{helperticket.NewKey(priv.Public().(ed25519.PublicKey))}
	}
	f.RoomTicket = helperticket.RoomFunc(keys, time.Now)
	f.KnownKeyID = helperticket.KnownKeyFunc(keys)
	go func() { _ = f.ListenAndServe() }()
	t.Cleanup(func() { _ = f.Shutdown() })
	url := "http://" + addr
	require.Eventually(t, func() bool {
		res, err := http.Get(url + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return true
	}, 5*time.Second, 10*time.Millisecond)
	return url
}

// Through a real Freddie, a volunteer serving both its rooms and the pool serves its friend, who
// reached it through their room, a personal direct exit, and a stranger a pool one: Freddie vouches
// for the friend's admission on its offer.
func TestDirectExitKindThroughFreddie(t *testing.T) {
	for _, tc := range []struct {
		name     string
		friend   bool
		personal bool
	}{
		{"friend through its room", true, true},
		{"stranger", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, priv, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			sign := func(role helperticket.Role) string {
				tk, err := helperticket.Sign(priv, helperticket.Ticket{PairingID: testPairingP, Role: role, Expires: time.Now().Add(time.Hour)})
				require.NoError(t, err)
				return tk
			}
			srv := startRealFreddie(t, priv)
			cn, pn, stunURL, _ := newVNetICE(t, vnet.EndpointIndependent)

			sessions := make(chan bool, 4)
			served := make(chan bool, 4)
			po := e2eOptions(srv, pn, stunURL)
			po.Exit = echoExit(true, sessions, served)
			po.SetRoomTickets([]string{sign(helperticket.RoleVolunteer)})
			po.SetServePool(true)

			paths := make(chan ExitPath, 1)
			co := e2eOptions(srv, cn, stunURL)
			if tc.friend {
				co.SetRoomTickets([]string{sign(helperticket.RoleInviter)})
			}
			co.OnExitPath = func(p ExitPath) {
				select {
				case paths <- p:
				default:
					_ = p.Conn.Close()
				}
			}
			// The consumer waits on Freddie first, so the friend is in its room when the genesis
			// comes.
			startConsumer(t, co)
			time.Sleep(200 * time.Millisecond)
			startProducer(t, po)

			var path ExitPath
			select {
			case path = <-paths:
			case <-time.After(45 * time.Second):
				t.Fatal("no direct exit session")
			}
			assert.Equal(t, tc.personal, path.Personal, "the consumer learns the session's kind")
			select {
			case personal := <-served:
				assert.Equal(t, tc.personal, personal, "the exit serves the session as its kind")
			case <-time.After(5 * time.Second):
				t.Fatal("the exit served no session")
			}
		})
	}
}
