package freddie

import (
	"container/list"
	"sync"
	"time"
)

const (
	// offerConsumerTTL bounds how long Freddie remembers a delivered offer's consumer. A
	// failed attempt is reported on the producer's next genesis, within a minute, but a connected
	// one only once its session ends, so the TTL has to outlast a long session.
	offerConsumerTTL = 24 * time.Hour
	// maxOfferConsumers caps the cache. Freddie delivers about one offer per producer attempt,
	// tens a minute, so a day of them fits. When full, the oldest offer is forgotten and its
	// attempt goes unlabeled ("unknown").
	maxOfferConsumers = 100000
)

// offerConsumer is what Freddie labels a delivered offer's attempt with: the consumer country it
// derived for the request, the consumer's common.NormalizeConsumerVersion value, the
// outcomeLabel of each peer's NAT, outcomeNATNone until Freddie relays that peer's candidates, and
// the request ID of the genesis the offer answered.
type offerConsumer struct {
	country      string
	version      string
	volunteerNAT string
	consumerNAT  string
	genesisID    string
}

type offerConsumerEntry struct {
	offerID string
	// answerID is the request ID of the volunteer's answer, which the consumer sends its ICE
	// message to.
	answerID string
	offerConsumer
	expires time.Time
	// offered, answered and iced are when Freddie relayed the offer, the volunteer's answer and
	// the consumer's first ICE message, for the handshake's phase durations.
	offered, answered, iced time.Time
}

// offerConsumerCache maps the request ID of a delivered offer, which the producer sees as its
// ReplyTo and echoes back as common.PrevOfferIDField, to the offerConsumer for that request. It
// holds no addresses and nothing the producer didn't already have. answers maps each answered
// offer's answerID back to its offerID.
//
// Entries are kept in insertion order, which is also expiry order, so expired and evicted entries
// come off the front and every operation is O(1).
type offerConsumerCache struct {
	sync.Mutex
	m       map[string]*list.Element
	answers map[string]string
	order   *list.List // of offerConsumerEntry, oldest first
	now     func() time.Time
}

func newOfferConsumerCache(now func() time.Time) *offerConsumerCache {
	return &offerConsumerCache{m: make(map[string]*list.Element), answers: make(map[string]string), order: list.New(), now: now}
}

func (c *offerConsumerCache) add(offerID string, oc offerConsumer) {
	c.Lock()
	defer c.Unlock()
	now := c.now()
	for e := c.order.Front(); e != nil; e = c.order.Front() {
		if c.order.Len() < maxOfferConsumers && now.Before(e.Value.(offerConsumerEntry).expires) {
			break
		}
		c.remove(e)
	}
	if old, ok := c.m[offerID]; ok {
		c.remove(old)
	}
	c.m[offerID] = c.order.PushBack(offerConsumerEntry{offerID: offerID, offerConsumer: oc, expires: now.Add(offerConsumerTTL), offered: now})
}

// take returns the offerConsumer for offerID and forgets it, so each attempt is labeled once. An
// unknown or expired offerID returns the zero offerConsumer.
func (c *offerConsumerCache) take(offerID string) offerConsumer {
	c.Lock()
	defer c.Unlock()
	e, ok := c.m[offerID]
	if !ok {
		return offerConsumer{}
	}
	c.remove(e)
	entry := e.Value.(offerConsumerEntry)
	if !c.now().Before(entry.expires) {
		return offerConsumer{}
	}
	return entry.offerConsumer
}

// answered records the volunteer's answer to offerID: the answer's request ID, and the
// volunteer's NAT label, which "" leaves unset. The first answer wins: an offer's request takes
// only its first reply, so a later answer is one the consumer never received. For the first
// answer it returns how long after the offer it came, and the offer's genesis ID.
func (c *offerConsumerCache) answered(offerID, answerID, nat string) (took time.Duration, genesisID string, ok bool) {
	c.Lock()
	defer c.Unlock()
	e, found := c.m[offerID]
	if !found {
		return 0, "", false
	}
	entry := e.Value.(offerConsumerEntry)
	if entry.answerID != "" {
		return 0, "", false
	}
	entry.answerID = answerID
	c.answers[answerID] = offerID
	if nat != "" {
		entry.volunteerNAT = nat
	}
	entry.answered = c.now()
	e.Value = entry
	return entry.answered.Sub(entry.offered), entry.genesisID, true
}

// consumerICE records the consumer's NAT label, which "" leaves unset, for the offer answered by
// answerID. For the consumer's first ICE message it returns how long after the answer it came,
// and the offer's genesis ID.
func (c *offerConsumerCache) consumerICE(answerID, nat string) (took time.Duration, genesisID string, ok bool) {
	c.Lock()
	defer c.Unlock()
	offerID, found := c.answers[answerID]
	if !found {
		return 0, "", false
	}
	e := c.m[offerID]
	entry := e.Value.(offerConsumerEntry)
	if nat != "" {
		entry.consumerNAT = nat
	}
	first := entry.iced.IsZero()
	if first {
		entry.iced = c.now()
	}
	e.Value = entry
	if !first {
		return 0, "", false
	}
	return entry.iced.Sub(entry.answered), entry.genesisID, true
}

func (c *offerConsumerCache) remove(e *list.Element) {
	entry := e.Value.(offerConsumerEntry)
	delete(c.m, entry.offerID)
	if entry.answerID != "" {
		delete(c.answers, entry.answerID)
	}
	c.order.Remove(e)
}
