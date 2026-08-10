// protocol.go provides primitives for defining client protocol behavior
package clientcore

import (
	"context"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"sync"
)

const (
	workerBufferSz = 4096
)

// WorkerFSM implements a Mealy machine: https://en.wikipedia.org/wiki/Mealy_machine
// A WorkerFSM independently manages the lifetime of a single connection slot. A client maintains
// two pools of WorkerFSMs - one for upstream channels and one for downstream channels.
type WorkerFSM struct {
	com          *ipcChan
	currentState int
	nextInput    []interface{}
	state        []FSMstate
	ctx          context.Context
	cancel       context.CancelFunc
	wg           *sync.WaitGroup
}

// Construct a new WorkerFSM
func NewWorkerFSM(wg *sync.WaitGroup, states []FSMstate) *WorkerFSM {
	// ctx/cancel are initialized here, not at the top of Start()'s goroutine, so a
	// Stop() that beats the goroutine still cancels instead of reading a nil
	// cancel and no-op'ing (or panicking). Same reasoning as NewQUICLayer — do not
	// move this write back into the goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	fsm := WorkerFSM{
		com:    newIpcChan(workerBufferSz),
		state:  states,
		wg:     wg,
		ctx:    ctx,
		cancel: cancel,
	}

	return &fsm
}

// Start this WorkerFSM
func (fsm *WorkerFSM) Start() {
	if fsm.wg != nil {
		fsm.wg.Add(1)
	}
	go func() {
		defer func() {
			if fsm.wg != nil {
				fsm.wg.Done()
			}
		}()
		slog.Debug("Starting WorkerFSM...")

		for {
			select {
			case <-fsm.ctx.Done():
				closeWorkerResource(fsm.nextInput)
				slog.Debug("End of last state, stopping WorkerFSM...")
				return
			default:
				fsm.currentState, fsm.nextInput = fsm.state[fsm.currentState](fsm.ctx, fsm.com, fsm.nextInput)
			}
		}
	}()
}

func closeWorkerResource(input []any) {
	if len(input) == 0 {
		return
	}

	switch c := input[0].(type) {
	case io.Closer:
		_ = c.Close()
	case interface{ CloseNow() error }:
		_ = c.CloseNow()
	}
}

// sendCtx sends msg on ch, abandoning the send if ctx is cancelled first, and
// reports whether the send happened.
//
// Control-plane messages must go through this rather than a bare `ch <- msg`. A
// bare send on a full buffer whose drain has already been torn down never returns,
// so the state never returns, the FSM never reaches its ctx.Done() check, and its
// wg.Done() never runs — which blocks BroflakeEngine.stop() and strands the whole
// stack. Data-plane sends are deliberately different: they use a non-blocking send
// with a default case, dropping chunks rather than waiting.
//
// On false the caller must return without clearing its input, so the FSM's exit
// path can still close any resource that input carries.
func sendCtx(ctx context.Context, ch chan<- IPCMsg, msg IPCMsg) bool {
	select {
	case ch <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop this WorkerFSM (takes effect upon returning from the currently executing state)
func (fsm *WorkerFSM) Stop() {
	// Guard the struct-literal case: a WorkerFSM not built via NewWorkerFSM has a
	// nil cancel, and calling it panics.
	if fsm.cancel == nil {
		return
	}
	fsm.cancel()
}

// FSMstate encapsulates logic for one state in a WorkerFSM. An FSMstate must return an int
// corresponding to the next state and a list of any inputs to propagate to the next state.
// TODO: a state's number simply corresponds to its index in WorkerFSM.state, but we perform no
// sanity checking of state indices.
type FSMstate func(ctx context.Context, com *ipcChan, input []interface{}) (int, []interface{})

// STUNCache implements the operations which support our strategy for evading STUN server blocking
// in-country. That is: populate the cache with the largest set of currently known STUN servers and
// shuffle it; select a cohort of the first N servers in the list to use in parallel; if any of
// those servers work, continue using that cohort; if all of those servers fail, delete the cohort
// from the list; when the list is empty, repeat the steps.
type STUNCache struct {
	data []string
	n    float64
}

func newSTUNCache(srvs []string, n float64) STUNCache {
	dest := make([]string, len(srvs))
	copy(dest, srvs)

	rand.Shuffle(len(dest), func(i, j int) {
		dest[i], dest[j] = dest[j], dest[i]
	})

	return STUNCache{data: dest, n: n}
}

// Return the size of the STUNCache
func (s *STUNCache) size() int {
	return len(s.data)
}

// Get the current cohort of servers in the STUNCache
func (s *STUNCache) cohort() []string {
	return s.data[:int(math.Min(s.n, float64(len(s.data))))]
}

// Delete the current cohort of servers from the STUNCache
func (s *STUNCache) drop() {
	s.data = s.data[int(math.Min(s.n, float64(len(s.data)))):]
}
