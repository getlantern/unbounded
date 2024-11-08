package clientcore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func newDefaultWebTransportEgressOptions() *EgressOptions {
	return &EgressOptions{
		Addr:           "https://localhost:8000",
		Endpoint:       "/wt/",
		ConnectTimeout: 5 * time.Second,
		ErrorBackoff:   5 * time.Second,
		CACert:         []byte(caCert),
	}
}

func TestConnection(t *testing.T) {
	options := newDefaultWebTransportEgressOptions()
	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	// rootC
	if ok := rootCAs.AppendCertsFromPEM(options.CACert); !ok {
		common.Debugf("Couldn't add root certificate: %v", options.CACert)
	}

	var d webtransport.Dialer = webtransport.Dialer{}
	d.RoundTripper = &http3.RoundTripper{
		TLSClientConfig: &tls.Config{
			//InsecureSkipVerify: true,
			RootCAs: rootCAs,
		},
	}

	// TODO: We ideally should create a single session and reuse it for all streams.
	_, _, err := d.Dial(context.Background(), options.Addr+options.Endpoint, nil)
	if err != nil {
		common.Debugf("Couldn't connect to egress server at %v: %v", options.Addr+options.Endpoint, err)
		//<-time.After(options.ErrorBackoff)
		//return 0, []interface{}{}
	} else {
		common.Debugf("Connected to egress server at %v", options.Addr+options.Endpoint)
	}
	time.Sleep(5 * time.Second)
}