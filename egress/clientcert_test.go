package egress

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
	egcmdcommon "github.com/getlantern/broflake/egress/cmd/common"
)

type certAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCertAuthority(t *testing.T, cn string) certAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certAuthority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leafSpec describes a leaf for issue. Zero validity means "valid now".
type leafSpec struct {
	eku       []x509.ExtKeyUsage
	san       string
	notBefore time.Time
	notAfter  time.Time
	// withChain appends the issuer's certificate after the leaf (full-chain PEM).
	withChain bool
}

// issueLeaf writes a CA-issued leaf and its key to disk and returns their paths.
func (ca certAuthority) issueLeaf(t *testing.T, eku []x509.ExtKeyUsage, san string) (certFile, keyFile string) {
	t.Helper()
	return ca.issue(t, leafSpec{eku: eku, san: san})
}

func (ca certAuthority) issue(t *testing.T, spec leafSpec) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if spec.notBefore.IsZero() {
		spec.notBefore = time.Now().Add(-time.Hour)
	}
	if spec.notAfter.IsZero() {
		spec.notAfter = time.Now().Add(12 * time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "unbounded egress"},
		NotBefore:    spec.notBefore,
		NotAfter:     spec.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  spec.eku,
		DNSNames:     []string{spec.san},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if spec.withChain {
		certPEM = append(certPEM, ca.pem...)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "egress-tls.crt"), filepath.Join(dir, "egress-tls.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// intermediate returns a CA signed by ca.
func (ca certAuthority) intermediate(t *testing.T, cn string) certAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certAuthority{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// keyPairConfig builds an egress client config straight from a key pair,
// bypassing LoadClientCertTLSConfig's checks so the consumer's rejection of a
// bad leaf can be tested on its own.
func keyPairConfig(t *testing.T, certFile, keyFile string) *tls.Config {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"broflake"}, InsecureSkipVerify: true}
}

// lanternBoxConsumerTLS mirrors the secure branch of lantern-box's
// protocol/unbounded/tls.go selfSignedTLSConfig: a fresh self-signed server
// cert, RequireAndVerifyClientCert against egress_ca, TLS 1.3, and an exact
// DNS-or-IP SAN match on egress_server_name. Keep it in step with that file;
// this test is only as good as the copy.
func lanternBoxConsumerTLS(t *testing.T, egressCA []byte, egressServerName string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(egressCA) {
		t.Fatal("parse egress_ca PEM: no certificates found")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"broflake"},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		ClientCAs:    caPool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("egress peer did not present a certificate")
			}
			cert := cs.PeerCertificates[0]
			for _, name := range cert.DNSNames {
				if name == egressServerName {
					return nil
				}
			}
			for _, ip := range cert.IPAddresses {
				if ip.String() == egressServerName {
					return nil
				}
			}
			return fmt.Errorf("egress peer SAN does not include %q", egressServerName)
		},
	}
}

// startVerifyingConsumer runs a QUIC server with serverTLS and reports every
// connection whose handshake the server accepted.
func startVerifyingConsumer(t *testing.T, serverTLS *tls.Config) (net.Addr, <-chan *quic.Conn) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: pc}
	ln, err := tr.Listen(serverTLS, &common.QUICCfg)
	if err != nil {
		_ = pc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
	accepted := make(chan *quic.Conn, 4)
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	return pc.LocalAddr(), accepted
}

// dialConsumer runs the egress's real dial path against the consumer. It returns
// whether the consumer accepted the handshake, and the egress-side error if the
// connection failed.
func dialConsumer(t *testing.T, egressTLS *tls.Config, consumer net.Addr, accepted <-chan *quic.Conn) (bool, error) {
	t.Helper()
	cm := &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       egressTLS,
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
	defer closeAllRecords(cm)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	conn, _, err := cm.createOrMigrate("csid-"+t.Name(), dialedPconn{PacketConn: pc, dst: consumer})
	if err != nil {
		return false, err
	}
	select {
	case <-accepted:
		return true, nil
	case <-conn.Context().Done():
		return false, context.Cause(conn.Context())
	case <-time.After(5 * time.Second):
		return false, fmt.Errorf("timed out waiting for the consumer")
	}
}

func TestEgressClientCert_ConsumerAcceptsCAIssuedLeaf(t *testing.T) {
	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	egressTLS, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	addr, accepted := startVerifyingConsumer(t, lanternBoxConsumerTLS(t, ca.pem, fleetSAN))

	ok, err := dialConsumer(t, egressTLS, addr, accepted)
	if !ok {
		t.Fatalf("consumer rejected the CA-issued egress leaf: %v", err)
	}
}

func TestEgressClientCert_ConsumerRejectsUnverifiableEgress(t *testing.T) {
	ca := newCertAuthority(t, "Unbounded Egress CA")
	other := newCertAuthority(t, "Some Other CA")

	wrongCACert, wrongCAKey := other.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	serverAuthCert, serverAuthKey := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, fleetSAN)
	wrongSANCert, wrongSANKey := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, "unbounded.iantem.io")

	// The Go client only offers a certificate whose issuer is in the server's
	// acceptable-CA list, so the self-signed and wrong-CA egresses send nothing
	// and the consumer reports certificate_required. A leaf that is sent but
	// fails verification is bad_certificate.
	cases := []struct {
		name  string
		tls   *tls.Config
		alert string
	}{
		{"self-signed fallback", egcmdcommon.GenerateSelfSignedTLSConfig(true), "certificate_required"},
		{"leaf from another CA", keyPairConfig(t, wrongCACert, wrongCAKey), "certificate_required"},
		{"leaf without clientAuth", keyPairConfig(t, serverAuthCert, serverAuthKey), "bad_certificate"},
		{"leaf with the wrong SAN", keyPairConfig(t, wrongSANCert, wrongSANKey), "bad_certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := handshakeFailureSnapshot()
			addr, accepted := startVerifyingConsumer(t, lanternBoxConsumerTLS(t, ca.pem, fleetSAN))

			ok, err := dialConsumer(t, tc.tls, addr, accepted)
			if ok {
				t.Fatal("consumer accepted an egress it should not trust")
			}
			t.Logf("egress-side error: %v", err)

			// The alert reaches the egress asynchronously when the consumer
			// rejects after the egress's side of the handshake completed.
			waitForAlert(t, before, handshakeAlert(tc.alert))
		})
	}
}

func handshakeFailureSnapshot() map[string]int64 {
	out := map[string]int64{}
	eachHandshakeFailure(func(alert handshakeAlert, n int64) { out[string(alert)] = n })
	return out
}

func TestDescribeEgressCert(t *testing.T) {
	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	cfg, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	info, ok := describeEgressCert(cfg)
	if !ok {
		t.Fatal("describeEgressCert found no certificate")
	}
	sum := sha256.Sum256(leaf.Raw)
	if info.sha256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %s, want the leaf's DER fingerprint", info.sha256)
	}
	if !info.notAfter.Equal(leaf.NotAfter) {
		t.Errorf("notAfter = %v, want %v", info.notAfter, leaf.NotAfter)
	}
	if len(info.sans) != 1 || info.sans[0] != fleetSAN {
		t.Errorf("sans = %v, want [%s]", info.sans, fleetSAN)
	}
	if !info.clientAuth {
		t.Error("clientAuth = false for a clientAuth leaf")
	}

	selfSigned, ok := describeEgressCert(egcmdcommon.GenerateSelfSignedTLSConfig(true))
	if !ok || selfSigned.clientAuth {
		t.Errorf("self-signed fallback described as %+v, want clientAuth=false", selfSigned)
	}
	if _, ok := describeEgressCert(nil); ok {
		t.Error("nil config described as having a certificate")
	}
}

// The expiry gauge reports only a certificate a consumer could verify. The
// self-signed fallback's 100-year NotAfter would otherwise read as "healthy".
func TestEgressCertExpiryGauge(t *testing.T) {
	t.Cleanup(func() { recordEgressCert(nil) })
	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	cfg, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	recordEgressCert(cfg)
	now := time.Now()
	secs, ok := egressCertSecondsToExpiry(now)
	if !ok {
		t.Fatal("no expiry reported for a CA-issued leaf")
	}
	want := int64(cfg.Certificates[0].Leaf.NotAfter.Sub(now) / time.Second)
	if secs < want-1 || secs > want+1 {
		t.Errorf("seconds to expiry = %d, want about %d", secs, want)
	}

	recordEgressCert(egcmdcommon.GenerateSelfSignedTLSConfig(true))
	if _, ok := egressCertSecondsToExpiry(now); ok {
		t.Error("expiry reported for the unverifiable self-signed fallback")
	}
}

func TestClassifyHandshakeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want handshakeAlert
		ok   bool
	}{
		{"nil", nil, "", false},
		{"plain error", errors.New("boom"), "", false},
		{"idle timeout", &quic.IdleTimeoutError{}, "", false},
		{"application close", &quic.ApplicationError{ErrorCode: 42069}, "", false},
		{"non-crypto transport error", &quic.TransportError{ErrorCode: quic.ProtocolViolation}, "", false},
		{"unknown_ca", &quic.TransportError{ErrorCode: 0x100 + 48, Remote: true}, alertUnknownCA, true},
		{"unlisted alert", &quic.TransportError{ErrorCode: 0x100 + 80, Remote: true}, alertOther, true},
		{"wrapped", fmt.Errorf("dial: %w", &quic.TransportError{ErrorCode: 0x100 + 116, Remote: true}), alertCertificateRequired, true},
		// The egress never verifies its peer, so a local crypto error is not a
		// consumer rejecting the egress certificate.
		{"local crypto error", &quic.TransportError{ErrorCode: 0x100 + 42}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := classifyHandshakeError(tc.err)
			if got != tc.want || ok != tc.ok {
				t.Errorf("classifyHandshakeError = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNewListener_RecordsEgressCert(t *testing.T) {
	withStubbedMetrics(t)
	t.Cleanup(func() { recordEgressCert(nil) })
	recordEgressCert(nil)

	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	cfg, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	tcpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ll, err := NewListener(context.Background(), tcpL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ll.Close() })

	if _, ok := egressCertSecondsToExpiry(time.Now()); !ok {
		t.Fatal("NewListener did not record the egress certificate")
	}
}

// The egress sends the whole chain from its certificate file, so a consumer whose
// egress_ca holds only the root still verifies a leaf issued by an intermediate.
// Without the intermediate in the file the Go client finds no certificate whose
// issuer the consumer accepts, sends none, and is rejected.
func TestEgressClientCert_IntermediateChainAgainstRootOnlyCA(t *testing.T) {
	root := newCertAuthority(t, "Unbounded Egress Root CA")
	inter := root.intermediate(t, "Unbounded Egress Issuing CA")

	t.Run("full chain", func(t *testing.T) {
		certFile, keyFile := inter.issue(t, leafSpec{eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, san: fleetSAN, withChain: true})
		egressTLS, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(egressTLS.Certificates[0].Certificate); n != 2 {
			t.Fatalf("loaded chain has %d certificates, want leaf + intermediate", n)
		}
		if egressTLS.Certificates[0].Leaf.Subject.CommonName != "unbounded egress" {
			t.Fatalf("leaf is %q, want the end-entity certificate", egressTLS.Certificates[0].Leaf.Subject.CommonName)
		}
		addr, accepted := startVerifyingConsumer(t, lanternBoxConsumerTLS(t, root.pem, fleetSAN))
		if ok, err := dialConsumer(t, egressTLS, addr, accepted); !ok {
			t.Fatalf("consumer rejected a full-chain egress certificate: %v", err)
		}
	})

	t.Run("leaf only", func(t *testing.T) {
		before := handshakeFailureSnapshot()
		certFile, keyFile := inter.issue(t, leafSpec{eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, san: fleetSAN})
		egressTLS, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		addr, accepted := startVerifyingConsumer(t, lanternBoxConsumerTLS(t, root.pem, fleetSAN))
		if ok, _ := dialConsumer(t, egressTLS, addr, accepted); ok {
			t.Fatal("consumer accepted a leaf whose intermediate it was never shown")
		}
		waitForAlert(t, before, alertCertificateRequired)
	})
}

func TestEgressClientCert_ConsumerRejectsExpiredLeaf(t *testing.T) {
	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issue(t, leafSpec{
		eku: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, san: fleetSAN,
		notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour),
	})
	egressTLS, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatalf("an expired leaf should still load: %v", err)
	}
	before := handshakeFailureSnapshot()
	addr, accepted := startVerifyingConsumer(t, lanternBoxConsumerTLS(t, ca.pem, fleetSAN))
	if ok, _ := dialConsumer(t, egressTLS, addr, accepted); ok {
		t.Fatal("consumer accepted an expired egress certificate")
	}
	// Go's server maps an expired client certificate to its own alert, which is
	// what makes expiry distinguishable from a wrong CA or SAN on the dashboard.
	waitForAlert(t, before, alertCertificateExpired)
}

func waitForAlert(t *testing.T, before map[string]int64, alert handshakeAlert) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := handshakeFailureSnapshot()[string(alert)] - before[string(alert)]
		if got == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("handshake-failures{alert=%q} grew by %d, want 1 (all: %v)", alert, got, handshakeFailureSnapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// client-cert-verifiable is reported in every mode, so a host that quietly fell
// back to the self-signed certificate shows up as 0 rather than as a gap.
func TestClientCertVerifiableGauge(t *testing.T) {
	t.Cleanup(func() { SetClientCertSource(""); recordEgressCert(nil) })
	ca := newCertAuthority(t, "Unbounded Egress CA")
	certFile, keyFile := ca.issueLeaf(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, fleetSAN)
	caIssued, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	selfSigned := egcmdcommon.GenerateSelfSignedTLSConfig(true)

	recordEgressCert(nil)
	if _, _, ok := clientCertVerifiable(); ok {
		t.Fatal("verifiable gauge reported before any certificate was recorded")
	}

	cases := []struct {
		source     string
		cfg        *tls.Config
		wantValue  int64
		wantSource string
	}{
		{egcmdcommon.SourceEnv, caIssued, 1, "env"},
		{egcmdcommon.SourceCredentials, caIssued, 1, "systemd-credentials"},
		{egcmdcommon.SourceSelfSigned, selfSigned, 0, "self-signed"},
		{"", caIssued, 1, "unknown"},
		{"something a caller made up", selfSigned, 0, "unknown"},
	}
	for _, tc := range cases {
		SetClientCertSource(tc.source)
		recordEgressCert(tc.cfg)
		value, source, ok := clientCertVerifiable()
		if !ok || value != tc.wantValue || source != tc.wantSource {
			t.Errorf("source %q: gauge = (%d, %q, %v), want (%d, %q, true)", tc.source, value, source, ok, tc.wantValue, tc.wantSource)
		}
	}
}

func TestRecordEgressCert_LogsProblems(t *testing.T) {
	t.Cleanup(func() { recordEgressCert(nil) })
	ca := newCertAuthority(t, "Unbounded Egress CA")
	load := func(spec leafSpec) *tls.Config {
		certFile, keyFile := ca.issue(t, spec)
		cfg, err := egcmdcommon.LoadClientCertTLSConfig(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	clientAuth := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	cases := []struct {
		name string
		cfg  *tls.Config
		want []string
		not  []string
	}{
		{"healthy", load(leafSpec{eku: clientAuth, san: fleetSAN}), nil, []string{"level=WARN", "level=ERROR"}},
		{"wrong SAN", load(leafSpec{eku: clientAuth, san: "unbounded.iantem.io"}), []string{"level=WARN", fleetSAN}, nil},
		{"expired", load(leafSpec{eku: clientAuth, san: fleetSAN, notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour)}), []string{"level=ERROR", "expired"}, nil},
		{"not yet valid", load(leafSpec{eku: clientAuth, san: fleetSAN, notBefore: time.Now().Add(time.Hour), notAfter: time.Now().Add(48 * time.Hour)}), []string{"level=ERROR", "not valid yet"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(prev)

			recordEgressCert(tc.cfg)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("log lacks %q:\n%s", w, out)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(out, n) {
					t.Errorf("log unexpectedly contains %q:\n%s", n, out)
				}
			}
		})
	}
}
