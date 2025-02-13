package supabase

import (
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func init() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	handler := slog.NewTextHandler(os.Stdout, opts)
	log = slog.New(handler)
}

func TestCreds(t *testing.T) {
	client, err := new()
	require.NoError(t, err)
	t.Logf("loaded supabase creds for url: %s", client.baseURL)
}

func TestEndpoints(t *testing.T) {
	client, err := new()
	require.NoError(t, err)

	t.Run("teams", func(t *testing.T) {
		result, err := client.request(http.MethodGet, "teams")
		require.NoError(t, err)
		require.Greater(t, len(result), 0)
		t.Logf("teams: %d results", len(result))
	})

	t.Run("leaderboard", func(t *testing.T) {
		params := map[string]string{"select": "name"}
		result, err := client.request(http.MethodGet, "leaderboard", params)
		require.NoError(t, err)
		require.Greater(t, len(result), 0)
		t.Logf("leaderboard: %d results", len(result))
	})

}
