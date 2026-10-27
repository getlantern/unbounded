package freddie

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/helperticket"
)

// About these TTL values: Freddie models each segment of the signaling handshake as a request and
// a response. When Bob POSTs a signaling message to Freddie, Freddie delivers it to Alice, and then
// Freddie waits for Alice to POST a response. Here, "TTL" is the length of time Freddie will wait
// for Alice to send her response. In the past, Freddie was aggressively agnostic about the contents
// of each signaling message, but these days we find it helpful to break that agonisticism a bit, and
// for Freddie to tweak the TTL on a per-message basis to accommodate steps in the signaling
// handshake which require more or less time. For example, ICE gathering can take quite a long time,
// particularly under censored conditions, and so segments of the signaling handshake which await
// remote ICE candidates should accordingly wait a bit longer for a response.

// consumerTTL = how long to hold open HTTP requests for the genesis message stream?
// remoteICEGatheringTTL = TTL for segments awaiting remote ICE gathering
// defaultMsgTTL = TTL for all other segments

const (
	consumerTTL           = 20
	defaultMsgTTL         = 5
	remoteICEGatheringTTL = 15
)

var (
	consumerTable = newConsumerIndex(0)
	signalTable   = userTable{Data: make(map[string]chan string), BufferSz: 1}
)

// A userTable keeps state for requests and responses. Each channel in the map represents a request
// that's awaiting a response.
type userTable struct {
	Data     map[string]chan string
	BufferSz int
	sync.RWMutex
}

// Add a channel for a responder to respond over. Since we cannot guarantee that a responder will
// arrive, request handlers should eventually call Delete() to clean up that channel state.
func (t *userTable) Add(userID string) chan string {
	t.Lock()
	defer t.Unlock()
	t.Data[userID] = make(chan string, t.BufferSz)
	return t.Data[userID]
}

func (t *userTable) Delete(userID string) {
	t.Lock()
	defer t.Unlock()
	delete(t.Data, userID)
}

// Send a message with *exactly once* semantics by deleting the response channel from the table after
// sending over it. NB the implementation pattern is to create a race between the request handler
// and the response handler to idempotently delete the response channel state: if a responder arrives
// and uses SendOnce() to send a response over the chnanel, they will delete it, and if the responder
// fails to arrive, the request hanndler will delete it upon timeout.
func (t *userTable) SendOnce(userID string, msg string) bool {
	t.Lock()
	defer t.Unlock()
	userChan, ok := t.Data[userID]
	if ok {
		userChan <- msg
		delete(t.Data, userID)
	}
	return ok
}

// Send a message to every user in the table with *unreliable* delivery semantics. Broadcast can
// behave in subtly different ways depending on the userTable's BufferSz, but you probably want to
// use it with a BufferSz of 0.
func (t *userTable) Broadcast(msg string) {
	t.RLock()
	defer t.RUnlock()
	for _, userChan := range t.Data {
		select {
		case userChan <- msg:
			// Message delivered, do nothing
		default:
			// Message not delivered, do nothing
		}
	}
}

func (t *userTable) Size() int {
	t.RLock()
	defer t.RUnlock()
	return len(t.Data)
}

// Outcomes of a genesis broadcast, recorded as the "targeting" attribute on
// freddie.genesis.broadcasts. A fixed set, so the attribute's cardinality is bounded no matter
// which countries producers target.
const (
	genesisUntargeted = "untargeted" // producer expressed no country preference
	genesisMatched    = "matched"    // delivered only to waiting consumers from a target country
	genesisFallback   = "fallback"   // no waiting consumer from a target country took it, so sent to all
)

// spanTargeting is the genesis.targeting span attribute for a genesis message whose pool step had
// outcome, "" when it did not run (a room consumer took the message, or the producer serves only
// its rooms). Every genesis span carries the attribute, and one that skipped the pool step gets
// a value an ordinary genesis with the same targets also gets: untargeted without targets, matched
// with them. So the attribute never singles out a paired producer. The metric
// freddie.genesis.broadcasts is not affected: it counts only real pool steps.
func spanTargeting(outcome string, targets []string) string {
	switch {
	case outcome != "":
		return outcome
	case len(targets) == 0:
		return genesisUntargeted
	default:
		return genesisMatched
	}
}

// A consumerIndex is a userTable which also indexes its users by server-derived country, so that
// genesis messages can be steered toward consumers from the countries a producer wants to serve,
// and by helper pairing room, so that a producer's genesis messages reach the consumers it is
// paired with first. Users whose country is unknown, or who are in no room, are still in the
// table and still receive untargeted and fallback broadcasts.
//
// Rooms are opaque pairing IDs from verified tickets. They are never logged, traced or counted.
type consumerIndex struct {
	userTable
	country   map[string]string              // userID -> country, only for users whose country is known
	byCountry map[string]map[string]struct{} // country -> set of userIDs
	rooms     map[string][]string            // userID -> rooms, only for users in at least one
	byRoom    map[string]map[string]struct{} // room -> set of userIDs
}

func newConsumerIndex(bufferSz int) *consumerIndex {
	return &consumerIndex{
		userTable: userTable{Data: make(map[string]chan string), BufferSz: bufferSz},
		country:   make(map[string]string),
		byCountry: make(map[string]map[string]struct{}),
		rooms:     make(map[string][]string),
		byRoom:    make(map[string]map[string]struct{}),
	}
}

// AddWithCountry is AddConsumer in no room.
func (t *consumerIndex) AddWithCountry(userID, country string) chan string {
	return t.AddConsumer(userID, country, nil)
}

// AddConsumer is Add, plus indexing userID under country and under each of rooms. country must
// already be normalized; "" means the user's country is unknown and it is not indexed. rooms must
// come from verified tickets. The index keeps its own copy of rooms.
func (t *consumerIndex) AddConsumer(userID, country string, rooms []string) chan string {
	t.Lock()
	defer t.Unlock()
	t.Data[userID] = make(chan string, t.BufferSz)
	if country != "" {
		t.country[userID] = country
		users, ok := t.byCountry[country]
		if !ok {
			users = make(map[string]struct{})
			t.byCountry[country] = users
		}
		users[userID] = struct{}{}
	}
	if len(rooms) > 0 {
		rooms = slices.Clone(rooms)
		t.rooms[userID] = rooms
		for _, room := range rooms {
			users, ok := t.byRoom[room]
			if !ok {
				users = make(map[string]struct{})
				t.byRoom[room] = users
			}
			users[userID] = struct{}{}
		}
	}
	return t.Data[userID]
}

func (t *consumerIndex) Delete(userID string) {
	t.Lock()
	defer t.Unlock()
	delete(t.Data, userID)
	if country, ok := t.country[userID]; ok {
		delete(t.country, userID)
		delete(t.byCountry[country], userID)
		if len(t.byCountry[country]) == 0 {
			delete(t.byCountry, country)
		}
	}
	for _, room := range t.rooms[userID] {
		delete(t.byRoom[room], userID)
		if len(t.byRoom[room]) == 0 {
			delete(t.byRoom, room)
		}
	}
	delete(t.rooms, userID)
}

// broadcastToCountries sends msg, with Broadcast's unreliable delivery semantics, to every user
// indexed under one of countries. It returns the number of users the message was delivered to.
func (t *consumerIndex) broadcastToCountries(msg string, countries []string) int {
	t.RLock()
	defer t.RUnlock()
	delivered := 0
	for _, country := range countries {
		for userID := range t.byCountry[country] {
			select {
			case t.Data[userID] <- msg:
				delivered++
			default:
			}
		}
	}
	return delivered
}

// BroadcastGenesis broadcasts a genesis message, preferring consumers from targets. If any waiting
// consumer from a target country takes the message, nobody else gets it; otherwise it falls back to
// a plain Broadcast to everyone. Targeting is a preference, not a filter: a producer who targets a
// country with no waiting consumers must keep receiving offers rather than sit idle. Returns which
// of the genesis* outcomes occurred.
func (t *consumerIndex) BroadcastGenesis(msg string, targets []string) string {
	if len(targets) == 0 {
		t.Broadcast(msg)
		return genesisUntargeted
	}
	if t.broadcastToCountries(msg, targets) > 0 {
		return genesisMatched
	}
	t.Broadcast(msg)
	return genesisFallback
}

// broadcastToRooms sends msg, with Broadcast's unreliable delivery semantics, to every user indexed
// under one of rooms, at most once per user. It returns the number of users the message was
// delivered to.
func (t *consumerIndex) broadcastToRooms(msg string, rooms []string) int {
	t.RLock()
	defer t.RUnlock()
	delivered := 0
	seen := make(map[string]struct{})
	for _, room := range rooms {
		for userID := range t.byRoom[room] {
			if _, dup := seen[userID]; dup {
				continue
			}
			seen[userID] = struct{}{}
			select {
			case t.Data[userID] <- msg:
				delivered++
			default:
			}
		}
	}
	return delivered
}

// DeliverGenesis delivers a genesis message from a producer in rooms. A waiting consumer in one of
// rooms comes first: if any takes roomMsg, nobody else gets the genesis. Otherwise, if servePool, it
// goes on to BroadcastGenesis(msg, targets), the country-then-everyone step. Without servePool it
// goes nowhere: the producer waits out its TTL and its slot loops, so a waiting room consumer gets
// the next free slot of a producer before any pool consumer does. roomMatched reports whether a
// room consumer took it; outcome is BroadcastGenesis's, or "" when the pool step did not run.
//
// The room step and the pool step take the read lock separately, on purpose. Precedence is per
// message, not atomic across the table: a consumer that joins between the two steps may get the
// message from either. Holding the read lock across both would mean nesting read locks, which
// deadlocks once a writer is waiting between them.
func (t *consumerIndex) DeliverGenesis(msg, roomMsg string, rooms []string, servePool bool, targets []string) (roomMatched bool, outcome string) {
	if len(rooms) > 0 && t.broadcastToRooms(roomMsg, rooms) > 0 {
		return true, ""
	}
	if !servePool {
		return false, ""
	}
	return false, t.BroadcastGenesis(msg, targets)
}

type Freddie struct {
	TLSConfig *tls.Config

	// ConsumerCountry, if non-nil, returns the ISO 3166-1 alpha-2 country of the consumer making
	// r, a GET /v1/signal, or "" when it is unknown. Freddie indexes the consumer under the
	// (normalized) result so producers who target that country can prefer it. It must derive the
	// country server-side and must not trust client-controlled input: a consumer who could claim
	// a country would absorb every genesis message targeted at it. nil, or a "" result, leaves the
	// consumer untargeted: it still receives untargeted and fallback broadcasts, but is never
	// selected by country. Set it after New and before ListenAndServe or ListenAndServeTLS; it
	// is read without synchronization by concurrent request handlers.
	//
	// Freddie also calls it on each offer POST, so freddie.producer.outcomes can say
	// which country the consumer behind each attempt was in.
	ConsumerCountry func(r *http.Request) string

	// RoomTicket, if non-nil, verifies a helper pairing ticket presented for role and returns the
	// room it admits to, the pairing's opaque ID, or ok=false. A consumer's inviter tickets, in
	// common.RoomTicketsHeader, put it in their rooms; a producer's volunteer tickets, in
	// common.RoomTicketField, send its genesis messages to those rooms' consumers first. It must
	// check the signature, the key, the expiry and the role, and must not log, trace or count the
	// ticket or the room. helperticket.RoomFunc is one. nil disables rooms: tickets are ignored, so
	// a producer that serves only its rooms is offered to nobody. Set it after New and before
	// serving; it is read without synchronization by concurrent request handlers.
	//
	// Freddie caches each ticket it accepts until the ticket's own expiry, so RoomTicket runs once
	// per distinct valid ticket rather than at every subscribe and genesis. See KnownKeyID.
	RoomTicket func(ticket string, role helperticket.Role) (room string, ok bool)

	// KnownKeyID reports whether kid names a key RoomTicket currently trusts. Freddie asks it on
	// every cache hit, and a ticket whose key is no longer trusted is dropped and refused, so a
	// key that leaves RoomTicket's set stops admitting anyone at once. nil disables the cache:
	// every ticket is verified by RoomTicket every time. helperticket.KnownKeyFunc is one. Set it
	// with RoomTicket, after New and before serving.
	KnownKeyID func(kid string) bool

	ctx context.Context
	srv *http.Server

	currentGets  atomic.Int64
	currentPosts atomic.Int64

	tracer            trace.Tracer
	meter             metric.Meter
	nConcurrentReqs   metric.Int64ObservableUpDownCounter
	totalRequests     metric.Int64Counter
	signalReplies     metric.Int64Counter
	consumerTableSize metric.Int64ObservableUpDownCounter
	signalTableSize   metric.Int64ObservableUpDownCounter
	genesisBroadcasts metric.Int64Counter
	consumerSubs      metric.Int64Counter
	roomMatches       metric.Int64Counter
	roomsDisabled     metric.Int64Counter
	iceNAT            metric.Int64Counter
	producerOutcomes  metric.Int64Counter

	tickets        *ticketCache
	offerConsumers *offerConsumerCache
}

func New(ctx context.Context, listenAddr string) (*Freddie, error) {
	mux := http.NewServeMux()

	f := Freddie{
		ctx: ctx,
		srv: &http.Server{
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			Addr:         listenAddr,
			Handler:      mux,
		},
		currentGets:    atomic.Int64{},
		currentPosts:   atomic.Int64{},
		tracer:         otel.Tracer("github.com/getlantern/broflake/freddie"),
		meter:          otel.Meter("github.com/getlantern/broflake/freddie"),
		tickets:        newTicketCache(maxCachedTickets, time.Now),
		offerConsumers: newOfferConsumerCache(time.Now),
	}

	var err error

	f.nConcurrentReqs, err = f.meter.Int64ObservableUpDownCounter("freddie.requests.concurrent",
		metric.WithDescription("concurrent requests"),
		metric.WithUnit("request"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			attrs := metric.WithAttributes(attribute.String("method", "GET"))
			m.Observe(f.currentGets.Load(), attrs)

			attrs = metric.WithAttributes(attribute.String("method", "POST"))
			m.Observe(f.currentPosts.Load(), attrs)
			return nil
		}))
	if err != nil {
		return nil, err
	}

	f.totalRequests, err = f.meter.Int64Counter("freddie.requests",
		metric.WithDescription("total requests"),
		metric.WithUnit("request"))
	if err != nil {
		return nil, err
	}

	f.signalReplies, err = f.meter.Int64Counter("freddie.signal.replies",
		metric.WithDescription("signaling replies, by message type and whether the reply was delivered"),
		metric.WithUnit("reply"))
	if err != nil {
		return nil, err
	}

	f.consumerTableSize, err = f.meter.Int64ObservableUpDownCounter("freddie.consumertable.size",
		metric.WithDescription("total number of users in the consumers table"),
		metric.WithUnit("user"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			m.Observe(int64(consumerTable.Size()))
			return nil
		}))
	if err != nil {
		return nil, err
	}

	f.signalTableSize, err = f.meter.Int64ObservableUpDownCounter("freddie.signaltable.size",
		metric.WithDescription("total number of users in the signal table"),
		metric.WithUnit("user"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			m.Observe(int64(signalTable.Size()))
			return nil
		}))
	if err != nil {
		return nil, err
	}

	f.genesisBroadcasts, err = f.meter.Int64Counter("freddie.genesis.broadcasts",
		metric.WithDescription("genesis messages broadcast, by country targeting outcome"),
		metric.WithUnit("message"))
	if err != nil {
		return nil, err
	}

	f.consumerSubs, err = f.meter.Int64Counter("freddie.consumer.subscribes",
		metric.WithDescription("consumers subscribed to the genesis stream, by whether their country is known"),
		metric.WithUnit("subscription"))
	if err != nil {
		return nil, err
	}

	f.roomMatches, err = f.meter.Int64Counter("freddie.genesis.room_match",
		metric.WithDescription("genesis messages from producers with valid room tickets, by whether a consumer in one of their rooms took it"),
		metric.WithUnit("message"))
	if err != nil {
		return nil, err
	}

	f.roomsDisabled, err = f.meter.Int64Counter("freddie.rooms.disabled",
		metric.WithDescription("signaling requests served with helper pairing rooms disabled, because no room ticket verifier is configured"),
		metric.WithUnit("request"))
	if err != nil {
		return nil, err
	}

	f.iceNAT, err = f.meter.Int64Counter("freddie.ice.nat",
		metric.WithDescription("ICE candidate sets received, by role, by how the peer's NAT mapped its sockets (see icenat.go), and by whether freddie delivered them"),
		metric.WithUnit("message"))
	if err != nil {
		return nil, err
	}

	f.producerOutcomes, err = f.meter.Int64Counter("freddie.producer.outcomes",
		metric.WithDescription("how producers' previous connection attempts ended, as reported on their next genesis message, with the ICE and peer connection states at that moment (see common/outcome.go)"),
		metric.WithUnit("attempt"))
	if err != nil {
		return nil, err
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "freddie (%v)\n", common.Version)
		fmt.Fprintf(w, "current GET requests: %d\n", f.currentGets.Load())
		fmt.Fprintf(w, "current POST requests: %d\n", f.currentPosts.Load())
	})
	mux.HandleFunc("/v1/signal", f.handleSignal)

	return &f, nil
}

func (f *Freddie) ListenAndServe() error {
	slog.Debug("Freddie listening on",
		"version", common.Version,
		"addr", f.srv.Addr,
		"consumer_ttl", consumerTTL,
		"remote_ice_gathering_ttl", remoteICEGatheringTTL,
		"default_msg_ttl", defaultMsgTTL)

	return f.srv.ListenAndServe()
}

func (f *Freddie) ListenAndServeTLS(certFile, keyFile string) error {
	f.srv.TLSConfig = f.TLSConfig
	slog.Debug("Freddie listening on (TLS)",
		"version", common.Version,
		"addr", f.srv.Addr,
		"consumer_ttl", consumerTTL,
		"remote_ice_gathering_ttl", remoteICEGatheringTTL,
		"default_msg_ttl", defaultMsgTTL)

	return f.srv.ListenAndServeTLS(certFile, keyFile)
}

func (f *Freddie) Shutdown() error {
	return f.srv.Shutdown(f.ctx)
}

func (f *Freddie) handleSignal(w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(r.Context(), "handleSignal")
	defer span.End()

	enableCors(w)

	// Handle preflight requests
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Credentials", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS,POST,PUT")
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Access-Control-Allow-Headers, Origin, Accept, X-Requested-With, Content-Type, "+
				"Access-Control-Request-Method, Access-Control-Request-Headers, "+common.VersionHeader+", "+
				common.RoomTicketsHeader,
		)

		w.WriteHeader(http.StatusOK)
		return
	}

	if !common.IsValidProtocolVersion(&r.Header) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		return
	}

	f.totalRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("method", r.Method)))
	if f.RoomTicket == nil {
		f.roomsDisabled.Add(ctx, 1)
	}

	switch r.Method {
	case http.MethodGet:
		f.handleSignalGet(ctx, w, r)
	case http.MethodPost:
		f.handleSignalPost(ctx, w, r)
	}
}

// verifyRoomTickets returns the distinct rooms that tickets, already normalized, admit a client in
// role to. Invalid tickets are dropped without a trace: neither they nor the rooms are logged,
// traced or counted, and every kind of failure looks the same.
func (f *Freddie) verifyRoomTickets(tickets []string, role helperticket.Role) []string {
	if f.RoomTicket == nil {
		return nil
	}
	var rooms []string
	for _, tk := range tickets {
		if room, ok := f.tickets.room(tk, role, f.RoomTicket, f.KnownKeyID); ok && room != "" && !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	return rooms
}

const (
	// maxCachedTickets bounds the verified ticket cache. An entry is under 200 bytes, and a
	// ticket lives 15 minutes, so this is room for far more pairings than one Freddie serves.
	maxCachedTickets = 1 << 16

	// ticketCacheSweep is how often the cache drops expired entries nobody has looked up again.
	ticketCacheSweep = time.Minute

	// ticketCacheFullSweep is the least time between sweeps of a full cache. A full cache of live
	// entries would otherwise be swept, under the lock, by every new ticket that misses it.
	ticketCacheFullSweep = time.Second
)

// A ticketCache remembers the tickets Freddie's verifier accepted, keyed by the ticket's SHA-256,
// until each ticket's own expiry. A consumer re-subscribes every consumerTTL with the same tickets,
// and each request can carry common.MaxRoomTickets of them, so without it every subscribe would
// cost that many signature checks. A ticket has exactly one accepted encoding, so its hash names
// it.
//
// Only accepted tickets are cached, so a client cannot fill the cache, or push good entries out
// of it, without tickets lantern-cloud signed. When the cache is full it sweeps out expired
// entries, at most once per ticketCacheFullSweep, and, failing that, leaves the new ticket
// uncached: it never evicts a live entry.
type ticketCache struct {
	mu        sync.Mutex
	entries   map[[sha256.Size]byte]cachedTicket
	max       int
	now       func() time.Time
	lastSweep time.Time
	sweeps    int // for tests
}

type cachedTicket struct {
	room    string
	role    helperticket.Role
	kid     string
	expires time.Time
}

func newTicketCache(max int, now func() time.Time) *ticketCache {
	return &ticketCache{
		entries:   make(map[[sha256.Size]byte]cachedTicket),
		max:       max,
		now:       now,
		lastSweep: now(),
	}
}

func ticketCacheKey(ticket string) [sha256.Size]byte {
	return sha256.Sum256([]byte(ticket))
}

// room returns the room ticket admits role to, from the cache while the ticket is unexpired and
// known still names its key, else from verify. A cached ticket presented for another role is
// refused without asking verify, and so is one whose key known no longer trusts: its entry is
// dropped, so it is verified afresh, against the current keys, if presented again. With a nil
// known nothing is cached.
func (c *ticketCache) room(ticket string, role helperticket.Role, verify func(string, helperticket.Role) (string, bool), known func(string) bool) (string, bool) {
	key := ticketCacheKey(ticket)
	now := c.now()
	if known != nil {
		c.mu.Lock()
		e, hit := c.entries[key]
		if hit && !now.Before(e.expires) {
			delete(c.entries, key)
			hit = false
		}
		c.mu.Unlock()
		if hit {
			if !known(e.kid) {
				c.drop(key)
				return "", false
			}
			if e.role != role {
				return "", false
			}
			return e.room, true
		}
	}

	room, ok := verify(ticket, role)
	if !ok {
		return "", false
	}
	// The verifier has accepted ticket, so what it claims is genuine.
	if claims, ok := helperticket.Claims(ticket); ok && known != nil && known(claims.KeyID) && now.Before(claims.Expires) {
		c.add(key, cachedTicket{room: room, role: role, kid: claims.KeyID, expires: claims.Expires}, now)
	}
	return room, true
}

func (c *ticketCache) drop(key [sha256.Size]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *ticketCache) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

func (c *ticketCache) add(key [sha256.Size]byte, e cachedTicket, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sinceSweep := now.Sub(c.lastSweep)
	if sinceSweep >= ticketCacheSweep || (len(c.entries) >= c.max && sinceSweep >= ticketCacheFullSweep) {
		for k, old := range c.entries {
			if !now.Before(old.expires) {
				delete(c.entries, k)
			}
		}
		c.lastSweep = now
		c.sweeps++
	}
	if len(c.entries) < c.max {
		c.entries[key] = e
	}
}

// FlushRoomTicketCache forgets every verified room ticket, so each is verified afresh the next
// time it is presented. Call it when the set of trusted ticket keys changes.
func (f *Freddie) FlushRoomTicketCache() {
	f.tickets.flush()
}

// GET /v1/signal is the producer advertisement stream
func (f *Freddie) handleSignalGet(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(ctx, "handleSignalGet")
	defer span.End()

	f.currentGets.Add(1)
	defer f.currentGets.Add(-1)

	consumerID := uuid.NewString()
	// Never taken from the request's own claims: see Freddie.ConsumerCountry.
	var country string
	if f.ConsumerCountry != nil {
		country = common.NormalizeCountry(f.ConsumerCountry(r))
	}
	f.consumerSubs.Add(ctx, 1, metric.WithAttributes(attribute.Bool("country_known", country != "")))
	span.SetAttributes(
		attribute.String("consumer.id", consumerID),
		attribute.String("consumer.country", country),
	)

	// Verified at every subscribe, so an ended pairing's consumer leaves its room within one
	// ticket lifetime plus one consumerTTL. Rooms are never put on the span.
	rooms := f.verifyRoomTickets(common.ParseRoomTicketsHeader(r.Header.Values(common.RoomTicketsHeader)), helperticket.RoleInviter)
	consumerChan := consumerTable.AddConsumer(consumerID, country, rooms)
	defer consumerTable.Delete(consumerID)

	// Matchmaking is country preference only, applied when genesis messages are broadcast: see
	// consumerIndex.BroadcastGenesis.
	w.WriteHeader(http.StatusOK)
	timeoutChan := time.After(consumerTTL * time.Second)

	for {
		select {
		case msg := <-consumerChan:
			fmt.Fprintf(w, "%v\n", msg)
			w.(http.Flusher).Flush()
		case <-timeoutChan:
			return
		case <-ctx.Done():
			// The consumer hung up. Leave the table now rather than at consumerTTL: a departed
			// consumer still accepts broadcasts, and under country targeting it would count as a
			// match and swallow genesis messages that should have fallen back to everyone else.
			return
		}
	}
}

// POST /v1/signal is how all signaling messaging is performed
func (f *Freddie) handleSignalPost(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(ctx, "handleSignalPost")
	defer span.End()

	f.currentPosts.Add(1)
	defer f.currentPosts.Add(-1)

	reqID := uuid.NewString()
	span.SetAttributes(attribute.String("request.id", reqID))

	reqChan := signalTable.Add(reqID)
	defer signalTable.Delete(reqID)

	if err := r.ParseForm(); err != nil {
		span.SetStatus(codes.Error, "failed to parse form")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}
	sendTo := r.Form.Get("send-to")
	data := r.Form.Get("data")
	msgType, err := strconv.ParseInt(r.Form.Get("type"), 10, 32)
	if err != nil {
		span.SetStatus(codes.Error, "invalid message type")
		span.RecordError(fmt.Errorf("invalid message type: %w", err))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	isGenesis := sendTo == common.SignalSendToGenesis || sendTo == common.SignalSendToGenesisRooms
	// A rooms-only genesis is traced as an ordinary one, so the span doesn't mark the producer as
	// paired.
	recipient := sendTo
	if isGenesis {
		recipient = common.SignalSendToGenesis
	}
	span.SetAttributes(
		attribute.String("recipient.id", recipient),
		attribute.String("msg_type", common.SignalMsgType(msgType).String()),
	)

	// Package the message
	msg, err := json.Marshal(
		common.SignalMsg{ReplyTo: reqID, Type: common.SignalMsgType(msgType), Payload: data},
	)
	if err != nil {
		// Malformed request
		span.SetStatus(codes.Error, "malformed request")
		span.RecordError(fmt.Errorf("malformed request: %w", err))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	if isGenesis {
		// It's a genesis message. Producers' rooms come first; then, unless the producer serves only
		// its rooms, its target countries; then everyone. See consumerIndex.DeliverGenesis. A
		// producer serves only its rooms when it posts to SignalSendToGenesisRooms, or, as the
		// first room-aware clients did, to SignalSendToGenesis with pool=0.
		f.recordProducerOutcome(ctx, span, r)
		rooms := f.verifyRoomTickets(common.NormalizeRoomTickets(r.Form[common.RoomTicketField]), helperticket.RoleVolunteer)
		servePool := sendTo == common.SignalSendToGenesis && r.Form.Get(common.PoolField) != "0"
		targets := common.NormalizeCountries(r.Form[common.TargetCountryField])
		var roomMsg []byte
		if len(rooms) > 0 {
			roomMsg, err = json.Marshal(common.SignalMsg{ReplyTo: reqID, Type: common.SignalMsgType(msgType), Payload: data, Room: true})
			if err != nil {
				span.SetStatus(codes.Error, "malformed request")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("400\n"))
				return
			}
		}
		matched, outcome := consumerTable.DeliverGenesis(string(msg), string(roomMsg), rooms, servePool, targets)
		if len(rooms) > 0 {
			f.roomMatches.Add(ctx, 1, metric.WithAttributes(attribute.Bool("matched", matched)))
		}
		if outcome != "" {
			f.genesisBroadcasts.Add(ctx, 1, metric.WithAttributes(attribute.String("targeting", outcome)))
		}
		span.SetAttributes(
			attribute.StringSlice("genesis.target_countries", targets),
			attribute.String("genesis.targeting", spanTargeting(outcome, targets)),
		)
	} else {
		// It's a regular message, so let's signal it to its recipient (or return a 404 if the
		// recipient is no longer available)
		// An offer's consumer is cached before the offer is published, so the producer can't
		// report the attempt before Freddie can label it.
		isOffer := common.SignalMsgType(msgType) == common.SignalMsgOffer
		if isOffer {
			oc := offerConsumer{
				version:      common.NormalizeConsumerVersion(r.Header.Get(common.VersionHeader), common.Version),
				volunteerNAT: outcomeNATNone,
				consumerNAT:  outcomeNATNone,
			}
			if f.ConsumerCountry != nil {
				oc.country = common.NormalizeCountry(f.ConsumerCountry(r))
			}
			f.offerConsumers.add(reqID, oc)
		}
		ok := signalTable.SendOnce(sendTo, string(msg))
		if isOffer && !ok {
			f.offerConsumers.take(reqID)
		}
		delivered := attribute.Bool("reply.delivered", ok)
		span.SetAttributes(delivered)
		f.signalReplies.Add(ctx, 1, metric.WithAttributes(
			delivered,
			attribute.String("msg_type", common.SignalMsgType(msgType).String()),
		))
		// A volunteer's answer is sent to the offer's ID, and the consumer's ICE message to the
		// answer's, so this links both peers' NATs to the offer the producer reports.
		nat := f.recordICENAT(ctx, span, common.SignalMsgType(msgType), data, delivered)
		switch common.SignalMsgType(msgType) {
		case common.SignalMsgAnswer:
			f.offerConsumers.answered(sendTo, reqID, nat)
		case common.SignalMsgICE:
			f.offerConsumers.consumerICE(sendTo, nat)
		}
		if !ok {
			// Only the first reply to a request is delivered, and none after its TTL,
			// so a late reply (e.g. an offer that lost the genesis race) is not an error.
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404\n"))
			return
		}
	}

	// Send 200 OK to indicate that signaling partner accepts the message, stream back their
	// response or a nil body if they failed to respond
	w.WriteHeader(http.StatusOK)
	span.SetStatus(codes.Ok, "")

	// XXX: below, Freddie becomes interested in the content of the signaling message so as to
	// implement optimizations, mostly related to synchronization and timing. This makes everything
	// less maintainable, so we dream of a world where Freddie can become agnostic again. That world
	// becomes real when we can move this logic to the client FSMs...
	var optimizedTTL time.Duration
	switch common.SignalMsgType(msgType) {
	case common.SignalMsgOffer:
		// Upon successfully delivering an offer, we wait for the remote peer to complete ICE gathering
		optimizedTTL = remoteICEGatheringTTL * time.Second
	case common.SignalMsgAnswer:
		// Upon successfully delivering an answer, we wait for the remote peer to complete ICE gathering
		optimizedTTL = remoteICEGatheringTTL * time.Second
	case common.SignalMsgICE:
		// There are no more steps in the signaling handshake, so just close the request immediately.
		// We previously implemented this short circuit behavior on the client side, but it required a
		// Flush() here to push the status header to the client. The Flush() confuses the browser and
		// breaks Golang context contracts in Wasm build targets, so we live with this for now.
		w.Write(nil)
		return
	default:
		optimizedTTL = defaultMsgTTL * time.Second
	}

	select {
	case res := <-reqChan:
		fmt.Fprintf(w, "%v\n", res)
	case <-time.After(optimizedTTL):
		span.AddEvent("timeout waiting for response")
		w.Write(nil)
	}
}

// TODO: delete me and replace with a real CORS strategy!
func enableCors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}
