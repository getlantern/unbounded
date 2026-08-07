package clientcore

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pion/transport/v4"

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
	ConsumerCountry string
	// 'Net' is currently only respected by the WebRTC *consumer*, and it won't work for Wasm builds!
	Net transport.Net
	// CovertDTLS configures DTLS ClientHello fingerprint-resistance on the
	// producer/widget side. See common/covertdtls and net4people/bbs#603
	// for background. The zero value disables the feature.
	CovertDTLS covertdtls.Config
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
