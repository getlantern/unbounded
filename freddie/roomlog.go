package freddie

import (
	"context"
	"log/slog"
	"time"
)

// roomGenesisLog is what Freddie logs, once it ends, about a genesis posted with room tickets. It
// holds counts and outcomes only: no room, ticket or request ID, so a line can't be tied to a
// pairing. Pool geneses aren't logged: there are far too many, and the metrics count them.
type roomGenesisLog struct {
	tickets int
	refused int
	rooms   int
	matched bool
	held    bool
	pool    bool
	socket  bool
	started time.Time
}

// wrap returns await, logging how the genesis ended once it does.
func (l *roomGenesisLog) wrap(await func(done <-chan struct{}) string) func(done <-chan struct{}) string {
	return func(done <-chan struct{}) string {
		res := await(done)
		result := "answered"
		if res == "" {
			result = "timeout"
			select {
			case <-done:
				result = "producer_left"
			default:
			}
		}
		l.log(result)
		return res
	}
}

func (l *roomGenesisLog) log(result string) {
	level := slog.LevelInfo
	if l.refused > 0 {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "Room genesis ended",
		"tickets", l.tickets,
		"refused", l.refused,
		"rooms", l.rooms,
		"matched", l.matched,
		"held", l.held,
		"pool", l.pool,
		"socket", l.socket,
		"result", result,
		"waited_ms", time.Since(l.started).Milliseconds(),
	)
}

// logRefusedConsumerTickets warns when a subscribing consumer presented room tickets Freddie
// refused, as when a ticket expired or its key isn't trusted.
func logRefusedConsumerTickets(tickets, refused, rooms int) {
	if refused == 0 {
		return
	}
	slog.Warn("Room consumer tickets refused",
		"tickets", tickets,
		"refused", refused,
		"rooms", rooms,
	)
}
