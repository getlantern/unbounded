package clientcore

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pion/transport/v3"
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
	Net               transport.Net
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
		HTTPClient:        &http.Client{},
		Patience:          500 * time.Millisecond,
		ErrorBackoff:      5 * time.Second,
		ConsumerSessionID: uuid.NewString(),
		Net:               nil,
	}
}

type EgressOptions struct {
	Addr           string
	Endpoint       string
	ConnectTimeout time.Duration
	ErrorBackoff   time.Duration
	PeerID         string
	Identity       *PeerIdentity
}

// SetIdentity sets the peer identity and updates PeerID to the identity's
// hex-encoded public key.
func (o *EgressOptions) SetIdentity(id *PeerIdentity) {
	o.Identity = id
	o.PeerID = id.PeerID()
}

func NewDefaultEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "ws://localhost:8000",
		Endpoint:       "/ws",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
		PeerID:         uuid.NewString(),
	}
}

// ConnectionChangeFunc is a callback for consumer connection state changes.
// state: 1 = connected, -1 = disconnected.
// When state == 1 (connected), addr is the IPv4 or IPv6 address of the new consumer.
// When state == -1 (disconnected), addr may be nil and should not be assumed to be non-nil.
type ConnectionChangeFunc func(state int, workerIdx int, addr net.IP)

type BroflakeOptions struct {
	ClientType             string
	CTableSize             int
	PTableSize             int
	BusBufferSz            int
	Netstated              string
	OnConnectionChangeFunc ConnectionChangeFunc
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
