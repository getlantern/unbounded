package supabase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/getlantern/broflake/common"
)

const (
	HeaderUserID = "X-Unbounded-UserID"
	HeaderTeamID = "X-Unbounded-TeamID"
)

var (
	envURL       = "SUPABASE_URL"
	envKeyPublic = "SUPABASE_KEY_SERVICE" // secret admin key
	keyHeader    = "apikey"               // required header along with Bearer
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

func (c *Supabase) request(method string, path string, queryParams map[string]string, data interface{}) ([]map[string]interface{}, error) {
	client := &http.Client{}
	url := c.baseURL.JoinPath(path)
	q := url.Query()
	for k, v := range queryParams {
		q.Add(k, v)
	}
	url.RawQuery = q.Encode()

	common.Debugf("%s %s", method, url.String())
	reqBody, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequest(method, url.String(), bytes.NewBuffer(reqBody))
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
		return nil, fmt.Errorf("[http %d] read body: %w", resp.StatusCode, err)
	}
	if !(resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated) {
		return nil, fmt.Errorf("[http %d] %s", resp.StatusCode, string(body))
	}
	if len(body) == 0 {
		return nil, nil
	}

	var result []map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

func (c *Supabase) Connections() ([]map[string]interface{}, error) {
	response, err := c.request(http.MethodGet, "connections", map[string]string{"select": "id"}, nil)
	if err != nil {
		return nil, err
	}
	if len(response) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return response, nil
}

// Leaderboard returns a leaderboard (unstructured)
func (c *Supabase) Leaderboard() ([]map[string]interface{}, error) {
	response, err := c.request(
		http.MethodGet,
		"leaderboard",
		map[string]string{"select": "name,people,connections"},
		nil,
	)
	if err != nil {
		return nil, err
	}
	if len(response) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return response, nil
}

func (c *Supabase) AddConnection(userID, teamID string) ([]map[string]interface{}, error) {
	common.Debugf("Adding connection")
	var data = map[string]string{
		"team_id": teamID,
		"user_id": userID,
	}
	return c.request(http.MethodPost, "connections", nil, data)
}

func (c *Supabase) AddTeam() error {
	return fmt.Errorf("not implemented")
}

func (c *Supabase) AddUser() error {
	return fmt.Errorf("not implemented")
}
