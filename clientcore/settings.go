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
	CACert         []byte
}

const caCert = `-----BEGIN CERTIFICATE-----
MIIDoDCCAogCCQCIDNrnudYmTzANBgkqhkiG9w0BAQsFADCBkTELMAkGA1UEBhMC
VVMxEzARBgNVBAgMCkNhbGlmb3JuaWExFDASBgNVBAcMC0xvcyBBbmdlbGVzMQww
CgYDVQQKDANCTlMxEjAQBgNVBAsMCU1hcmtldGluZzEXMBUGA1UEAwwObG9jYWxo
b3N0OjgwMDAxHDAaBgkqhkiG9w0BCQEWDXRlc3RAdGVzdC5jb20wHhcNMjMwNzEw
MTgwODI5WhcNMjQwNzA5MTgwODI5WjCBkTELMAkGA1UEBhMCVVMxEzARBgNVBAgM
CkNhbGlmb3JuaWExFDASBgNVBAcMC0xvcyBBbmdlbGVzMQwwCgYDVQQKDANCTlMx
EjAQBgNVBAsMCU1hcmtldGluZzEXMBUGA1UEAwwObG9jYWxob3N0OjgwMDAxHDAa
BgkqhkiG9w0BCQEWDXRlc3RAdGVzdC5jb20wggEiMA0GCSqGSIb3DQEBAQUAA4IB
DwAwggEKAoIBAQCx73COifnB44oIGU5OO0Wy0tt8vzzd/bZgIhYqOw0+PFqGWi7a
9UnHpbhYx9NIoNo89CZO6TifsKFT2vuq/cphuhby1h+h+k1QT9jPVE+0vT5EFpe2
6l0eU9joI4g+lCI2HAZS6JEeYOky+yvPsro7K7L22/XoFCm24ZU1KSqsBCSoN4dk
qzcmaV2i5EqvUrg+SFOzesUdB2cj1cnydakPEVJPHgpNtlK1NQdhLiixHmjXuF9P
DvVKPuhSyDMJbfMvTjVveGDP4TrGLAoxKMZvUSGSL3hJ5IulwKXH2YUqU5UQyEOI
LETRNrf3fUR7zzueyRp9Qj+pnENziMeHwzIvAgMBAAEwDQYJKoZIhvcNAQELBQAD
ggEBACbkHE1wPiKvHQZIIBxETvVwU9iAosGFcHOGJgMt7CgatdYEdVUjtBN0sf+y
DfL2PNnaY/gEGohORpgcGWJ1s1zAo4dtGGfnK9IVu07bFqTnABS6aaYj4obl7wJt
gRswuB4QTwDrKVoFVNfhVqXRU5rGxqu1S40axK+ZhkHNH44JP2M1dpAxSFkSZ++S
MW5z67ODDCxOZGYp9f5ulOLSzEZjQ0ux3gndKEQt1SVqx/2ca6xcYyC//ga95Yv+
+Tm8REHMUg5er9deB/99j5AbWj5pYZYmqKnXDAm/2oFaVUqVMvtHfb2zEpF+BPbr
67tOV4gpT2Q2Z6dVlnnjtuaIx9U=
-----END CERTIFICATE-----
`

func NewDefaultWebSocketEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "ws://localhost:8000",
		Endpoint:       "/ws",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
	}
}

func NewDefaultWebTransportEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "https://localhost:8000",
		Endpoint:       "/wt/",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
		CACert:         []byte(caCert),
	}
}

type BroflakeOptions struct {
	ClientType   string
	CTableSize   int
	PTableSize   int
	BusBufferSz  int
	Netstated    string
	WebTransport bool
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
