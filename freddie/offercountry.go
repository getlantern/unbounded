package freddie

import (
	"container/list"
	"sync"
	"time"
)

const (
	// offerCountryTTL bounds how long Freddie remembers a delivered offer's consumer country. A
	// failed attempt is reported on the producer's next genesis, within a minute, but a connected
	// one only once its session ends, so the TTL has to outlast a long session.
	offerCountryTTL = 24 * time.Hour
	// maxOfferCountries caps the cache. Freddie delivers about one offer per producer attempt,
	// tens a minute, so a day of them fits. When full, the oldest offer is forgotten and its
	// attempt goes unlabeled ("unknown").
	maxOfferCountries = 100000
)

type offerCountry struct {
	offerID string
	country string
	expires time.Time
}

// offerCountryCache maps the request ID of a delivered offer, which the producer sees as its
// ReplyTo and echoes back as common.PrevOfferIDField, to the consumer country Freddie derived
// for that request. It holds no addresses and nothing the producer didn't already have.
//
// Entries are kept in insertion order, which is also expiry order, so expired and evicted entries
// come off the front and every operation is O(1).
type offerCountryCache struct {
	sync.Mutex
	m     map[string]*list.Element
	order *list.List // of offerCountry, oldest first
	now   func() time.Time
}

func newOfferCountryCache(now func() time.Time) *offerCountryCache {
	return &offerCountryCache{m: make(map[string]*list.Element), order: list.New(), now: now}
}

func (c *offerCountryCache) add(offerID, country string) {
	c.Lock()
	defer c.Unlock()
	now := c.now()
	for e := c.order.Front(); e != nil; e = c.order.Front() {
		if c.order.Len() < maxOfferCountries && now.Before(e.Value.(offerCountry).expires) {
			break
		}
		c.remove(e)
	}
	if old, ok := c.m[offerID]; ok {
		c.remove(old)
	}
	c.m[offerID] = c.order.PushBack(offerCountry{offerID: offerID, country: country, expires: now.Add(offerCountryTTL)})
}

// take returns the country for offerID and forgets it, so each attempt is labeled once. An
// unknown or expired offerID returns "".
func (c *offerCountryCache) take(offerID string) string {
	c.Lock()
	defer c.Unlock()
	e, ok := c.m[offerID]
	if !ok {
		return ""
	}
	c.remove(e)
	oc := e.Value.(offerCountry)
	if !c.now().Before(oc.expires) {
		return ""
	}
	return oc.country
}

func (c *offerCountryCache) remove(e *list.Element) {
	delete(c.m, e.Value.(offerCountry).offerID)
	c.order.Remove(e)
}
