package freddie

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRoomGenesisLog_Results(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	closed := make(chan struct{})
	close(closed)
	for _, tc := range []struct {
		name string
		res  string
		done chan struct{}
		want string
	}{
		{"answered", "offer", make(chan struct{}), "result=answered"},
		{"timeout", "", make(chan struct{}), "result=timeout"},
		{"producer left", "", closed, "result=producer_left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			l := &roomGenesisLog{tickets: 1, rooms: 1, held: true, socket: true, started: time.Now()}
			await := l.wrap(func(<-chan struct{}) string { return tc.res })
			if got := await(tc.done); got != tc.res {
				t.Fatalf("wrapped await returned %q, want %q", got, tc.res)
			}
			line := logs.String()
			if strings.Count(line, `msg="Room genesis ended"`) != 1 || !strings.Contains(line, tc.want) || !strings.Contains(line, "held=true") {
				t.Errorf("logged %q, want one room genesis line with %s and held=true", line, tc.want)
			}
		})
	}
}
