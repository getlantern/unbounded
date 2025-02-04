package leaderboard

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreds(t *testing.T) {
	client, err := new()
	require.NoError(t, err)
	t.Logf("loaded supabase creds for url: %s", client.url)
}

func TestRequest(t *testing.T) {
	client, err := new()
	require.NoError(t, err)

	endpoints := []string{"teams", "connections", "team_members", "users"}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			result, err := client.request(http.MethodGet, endpoint)
			require.NoError(t, err)
			require.Greater(t, len(result), 0)
			t.Logf("%q - got %d", endpoint, len(result))
		})
	}
}
