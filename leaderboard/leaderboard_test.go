package leaderboard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitFromEnv(t *testing.T) {
	creds := &supabaseCreds{}
	err := creds.initFromEnv()
	require.NoError(t, err)
	t.Logf("loaded supabase creds for url: %s", creds.url)
}
