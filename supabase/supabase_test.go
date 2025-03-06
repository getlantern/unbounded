package supabase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	testUserID string = "fd40c984-2d1c-4fe8-b4d2-92edf7cbf860"
	testTeamID string = "8"
)

func TestNew(t *testing.T) {
	client, err := New()
	require.NoError(t, err)
	t.Logf("loaded supabase creds for url: %s", client.baseURL)
}

func TestConnections(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	result, err := client.Connections()
	require.NoError(t, err)
	require.Greater(t, len(result), 0)
	t.Logf("connections: %d results", len(result))
	t.Logf("connections: %+v", result)
}

func TestLeaderboard(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	result, err := client.Leaderboard()
	t.Logf("leaderboard: %d results", len(result))
	t.Logf("leaderboard: %+v", result)
	require.NoError(t, err)
	require.Greater(t, len(result), 0)
}

func TestAddConnection(t *testing.T) {
	client, err := New()
	require.NoError(t, err)

	result, err := client.AddConnection(testUserID, testTeamID)
	require.NoError(t, err)
	t.Logf("add connection: %+v", result)
}
