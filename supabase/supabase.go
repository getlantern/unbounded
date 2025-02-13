package supabase

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/getlantern/broflake/common"
)

var (
	envURL       = "SUPABASE_URL"
	envKeyPublic = "SUPABASE_KEY_PUBLIC" // anon key
	keyHeader    = "apikey"
)

type Supabase struct {
	baseURL *url.URL // postgREST endpoint
	key     string   // authentication token
}

func New() (*Supabase, error) {
	c := &Supabase{}
	urlStr, ok := os.LookupEnv(envURL)
	if !ok {
		return nil, fmt.Errorf("missing %s", envURL)
	}
	key, ok := os.LookupEnv(envKeyPublic)
	if !ok {
		return nil, fmt.Errorf("missing %s", envKeyPublic)
	}
	urlParsed, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("unparsable URL %q: %w", envURL, err)
	}
	c.baseURL = urlParsed.JoinPath("rest", "v1")
	c.key = key
	return c, nil
}

func (c *Supabase) request(method string, path string, queryParams ...map[string]string) ([]map[string]interface{}, error) {
	client := &http.Client{}
	url := c.baseURL.JoinPath(path)
	q := url.Query()
	for _, queryParams := range queryParams {
		for k, v := range queryParams {
			q.Add(k, v)
		}
	}
	url.RawQuery = q.Encode()

	common.Debugf("%s %s", method, url.String())
	req, err := http.NewRequest(method, url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set(keyHeader, c.key)                 // TODO both necessary?
	req.Header.Set("Authorization", "Bearer "+c.key) // TODO both?
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

// Teams returns a list of teams (unstructured)
func (c *Supabase) Teams() ([]map[string]interface{}, error) {
	return c.request(http.MethodGet, "teams")
}

// Leaderboard returns a leaderboard (unstructured)
func (c *Supabase) Leaderboard() ([]map[string]interface{}, error) {
	return c.request(http.MethodGet, "leaderboard", map[string]string{"select": "name"})
}

func (c *Supabase) AddConnection() error {
	return fmt.Errorf("not implemented")
}

func (c *Supabase) AddTeam() error {
	return fmt.Errorf("not implemented")
}

func (c *Supabase) AddUser() error {
	return fmt.Errorf("not implemented")
}
