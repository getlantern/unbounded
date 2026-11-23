package freddie

import (
	"container/list"
	"sync"
	"time"
)

const (
	// roomGenesisTTL bounds how long Freddie remembers that a room consumer took a genesis. The
	// attempt is labeled when the producer reports it, which for a connected attempt is only once
	// its session ends, so it outlasts offerConsumerTTL. The offer's TTL starts later than the
	// genesis's: when the offer arrives, which is while the genesis request waits for it, at most
	// defaultMsgTTL after the match. The extra minute covers that wait, so the room is remembered
	// for as long as any offer that answered the genesis is.
	roomGenesisTTL = offerConsumerTTL + time.Minute
	// maxRoomGeneses caps the set. Room consumers take tens of geneses a day, so a day of them is
	// far below it. When full, the oldest genesis is forgotten and its attempt is labeled pool.
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
