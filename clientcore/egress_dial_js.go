//go:build js

package clientcore

import (
	"net/http"

	"github.com/coder/websocket"
)

// In a browser the browser makes the WebSocket handshake, so there's no
// client to set.
func setEgressHTTPClient(*websocket.DialOptions, *http.Client) {}
