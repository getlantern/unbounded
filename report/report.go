// package report provides a method for reporting Unbounded connections
package report

import (
	"fmt"
	"net/http"
)

var (
	EndpointURL  = "http://localhost"
	EndpointPort = 8888
	HeaderUserID = "X-Lantern-UserID"
	HeaderTeamID = "X-Lantern-TeamID"
	HeaderConnID = "X-Lantern-ConnID"
)

func Report(userID, connID, teamID string) error {
	req, err := http.NewRequest(http.MethodPost, EndpointURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set(HeaderUserID, userID)
	req.Header.Set(HeaderConnID, connID)
	req.Header.Set(HeaderTeamID, teamID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}
