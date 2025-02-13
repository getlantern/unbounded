package supabase

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
)

var (
	log *slog.Logger

	urlVar = "SUPABASE_URL"
	keyVar = "SUPABASE_KEY"
	// keyVarPublic = "SUPABASE_KEY_PUBLIC" // anyone
	// keyVarAuth   = "SUPABASE_KEY_AUTH"   // logged in user
	// keyVarAdmin  = "SUPABASE_KEY_ADMIN"  // admin
	keyHeader = "apikey"
)

type supabaseCreds struct {
	baseURL *url.URL // postgREST endpoint
	key     string   // authentication token
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
	c.baseURL = urlParsed.JoinPath("rest", "v1")
	c.key = key
	return c, nil
}

func (c *supabaseCreds) request(method string, path string, queryParams ...map[string]string) ([]map[string]interface{}, error) {
	client := &http.Client{}
	url := c.baseURL.JoinPath(path)
	q := url.Query()
	for _, queryParams := range queryParams {
		for k, v := range queryParams {
			q.Add(k, v)
		}
	}
	url.RawQuery = q.Encode()

	log.Debug("requesting", "method", method, "url", url.String())
	req, err := http.NewRequest(method, url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set(keyHeader, c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("(http %d) read body: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("(http %d) %s", resp.StatusCode, string(body))
	}

	var result []map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}
