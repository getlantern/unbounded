package leaderboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

var (
	urlVar    = "SUPABASE_URL"
	keyVar    = "SUPABASE_KEY"
	keyHeader = "apikey"
)

type supabaseCreds struct {
	url *url.URL
	key string
}

func new() (*supabaseCreds, error) {
	c := &supabaseCreds{}
	urlStr, ok := os.LookupEnv(urlVar)
	if !ok {
		return nil, fmt.Errorf("missing %s", urlVar)
	}
	key, ok := os.LookupEnv(keyVar)
	if !ok {
		return nil, fmt.Errorf("missing %s", keyVar)
	}
	urlParsed, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("unparsable URL %q: %w", urlVar, err)
	}
	c.url = urlParsed.JoinPath("rest", "v1")
	c.key = key
	return c, nil
}

func (c *supabaseCreds) request(method string, path string) ([]map[string]interface{}, error) {
	client := &http.Client{}
	c.url = c.url.JoinPath(path)
	req, err := http.NewRequest(method, c.url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set(keyHeader, c.key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var result []map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}
