package common

import (
	"encoding/json"
	"slices"
	"strings"
)

// Helper pairing rooms. A consumer and a producer that belong to the same
// helper pairing meet in a room keyed by the pairing's opaque ID. Each proves
// membership with a short-lived ticket lantern-cloud signs (see
// common/helperticket), which Freddie verifies on every request. Tickets ride
// outside the JSON signaling payload, like target countries, so Freddie stays
// payload-agnostic. Every field is optional, so an old Freddie ignores them and
// an old client never sends them.
const (
	// RoomTicketsHeader carries a consumer's inviter tickets, comma-separated,
	// on its GET of the genesis stream.
	RoomTicketsHeader = "X-BF-Room-Tickets"

	// RoomTicketField is the (repeatable) form field on a genesis POST
	// carrying one of the producer's volunteer tickets.
	RoomTicketField = "room-ticket"

	// PoolField on a genesis POST is "0" when the producer serves only its
	// rooms. Absent, or any other value, means it also serves everyone.
	PoolField = "pool"

	// MaxRoomTickets bounds how many tickets Freddie verifies per request.
	// It matches lantern-cloud's limit of 20 pairings per side.
	MaxRoomTickets = 20

	// MaxRoomTicketLen bounds one ticket. A real ticket is about 215
	// characters; anything longer is refused before it is decoded.
	MaxRoomTicketLen = 512
)

// NormalizeRoomTickets drops empty, oversized and malformed tickets (a ticket
// is base64url and '.') and duplicates, and truncates to MaxRoomTickets.
// Returns nil if nothing remains. It does not verify them: Freddie does.
func NormalizeRoomTickets(tickets []string) []string {
	var out []string
	for _, tk := range tickets {
		if len(out) == MaxRoomTickets {
			break
		}
		tk = strings.TrimSpace(tk)
		if !wellFormedRoomTicket(tk) || slices.Contains(out, tk) {
			continue
		}
		out = append(out, tk)
	}
	return out
}

// ParseRoomTicketsHeader splits every value of RoomTicketsHeader at commas
// and normalizes the result.
func ParseRoomTicketsHeader(values []string) []string {
	var all []string
	for _, v := range values {
		all = append(all, strings.Split(v, ",")...)
	}
	return NormalizeRoomTickets(all)
}

func wellFormedRoomTicket(tk string) bool {
	if tk == "" || len(tk) > MaxRoomTicketLen {
		return false
	}
	for i := 0; i < len(tk); i++ {
		switch c := tk[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// IsRoomGenesis reports whether raw, a signaling message from Freddie, is a
// genesis message Freddie delivered through one of the consumer's rooms.
func IsRoomGenesis(raw []byte) bool {
	var msg SignalMsg
	return json.Unmarshal(raw, &msg) == nil && msg.Type == SignalMsgGenesis && msg.Room
}
