package freddie

import (
	"container/list"
	"sync"
	"time"
)

const (
	// roomGenesisTTL bounds how long Freddie remembers that a room consumer took a genesis. An
	// offer only reaches the producer while its genesis POST is still open, at most defaultMsgTTL
	// seconds after delivery; a later one gets a 404 and is never labeled. A minute is many times
	// that.
	roomGenesisTTL = time.Minute
	// maxRoomGeneses caps the set. Room consumers take a handful of geneses a day, so a minute of
	// them is far below it. When full, the oldest genesis is forgotten and an offer to it is
	// labeled pool.
	maxRoomGeneses = 10000
)

type roomGenesisEntry struct {
	genesisID string
	expires   time.Time
}

// roomGenesisSet holds the request IDs of genesis messages a consumer in one of the producer's
// rooms took, so an offer sent to one can be labeled as a room attempt. It holds only the genesis
// IDs, never the rooms.
//
// Entries are kept in insertion order, which is also expiry order, so expired and evicted entries
// come off the front and every operation is O(1).
type roomGenesisSet struct {
	sync.Mutex
	m     map[string]*list.Element
	order *list.List // of roomGenesisEntry, oldest first
	now   func() time.Time
}

func newRoomGenesisSet(now func() time.Time) *roomGenesisSet {
	return &roomGenesisSet{m: make(map[string]*list.Element), order: list.New(), now: now}
}

func (s *roomGenesisSet) add(genesisID string) {
	s.Lock()
	defer s.Unlock()
	now := s.now()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		if s.order.Len() < maxRoomGeneses && now.Before(e.Value.(roomGenesisEntry).expires) {
			break
		}
		s.remove(e)
	}
	if old, ok := s.m[genesisID]; ok {
		s.remove(old)
	}
	s.m[genesisID] = s.order.PushBack(roomGenesisEntry{genesisID: genesisID, expires: now.Add(roomGenesisTTL)})
}

// has reports whether a room consumer took genesisID within roomGenesisTTL. It doesn't forget the
// genesis: several room consumers may answer it, and only the first offer is delivered.
func (s *roomGenesisSet) has(genesisID string) bool {
	s.Lock()
	defer s.Unlock()
	e, ok := s.m[genesisID]
	return ok && s.now().Before(e.Value.(roomGenesisEntry).expires)
}

func (s *roomGenesisSet) remove(e *list.Element) {
	delete(s.m, e.Value.(roomGenesisEntry).genesisID)
	s.order.Remove(e)
}
