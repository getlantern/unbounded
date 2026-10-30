package clientcore

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/transport/v5"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/broflake/common/covertdtls"
)

type WebRTCOptions struct {
	DiscoverySrv      string
	Endpoint          string
	GenesisAddr       string
	NATFailTimeout    time.Duration
	STUNBatch         func(size uint32) (batch []string, err error)
	STUNBatchSize     uint32
	Tag               string
	HTTPClient        *http.Client
	Patience          time.Duration
	ErrorBackoff      time.Duration
	ConsumerSessionID string
	// ConsumerCountry is an optional ISO-3166-1 alpha-2 country the consumer
	// discloses so the egress can attribute traffic to the region it is actually
	// serving. Consumer-side only, like ConsumerSessionID above.
	//
	// Empty is a valid and complete choice: it emits the same 3-element subprotocol
	// list as every release before this one, so a consumer that declines is
	// byte-identical on the wire to one that predates the field.
	//
	// Note this travels consumer -> donor -> egress, so the donor can read it. That
	// discloses nothing the donor does not already have: producer.go extracts the
	// consumer's public address from the remote ICE candidates and the widget
	// geolocates it for the UI globe. The marginal disclosure is to the *egress*,
	// which otherwise cannot know.
	//
	// A consumer also declares it to Freddie when subscribing to the genesis stream, so Freddie
	// can steer genesis messages from producers who target that country (see TargetCountries).
	ConsumerCountry string
	// TargetCountries optionally lists ISO-3166-1 alpha-2 countries whose consumers this
	// *producer* prefers to serve. Freddie delivers this producer's genesis messages only to
	// waiting consumers from these countries when there are any, and to everyone otherwise, so a
	// targeting producer is never left idle. Empty means no preference, which is byte-identical on
	// the wire to releases before this field existed. Producer-side only.
	//
	// Set it before NewBroflake; to change it on a running instance use SetTargetCountries, which
	// takes effect from each worker's next genesis message.
	TargetCountries []string
	// RoomTickets optionally lists helper pairing tickets (see
	// common/helperticket) for pairings this client belongs to. A producer
	// sends its volunteer tickets with every genesis message, and Freddie
	// offers that genesis to the consumers waiting in those pairings' rooms
	// before anyone else. A consumer sends its inviter tickets when it
	// subscribes, and Freddie puts it in those rooms. Freddie verifies every
	// ticket and ignores invalid ones. Empty is byte-identical on the wire to
	// releases before this field.
	//
	// Set it before NewBroflake; on a running instance use SetRoomTickets,
	// which takes effect from each worker's next genesis message or
	// subscribe. RoomTicketSource, when set, takes precedence.
	RoomTickets []string
	// RoomTicketSource, if non-nil, is called for the current tickets at each
	// genesis message and each subscribe instead of reading RoomTickets. It
	// runs on the signaling path, so it must be cheap and must not block. Set
	// it before NewBroflake.
	RoomTicketSource func() []string
	// RoomsOnly, on a producer, tells Freddie to offer its genesis messages
	// to the consumers in its rooms and nobody else. The zero value serves
	// everyone, as every release before rooms did, and sends nothing new on
	// the wire. A Freddie that predates rooms refuses a rooms-only
	// producer's genesis messages rather than broadcast them (see
	// common.SignalSendToGenesisRooms), so it is offered to nobody there.
	// On a running instance use SetServePool.
	RoomsOnly bool
	// ConsumerRoomsOnly, on a consumer, makes it answer only genesis messages
	// Freddie delivered through one of its rooms (see RoomTickets), so it
	// connects only through its own helpers and never through a stranger.
	// Pool genesis messages are ignored, and repeated failures with room
	// helpers never fall back to the pool. Without a room helper online the
	// consumer has no connection at all. The zero value answers everyone, as
	// every release before it did. It changes nothing on the wire. Set it
	// before NewBroflake.
	ConsumerRoomsOnly bool
	// 'Net' is currently only respected by the WebRTC *consumer*, and it won't work for Wasm builds!
	Net transport.Net
	// CovertDTLS configures DTLS ClientHello fingerprint-resistance on the
	// producer/widget side. See common/covertdtls and net4people/bbs#603
	// for background. The zero value disables the feature.
	CovertDTLS covertdtls.Config
	// Tuning overrides pion's ICE, DTLS and SCTP defaults on native builds.
	// The zero value keeps every default.
	Tuning WebRTCTuning
}

func NewDefaultWebRTCOptions() *WebRTCOptions {
	return &WebRTCOptions{
		DiscoverySrv:      "http://localhost:9000",
		Endpoint:          "/v1/signal",
		GenesisAddr:       "genesis",
		NATFailTimeout:    5 * time.Second,
		STUNBatch:         DefaultSTUNBatchFunc,
		STUNBatchSize:     5,
		Tag:               "",
		ConsumerCountry:   "",
		HTTPClient:        &http.Client{},
		Patience:          500 * time.Millisecond,
		ErrorBackoff:      5 * time.Second,
		ConsumerSessionID: uuid.NewString(),
		Net:               nil,
		// randomizemimic matches Snowflake v2.13.1's default and is the most
		// stable mode: each handshake picks a random real-browser fingerprint.
		CovertDTLS: covertdtls.Config{Randomize: true, Mimic: true},
	}
}

// targetCountriesMu guards WebRTCOptions.TargetCountries, which producer workers read on every
// genesis message while SetTargetCountries may write it. Package-level rather than a struct field
// so WebRTCOptions stays safe to copy.
var targetCountriesMu sync.RWMutex

// SetTargetCountries replaces TargetCountries on a live instance. Invalid codes are dropped. Each
// producer worker picks up the change with its next genesis message; established connections are
// unaffected.
func (o *WebRTCOptions) SetTargetCountries(countries []string) {
	normalized := common.NormalizeCountries(countries)
	targetCountriesMu.Lock()
	defer targetCountriesMu.Unlock()
	o.TargetCountries = normalized
}

func (o *WebRTCOptions) targetCountries() []string {
	targetCountriesMu.RLock()
	defer targetCountriesMu.RUnlock()
	return common.NormalizeCountries(o.TargetCountries)
}

// roomsMu guards RoomTickets and RoomsOnly, which workers read on every
// genesis message and subscribe while SetRoomTickets and SetServePool may
// write them. Package-level, like targetCountriesMu, so WebRTCOptions stays
// safe to copy.
var roomsMu sync.RWMutex

// SetRoomTickets replaces RoomTickets on a live instance. Malformed tickets
// are dropped. Tickets expire after 15 minutes, so the caller renews them;
// each worker picks up the change with its next genesis message or subscribe.
func (o *WebRTCOptions) SetRoomTickets(tickets []string) {
	normalized := common.NormalizeRoomTickets(tickets)
	roomsMu.Lock()
	defer roomsMu.Unlock()
	o.RoomTickets = normalized
}

// SetServePool sets whether a producer's genesis messages may go to consumers
// outside its rooms. Each producer worker picks up the change with its next
// genesis message; established connections are unaffected.
func (o *WebRTCOptions) SetServePool(serve bool) {
	roomsMu.Lock()
	defer roomsMu.Unlock()
	o.RoomsOnly = !serve
}

// CurrentRoomTickets returns the tickets the next genesis message or subscribe
// will carry: RoomTicketSource's if it is set, else RoomTickets.
func (o *WebRTCOptions) CurrentRoomTickets() []string {
	if src := o.RoomTicketSource; src != nil {
		return common.NormalizeRoomTickets(src())
	}
	roomsMu.RLock()
	defer roomsMu.RUnlock()
	return common.NormalizeRoomTickets(o.RoomTickets)
}

// ServesPool reports whether a producer's genesis messages may go to consumers
// outside its rooms.
func (o *WebRTCOptions) ServesPool() bool {
	roomsMu.RLock()
	defer roomsMu.RUnlock()
	return !o.RoomsOnly
}

type EgressOptions struct {
	Addr           string
	Endpoint       string
	ConnectTimeout time.Duration
	ErrorBackoff   time.Duration
}

func NewDefaultEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "ws://localhost:8000",
		Endpoint:       "/ws",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
	}
}

// ConnectionChangeFunc is a callback for consumer connection state changes.
// state: 1 = connected, -1 = disconnected.
// When state == 1 (connected), addr is the IPv4 or IPv6 address of the new consumer.
// When state == -1 (disconnected), addr may be nil and should not be assumed to be non-nil.
type ConnectionChangeFunc func(state int, workerIdx int, addr net.IP)

// ConnectionChangeInfoFunc is ConnectionChangeFunc with the whole ConsumerInfo
// in place of its address. state and workerIdx are as for ConnectionChangeFunc.
// When state == 1, info.Addr is the consumer's address, and info.Pairing is the
// helper pairing the consumer came through, or "" for a pool consumer or one
// whose pairing couldn't be attributed (see common.ConsumerInfo.Pairing). When
// state == -1, info is zero. info.Pairing must never be logged.
type ConnectionChangeInfoFunc func(state int, workerIdx int, info common.ConsumerInfo)

// ConsumerSlotChangeFunc is a callback for a consumer's own WebRTC connection
// slots. state: 1 = connected, -1 = disconnected. workerIdx is the slot.
// When state == 1, pairing is the helper pairing of the room genesis the slot
// answered, or "" for a pool producer, or when Freddie or the producer
// predates pairing attribution. When state == -1, pairing is "". It is called
// on the slot's own goroutine, so it must be cheap and must not block.
// pairing must never be logged.
type ConsumerSlotChangeFunc func(state int, workerIdx int, pairing string)

// ChunkFunc is a callback for data relayed for a consumer. size is in bytes; workerIdx is the
// consumer's connection slot, matching the workerIdx passed to ConnectionChangeFunc. Downstream
// callbacks fire once per chunk on the data path, so implementations must be cheap and
// non-blocking (e.g. an atomic add); upstream callbacks are batched per slot at uiRefreshHz.
type ChunkFunc func(size int, workerIdx int)

type BroflakeOptions struct {
	ClientType             string
	CTableSize             int
	PTableSize             int
	BusBufferSz            int
	Netstated              string
	OnConnectionChangeFunc ConnectionChangeFunc
	// OnConnectionChangeInfoFunc, if non-nil, is called with each producer
	// connection change, after OnConnectionChangeFunc, with the whole
	// ConsumerInfo, including its pairing.
	OnConnectionChangeInfoFunc ConnectionChangeInfoFunc
	// OnConsumerSlotChangeFunc, if non-nil, is called when one of a
	// consumer's (ClientType "desktop") WebRTC slots connects or disconnects.
	// See also BroflakeEngine.ConsumerSlots.
	OnConsumerSlotChangeFunc ConsumerSlotChangeFunc
	// OnDownstreamChunkFunc receives bytes relayed from the egress to a consumer
	OnDownstreamChunkFunc ChunkFunc
	// OnUpstreamChunkFunc receives bytes relayed from a consumer to the egress
	OnUpstreamChunkFunc ChunkFunc
}

func NewDefaultBroflakeOptions() *BroflakeOptions {
	return &BroflakeOptions{
		ClientType:  "desktop",
		CTableSize:  5,
		PTableSize:  5,
		BusBufferSz: 4096,
		Netstated:   "",
	}
}

func DefaultSTUNBatchFunc(size uint32) (batch []string, err error) {
	// Naive batch logic: at batch time, fetch a public list of servers and select N at random
	res, err := http.Get("https://raw.githubusercontent.com/pradt2/always-online-stun/master/valid_ipv4s.txt")
	if err != nil {
		return batch, err
	}
	defer res.Body.Close()

	candidates := []string{}
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		candidates = append(candidates, fmt.Sprintf("stun:%v", scanner.Text()))
	}

	if err := scanner.Err(); err != nil {
		return batch, err
	}

	for i := 0; i < int(size) && len(candidates) > 0; i++ {
		idx := rand.Intn(len(candidates))
		batch = append(batch, candidates[idx])
		candidates[idx] = candidates[len(candidates)-1]
		candidates = candidates[:len(candidates)-1]
	}

	return batch, err
}
