package clientcore

import (
	"bufio"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

type WebRTCOptions struct {
	DiscoverySrv   string
	Endpoint       string
	GenesisAddr    string
	NATFailTimeout time.Duration
	ICEFailTimeout time.Duration
	STUNBatch      func(size uint32) (batch []string, err error)
	STUNBatchSize  uint32
	Tag            string
	HttpClient     *http.Client
	Patience       time.Duration
	ErrorBackoff   time.Duration
}

func NewDefaultWebRTCOptions() *WebRTCOptions {
	return &WebRTCOptions{
		DiscoverySrv:   "http://localhost:9000",
		Endpoint:       "/v1/signal",
		GenesisAddr:    "genesis",
		NATFailTimeout: 5 * time.Second,
		ICEFailTimeout: 5 * time.Second,
		STUNBatch:      DefaultSTUNBatchFunc,
		STUNBatchSize:  5,
		Tag:            "",
		HttpClient:     &http.Client{},
		Patience:       500 * time.Millisecond,
		ErrorBackoff:   5 * time.Second,
	}
}

type EgressOptions struct {
	Addr           string
	Endpoint       string
	ConnectTimeout time.Duration
	ErrorBackoff   time.Duration
	TeamMemberID   string // an optional team member ID recording donated connectivity
}

func NewDefaultEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "ws://localhost:8000",
		Endpoint:       "/ws",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
	}
}

type BroflakeOptions struct {
	ClientType  string
	CTableSize  int
	PTableSize  int
	BusBufferSz int
	Netstated   string
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

	candidates := []string{}
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		candidates = append(candidates, fmt.Sprintf("stun:%v", scanner.Text()))
	}

	if err := scanner.Err(); err != nil {
		return batch, err
	}

	rand.Seed(time.Now().Unix())

	for i := 0; i < int(size) && len(candidates) > 0; i++ {
		idx := rand.Intn(len(candidates))
		batch = append(batch, candidates[idx])
		candidates[idx] = candidates[len(candidates)-1]
		candidates = candidates[:len(candidates)-1]
	}

	return batch, err
}
