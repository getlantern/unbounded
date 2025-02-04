package leaderboard

import (
	"fmt"
	"net/url"
	"os"
)

type supabaseCreds struct {
	url *url.URL
	key string
}

func (c *supabaseCreds) initFromEnv() error {
	urlVar := "SUPABASE_URL"
	keyVar := "SUPABASE_KEY"
	urlStr, ok := os.LookupEnv(urlVar)
	if !ok {
		return fmt.Errorf("missing %s", urlVar)
	}
	key, ok := os.LookupEnv(keyVar)
	if !ok {
		return fmt.Errorf("missing %s", keyVar)
	}
	urlParsed, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("unparsable URL %q: %w", urlVar, err)
	}
	c.url = urlParsed
	c.key = key
	return nil
}
