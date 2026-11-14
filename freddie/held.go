package freddie

import (
	"slices"
	"sync"
	"time"
)

// heldGenesisTTL bounds how long Freddie holds a rooms-only genesis that no room consumer was
// waiting for (see common.HoldField). A volunteer's room slot otherwise sends one every
// defaultMsgTTL just to be there when its inviter subscribes.
var heldGenesisTTL = 30 * time.Second

// heldRecheck is how often a subscribed room consumer looks for a genesis held for it, which it
// misses when one is held while it isn't receiving.
var heldRecheck = 500 * time.Millisecond

// heldGenesis is a rooms-only genesis Freddie holds for the next consumer to subscribe to one of
// its rooms.
type heldGenesis struct {
	id    string
	rooms []roomGenesis
	// seq orders held geneses across rooms, newest highest.
	seq uint64
	// taken is closed when a consumer takes the genesis, which then waits defaultMsgTTL for its
	// offer as a delivered genesis does.
	taken chan struct{}
}

// heldGeneses indexes held geneses by room, oldest first, so a subscribing room consumer can be
// handed one at once. Each is handed to one consumer only: the consumer's own workers subscribe
// separately, and a genesis several of them took could be answered by only one.
//
// Rooms are pairing IDs: never logged, traced or counted.
type heldGeneses struct {
	sync.Mutex
	byRoom map[string][]*heldGenesis
	seq    uint64
}

func newHeldGeneses() *heldGeneses {
	return &heldGeneses{byRoom: make(map[string][]*heldGenesis)}
}

// deliverOrHold runs deliver, which delivers g to the room consumers waiting now and reports
// whether one took it, and holds g if none did. Both happen under the lock that take runs under,
// so a consumer that subscribes meanwhile either gets g from deliver or takes it from the hold.
func (h *heldGeneses) deliverOrHold(g *heldGenesis, deliver func() bool) (delivered bool) {
	h.Lock()
	defer h.Unlock()
	if deliver() {
		return true
	}
	h.seq++
	g.seq = h.seq
	for _, rg := range g.rooms {
		h.byRoom[rg.room] = append(h.byRoom[rg.room], g)
	}
	return false
}

// take hands the subscribing consumer in rooms the newest genesis held for any of them, as its
// room's message, and stops holding it. ok is false when none is held. The newest is the likeliest
// to have a volunteer still waiting behind it: one that stopped without a word leaves its genesis
// held until heldGenesisTTL ends.
func (h *heldGeneses) take(rooms []string) (g *heldGenesis, msg string, ok bool) {
	h.Lock()
	defer h.Unlock()
	var takenRoom string
	for _, room := range rooms {
		held := h.byRoom[room]
		if len(held) == 0 {
			continue
		}
		if newest := held[len(held)-1]; g == nil || newest.seq > g.seq {
			g, takenRoom = newest, room
		}
	}
	if g == nil {
		return nil, "", false
	}
	h.removeLocked(g)
	close(g.taken)
	for _, rg := range g.rooms {
		if rg.room == takenRoom {
			msg = rg.msg
		}
	}
	return g, msg, true
}

// release stops holding g, if it is still held.
func (h *heldGeneses) release(g *heldGenesis) {
	h.Lock()
	defer h.Unlock()
	h.removeLocked(g)
}

func (h *heldGeneses) removeLocked(g *heldGenesis) {
	for _, rg := range g.rooms {
		held := slices.DeleteFunc(h.byRoom[rg.room], func(o *heldGenesis) bool { return o == g })
		if len(held) == 0 {
			delete(h.byRoom, rg.room)
		} else {
			h.byRoom[rg.room] = held
		}
	}
}

// size is the number of geneses held, for the gauge.
func (h *heldGeneses) size() int {
	h.Lock()
	defer h.Unlock()
	seen := make(map[*heldGenesis]struct{})
	for _, held := range h.byRoom {
		for _, g := range held {
			seen[g] = struct{}{}
		}
	}
	return len(seen)
}
