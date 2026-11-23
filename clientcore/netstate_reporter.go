package clientcore

import (
	"context"
	"log/slog"
	"sync"

	netstatecl "github.com/getlantern/broflake/netstate/client"
)

// netstateReporter sends a volunteer's connected consumers to netstated off the forwarding path,
// so a slow or unresponsive netstated delays only its reports, never the traffic. Each report
// carries the whole set, so only the latest matters: one still waiting to go is replaced rather
// than queued behind it.
type netstateReporter struct {
	netstated string
	tag       string
	exec      func(netstated string, inst *netstatecl.Instruction) error

	mu      sync.Mutex
	latest  [][]string
	pending bool
	wake    chan struct{}
}

// newNetstateReporter starts a reporter that runs until ctx is done.
func newNetstateReporter(ctx context.Context, netstated, tag string) *netstateReporter {
	r := &netstateReporter{netstated: netstated, tag: tag, exec: netstatecl.Exec, wake: make(chan struct{}, 1)}
	go r.run(ctx)
	return r
}

// report records consumers as the set to send next, and returns without waiting for it to go.
func (r *netstateReporter) report(consumers [][]string) {
	r.mu.Lock()
	r.latest, r.pending = consumers, true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *netstateReporter) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		// With a report pending when ctx ends, select can pick either case, so a wake after
		// cancellation must not send another.
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		consumers, pending := r.latest, r.pending
		r.latest, r.pending = nil, false
		r.mu.Unlock()
		if !pending {
			continue
		}
		err := r.exec(r.netstated, &netstatecl.Instruction{
			Op:   netstatecl.OpConsumerState,
			Args: netstatecl.EncodeArgsOpConsumerState(consumers),
			Tag:  r.tag,
		})
		if err != nil {
			slog.Debug("Netstate client Exec error", "error", err)
		}
	}
}
