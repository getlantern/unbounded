package freddie

import (
	"sync"
	"time"
)

const (
	// offerCountryTTL bounds how long Freddie remembers a delivered offer's consumer country. A
	// producer reports the attempt on its next genesis, which follows the offer by its ICE wait
	// and NATFailTimeout, well under a minute.
	offerCountryTTL = 5 * time.Minute
	// maxOfferCountries caps the cache. Freddie delivers about one offer per producer attempt, so
	// the cap is never reached in practice; past it, new offers go unlabeled ("unknown").
	maxOfferCountries = 100000
)

type offerCountry struct {
	country string
	expires time.Time
}

// offerCountryCache maps the request ID of a delivered offer, which the producer sees as its
// ReplyTo and echoes back as common.PrevOfferIDField, to the consumer country Freddie derived
// for that request. It holds no addresses and nothing the producer didn't already have.
type offerCountryCache struct {
	sync.Mutex
	m   map[string]offerCountry
	now func() time.Time
}

func newOfferCountryCache(now func() time.Time) *offerCountryCache {
	return &offerCountryCache{m: make(map[string]offerCountry), now: now}
}

func (c *offerCountryCache) add(offerID, country string) {
	c.Lock()
	defer c.Unlock()
	now := c.now()
	if len(c.m) >= maxOfferCountries {
		for id, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, id)
			}
		}
		if len(c.m) >= maxOfferCountries {
			return
		}
	}
	c.m[offerID] = offerCountry{country: country, expires: now.Add(offerCountryTTL)}
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
	delete(c.m, offerID)
	if !c.now().Before(e.expires) {
		return ""
	}
	return e.country
}
