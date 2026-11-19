package freddie

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// deliveryTokenTTL bounds how long a room genesis's delivery token can be redeemed. A
	// consumer offers within its patience of the delivery, and a genesis waits defaultMsgTTL for
	// its offer, so a minute outlasts any offer that can still reach its producer.
	deliveryTokenTTL = time.Minute
	// maxDeliveryTokens caps the store. Room consumers take tens of geneses a minute at most, so
	// a minute of them is far below it. When full, the oldest token is forgotten, and its offer,
	// if one still comes, goes unstamped: its session is pool, the safe kind.
	maxDeliveryTokens = 100000
)

// delivery is one room genesis delivered to one consumer: the genesis's request ID, the room
// (pairing ID) it went through, and the inviter ticket that admitted the consumer to that room,
// which is verified again when the consumer offers.
type delivery struct {
	genesisID string
	room      string
	ticket    string
}

type deliveryEntry struct {
	token string
	delivery
	expires time.Time
}

// deliveryTokens maps each room genesis delivery's single-use token to its delivery. Entries are
// kept in insertion order, which is also expiry order, so expired and evicted entries come off the
// front.
//
// Tokens, rooms and tickets are never logged, traced or counted.
type deliveryTokens struct {
	sync.Mutex
	m     map[string]*list.Element
	order *list.List // of deliveryEntry, oldest first
	now   func() time.Time
}

func newDeliveryTokens(now func() time.Time) *deliveryTokens {
	return &deliveryTokens{m: make(map[string]*list.Element), order: list.New(), now: now}
}

// issue records d under a new random token and returns the token, or "" if no randomness could be
// had, in which case the delivery goes without one.
func (s *deliveryTokens) issue(d delivery) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	token := hex.EncodeToString(b[:])
	s.Lock()
	defer s.Unlock()
	now := s.now()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		if s.order.Len() < maxDeliveryTokens && now.Before(e.Value.(deliveryEntry).expires) {
			break
		}
		s.remove(e)
	}
	s.m[token] = s.order.PushBack(deliveryEntry{token: token, delivery: d, expires: now.Add(deliveryTokenTTL)})
	return token
}

// take returns token's delivery and forgets it, so each token is redeemed once. ok is false for an
// unknown, used or expired token.
func (s *deliveryTokens) take(token string) (d delivery, ok bool) {
	if token == "" {
		return delivery{}, false
	}
	s.Lock()
	defer s.Unlock()
	e, found := s.m[token]
	if !found {
		return delivery{}, false
	}
	s.remove(e)
	entry := e.Value.(deliveryEntry)
	if !s.now().Before(entry.expires) {
		return delivery{}, false
	}
	return entry.delivery, true
}

// revoke forgets token, for a delivery that didn't happen.
func (s *deliveryTokens) revoke(token string) {
	s.Lock()
	defer s.Unlock()
	if e, ok := s.m[token]; ok {
		s.remove(e)
	}
}

func (s *deliveryTokens) remove(e *list.Element) {
	delete(s.m, e.Value.(deliveryEntry).token)
	s.order.Remove(e)
}
