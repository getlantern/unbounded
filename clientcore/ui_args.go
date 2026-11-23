package clientcore

import "strings"

// The widget's JS setters convert their argument with jsArg (wasm only) to a
// bool, string, float64, []any of those, or nil for a missing, undefined or
// null argument or any other object, and map it with these functions, which
// run natively in tests.

// servePoolArg is setServePool's argument mapping. It fails closed: only a
// real boolean true serves the pool, so a mistyped call ("true", 1, nothing)
// keeps the widget in its pairings' rooms.
func servePoolArg(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// roomTicketsArg is setRoomTickets' argument mapping: a comma-separated
// string or an array of strings, each ticket trimmed and empty ones dropped.
// Anything else maps to no tickets, which clears every room on purpose: a
// widget given input it does not understand fails safe by leaving its rooms
// rather than keeping stale tickets.
func roomTicketsArg(v any) []string {
	var parts []string
	switch v := v.(type) {
	case string:
		parts = strings.Split(v, ",")
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	var tickets []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			tickets = append(tickets, p)
		}
	}
	return tickets
}
