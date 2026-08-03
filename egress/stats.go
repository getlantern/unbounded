package egress

import (
	"sync"
	"sync/atomic"
)

// countryStats accumulates per-donor-country counters. Each live WebSocket holds
// a pointer to the entry for its own country, so the read path does one atomic
// add against memory it already has rather than taking a lock or hashing a map
// key per packet.
type countryStats struct {
	// clients is the number of currently open WebSockets from this country.
	clients int64
	// ingressBytes is bytes received from this country since the last otel
	// callback, which resets it — matching the pre-existing semantics of the
	// global nIngressBytes it replaces.
	ingressBytes int64
}

var (
	statsMx    sync.Mutex
	statsByCC  = map[string]*countryStats{}
	unknownCCs = &countryStats{}
)

// statsFor returns the counter block for a country, creating it on first use.
// Entries are never removed: the set of countries that have ever connected is
// small and bounded, and keeping them means a country that drops to zero
// clients keeps reporting 0 instead of vanishing from the series.
func statsFor(cc string) *countryStats {
	if cc == "" {
		return unknownCCs
	}
	statsMx.Lock()
	defer statsMx.Unlock()
	s, ok := statsByCC[cc]
	if !ok {
		s = &countryStats{}
		statsByCC[cc] = s
	}
	return s
}

// eachCountryStats calls f for every known country. It snapshots under the lock
// so the otel callback never holds statsMx while observing.
func eachCountryStats(f func(cc string, clients, ingressBytes int64)) {
	type row struct {
		cc string
		s  *countryStats
	}
	statsMx.Lock()
	rows := make([]row, 0, len(statsByCC)+1)
	for cc, s := range statsByCC {
		rows = append(rows, row{cc, s})
	}
	statsMx.Unlock()
	rows = append(rows, row{unknownCountry, unknownCCs})

	for _, r := range rows {
		clients := atomic.LoadInt64(&r.s.clients)
		// Swap rather than load-then-store: a concurrent read adding bytes
		// between the two would otherwise be silently discarded.
		bytes := atomic.SwapInt64(&r.s.ingressBytes, 0)
		// Skip countries that are entirely idle this interval so a long tail of
		// historical countries doesn't inflate the series count — but never skip
		// unknownCountry, so at least one series is always reported. Emitting
		// nothing at all would make "no donors connected" indistinguishable from
		// "the egress stopped reporting", which is precisely the outage we most
		// need to be able to see.
		if clients == 0 && bytes == 0 && r.cc != unknownCountry {
			continue
		}
		f(r.cc, clients, bytes)
	}
}
