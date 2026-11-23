//go:build !js

package clientcore

import (
	"net/http"

	"github.com/coder/websocket"
)

func setEgressHTTPClient(opts *websocket.DialOptions, client *http.Client) {
	opts.HTTPClient = client
}
