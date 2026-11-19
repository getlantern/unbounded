package freddie

import (
	"slices"
	"sync"
)

// genesisRooms remembers, for each room genesis waiting for its offer, the rooms (pairing IDs)
// Freddie verified from its producer's volunteer tickets, so the offer that answers it can be
// stamped with the room the consumer reached it through (see Freddie.offerPairing). An entry
// lives only as long as its genesis request: it is added before the genesis is delivered and
// dropped when the request ends, so the map is bounded by the genesis requests in flight.
//
// Rooms are pairing IDs: never logged, traced or counted.
type genesisRooms struct {
	sync.Mutex
	m map[string][]string
}

func newGenesisRooms() *genesisRooms {
	return &genesisRooms{m: make(map[string][]string)}
}

func (g *genesisRooms) set(genesisID string, rooms []string) {
	g.Lock()
	defer g.Unlock()
	g.m[genesisID] = slices.Clone(rooms)
}

func (g *genesisRooms) drop(genesisID string) {
	g.Lock()
	defer g.Unlock()
	delete(g.m, genesisID)
}

// has reports whether genesisID is a room genesis still waiting for its offer.
func (g *genesisRooms) has(genesisID string) bool {
	g.Lock()
	defer g.Unlock()
	_, ok := g.m[genesisID]
	return ok
}

// room returns the first of genesisID's rooms that is also one of consumerRooms, the order
// broadcastToRooms delivers in, or "" if they share none or genesisID is unknown.
func (g *genesisRooms) room(genesisID string, consumerRooms []string) string {
	g.Lock()
	defer g.Unlock()
	for _, room := range g.m[genesisID] {
		if slices.Contains(consumerRooms, room) {
			return room
		}
	}
	return ""
}
