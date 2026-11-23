package freddie

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/broflake/common/helperticket"
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
			// Like Freddie's awaits, this one records a hang-up in the branch that sees it.
			await := l.wrap(func(done <-chan struct{}) string {
				select {
				case <-done:
					l.producerLeft()
				default:
				}
				return tc.res
			})
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

// A producer that hangs up after its await timed out is still logged as a timeout.
func TestRoomGenesisLog_TimeoutThenHangUp(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	done := make(chan struct{})
	l := &roomGenesisLog{tickets: 1, started: time.Now()}
	l.wrap(func(<-chan struct{}) string {
		close(done)
		return ""
	})(done)
	if !strings.Contains(logs.String(), "result=timeout") {
		t.Errorf("logged %q, want result=timeout", logs.String())
	}
}

// With rooms off, every ticket is refused, so a misconfigured Freddie is visible in its logs.
func TestVerifyRoomTicketsBy_RoomsOffRefusesAll(t *testing.T) {
	f := &Freddie{}
	rooms, ticketOf, refused := f.verifyRoomTicketsBy([]string{"a", "b"}, helperticket.RoleInviter)
	if rooms != nil || ticketOf != nil || refused != 2 {
		t.Errorf("got rooms=%v ticketOf=%v refused=%d, want none admitted and 2 refused", rooms, ticketOf, refused)
	}
}

// syncBuffer is a bytes.Buffer safe to log into from Freddie's goroutines while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs sends slog's output to a buffer for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	logs := &syncBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	return logs
}
