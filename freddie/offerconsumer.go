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
// derived for the request, and the consumer's common.NormalizePionDTLS value.
type offerConsumer struct {
	country string
	dtls    string
}

type offerConsumerEntry struct {
	offerID string
	offerConsumer
	expires time.Time
}

// offerConsumerCache maps the request ID of a delivered offer, which the producer sees as its
// ReplyTo and echoes back as common.PrevOfferIDField, to the offerConsumer for that request. It
// holds no addresses and nothing the producer didn't already have.
//
// Entries are kept in insertion order, which is also expiry order, so expired and evicted entries
// come off the front and every operation is O(1).
type offerConsumerCache struct {
	sync.Mutex
	m     map[string]*list.Element
	order *list.List // of offerConsumerEntry, oldest first
	now   func() time.Time
}

func newOfferConsumerCache(now func() time.Time) *offerConsumerCache {
	return &offerConsumerCache{m: make(map[string]*list.Element), order: list.New(), now: now}
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
	c.m[offerID] = c.order.PushBack(offerConsumerEntry{offerID: offerID, offerConsumer: oc, expires: now.Add(offerConsumerTTL)})
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

func (c *offerConsumerCache) remove(e *list.Element) {
	delete(c.m, e.Value.(offerConsumerEntry).offerID)
	c.order.Remove(e)
}
