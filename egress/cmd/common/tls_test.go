package common

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testEgressServerName = "unbounded-egress.test"

// issueEgressCert writes a CA-signed egress client certificate and key to
// dir and returns their paths with the CA's PEM.
func issueEgressCert(t *testing.T, dir string) (certFile, keyFile string, caPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{testEgressServerName},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, "egress.crt")
	keyFile = filepath.Join(dir, "egress.key")
	writePEM(t, certFile, "CERTIFICATE", leafDER)
	writePEM(t, keyFile, "EC PRIVATE KEY", leafKeyDER)
	return certFile, keyFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// verifyingConsumerTLS mirrors the consumer's config when it verifies the
// egress (lantern-box protocol/unbounded/tls.go): it is the TLS server,
// requires a client certificate chaining to caPEM, and checks its SAN.
func verifyingConsumerTLS(t *testing.T, caPEM []byte) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("bad CA PEM")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no egress certificate")
			}
			return cs.PeerCertificates[0].VerifyHostname(testEgressServerName)
		},
	}
}

// handshake runs a TLS handshake with the egress as client and the consumer
// as server, as in production, and returns the consumer's error.
func handshake(t *testing.T, egress, consumer *tls.Config) error {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(5 * time.Second))
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- tls.Client(a, egress).Handshake()
		// In TLS 1.3 the client finishes before the server has checked its
		// certificate, so keep reading or the server's rejection alert
		// blocks on the synchronous pipe until the deadline.
		_, _ = io.Copy(io.Discard, a)
	}()
	serverErr := tls.Server(b, consumer).Handshake()
	_ = b.Close()
	<-clientErr
	return serverErr
}

func TestTLSConfigFromEnv_StableCertPassesConsumerVerification(t *testing.T) {
	certFile, keyFile, caPEM := issueEgressCert(t, t.TempDir())
	t.Setenv(TLSCertFileEnv, certFile)
	t.Setenv(TLSKeyFileEnv, keyFile)

	egress, err := TLSConfigFromEnv()
	if err != nil {
		t.Fatalf("TLSConfigFromEnv: %v", err)
	}
	if err := handshake(t, egress, verifyingConsumerTLS(t, caPEM)); err != nil {
		t.Fatalf("verifying consumer rejected the stable egress cert: %v", err)
	}
}

func TestTLSConfigFromEnv_SelfSignedFailsConsumerVerification(t *testing.T) {
	_, _, caPEM := issueEgressCert(t, t.TempDir())
	t.Setenv(TLSCertFileEnv, "")
	t.Setenv(TLSKeyFileEnv, "")

	egress, err := TLSConfigFromEnv()
	if err != nil {
		t.Fatalf("TLSConfigFromEnv: %v", err)
	}
	err = handshake(t, egress, verifyingConsumerTLS(t, caPEM))
	if err == nil {
		t.Fatal("verifying consumer accepted a per-process self-signed egress cert")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("handshake timed out instead of being rejected: %v", err)
	}
}

func TestTLSConfigFromEnv_RequiresBothFiles(t *testing.T) {
	certFile, _, _ := issueEgressCert(t, t.TempDir())
	t.Setenv(TLSCertFileEnv, certFile)
	t.Setenv(TLSKeyFileEnv, "")
	if _, err := TLSConfigFromEnv(); err == nil {
		t.Fatal("accepted a cert file without a key file")
	}
}

func TestTLSConfigFromEnv_BadFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(TLSCertFileEnv, filepath.Join(dir, "missing.crt"))
	t.Setenv(TLSKeyFileEnv, filepath.Join(dir, "missing.key"))
	if _, err := TLSConfigFromEnv(); err == nil {
		t.Fatal("accepted missing cert files")
	}
}
