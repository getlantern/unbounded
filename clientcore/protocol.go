// protocol.go provides primitives for defining client protocol behavior
package clientcore

import (
	"context"
	"fmt"
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
	fsm := WorkerFSM{
		com:   newIpcChan(workerBufferSz),
		state: states,
		wg:    wg,
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
		slog.Debug(fmt.Sprint("Starting WorkerFSM..."))
		fsm.ctx, fsm.cancel = context.WithCancel(context.Background())

		for {
			select {
			case <-fsm.ctx.Done():
				slog.Debug(fmt.Sprint("End of last state, stopping WorkerFSM..."))
				return
			default:
				fsm.currentState, fsm.nextInput = fsm.state[fsm.currentState](fsm.ctx, fsm.com, fsm.nextInput)
			}
		}
	}()
}

// Stop this WorkerFSM (takes effect upon returning from the currently executing state)
func (fsm *WorkerFSM) Stop() {
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
