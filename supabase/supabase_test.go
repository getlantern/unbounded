package supabase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	client, err := New()
	require.NoError(t, err)
	t.Logf("loaded supabase creds for url: %s", client.baseURL)
}

func TestTeams(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	result, err := client.Teams()
	require.NoError(t, err)
	require.Greater(t, len(result), 0)
	t.Logf("teams: %d results", len(result))
	t.Logf("teams: %+v", result)
}

func TestLeaderboard(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	result, err := client.Leaderboard()
	require.NoError(t, err)
	require.Greater(t, len(result), 0)
	t.Logf("leaderboard: %d results", len(result))
	t.Logf("leaderboard: %+v", result)
}
