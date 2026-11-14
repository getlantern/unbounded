package clientcore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

func selfSignedHelloTestTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	}
}

// The consumer is the QUIC server and the egress the client, as in
// production, so the consumer's accepted connection sends the hello and the
// egress's dialed connection reads it.
func TestSendConsumerHello(t *testing.T) {
	tlsConf := selfSignedHelloTestTLS(t)

	consumerPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket consumer: %v", err)
	}
	t.Cleanup(func() { _ = consumerPC.Close() })
	listener, err := (&quic.Transport{Conn: consumerPC}).Listen(tlsConf, &common.QUICCfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	egressPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket egress: %v", err)
	}
	t.Cleanup(func() { _ = egressPC.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	accepted := make(chan *quic.Conn, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		if err == nil {
			accepted <- conn
		}
	}()

	egressConn, err := (&quic.Transport{Conn: egressPC}).Dial(ctx, consumerPC.LocalAddr(), tlsConf, &common.QUICCfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = egressConn.CloseWithError(0, "") })

	var consumerConn *quic.Conn
	select {
	case consumerConn = <-accepted:
	case <-ctx.Done():
		t.Fatal("consumer never accepted the connection")
	}

	want := common.ConsumerHello{Tag: "unbounded-out-trackA-r42", ClientVersion: "9.1.0", Platform: "android"}
	go sendConsumerHello(ctx, consumerConn, want)

	s, err := egressConn.AcceptUniStream(ctx)
	if err != nil {
		t.Fatalf("AcceptUniStream: %v", err)
	}
	got, err := common.ReadConsumerHello(s)
	if err != nil {
		t.Fatalf("ReadConsumerHello: %v", err)
	}
	if got != want {
		t.Fatalf("hello = %+v, want %+v", got, want)
	}
}
