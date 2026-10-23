package common

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSAN = "egress.unbounded.lantern"

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Unbounded Egress CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
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
	return testCA{cert: cert, key: key}
}

// issue returns a PEM leaf and key signed by ca.
func (ca testCA) issue(t *testing.T, eku []x509.ExtKeyUsage, dnsNames []string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "egress"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadEgressTLSConfig_EnvVars(t *testing.T) {
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{testSAN})
	dir := t.TempDir()
	env := envOf(map[string]string{
		EnvCertFile: writeFile(t, dir, "c.pem", certPEM),
		EnvKeyFile:  writeFile(t, dir, "k.pem", keyPEM),
	})

	cfg, source, err := LoadEgressTLSConfig(env)
	if err != nil {
		t.Fatalf("LoadEgressTLSConfig: %v", err)
	}
	if source != SourceEnv {
		t.Fatalf("source = %q, want %q", source, SourceEnv)
	}
	assertBroflakeConfig(t, cfg)
	leaf := cfg.Certificates[0].Leaf
	if leaf == nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != testSAN {
		t.Fatalf("loaded leaf has unexpected SANs: %+v", leaf)
	}
}

func TestLoadEgressTLSConfig_SystemdCredentials(t *testing.T) {
	ca := newTestCA(t)
	certPEM, keyPEM := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{testSAN})
	dir := t.TempDir()
	writeFile(t, dir, CredentialCert, certPEM)
	writeFile(t, dir, CredentialKey, keyPEM)

	cfg, source, err := LoadEgressTLSConfig(envOf(map[string]string{"CREDENTIALS_DIRECTORY": dir}))
	if err != nil {
		t.Fatalf("LoadEgressTLSConfig: %v", err)
	}
	if source != SourceCredentials {
		t.Fatalf("source = %q, want %q", source, SourceCredentials)
	}
	assertBroflakeConfig(t, cfg)
}

// Explicit env vars are an operator override and win over systemd credentials.
func TestLoadEgressTLSConfig_EnvVarsBeatCredentials(t *testing.T) {
	ca := newTestCA(t)
	credCert, credKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{"from-credentials"})
	envCert, envKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{"from-env"})
	credDir := t.TempDir()
	writeFile(t, credDir, CredentialCert, credCert)
	writeFile(t, credDir, CredentialKey, credKey)
	envDir := t.TempDir()

	cfg, source, err := LoadEgressTLSConfig(envOf(map[string]string{
		"CREDENTIALS_DIRECTORY": credDir,
		EnvCertFile:             writeFile(t, envDir, "c.pem", envCert),
		EnvKeyFile:              writeFile(t, envDir, "k.pem", envKey),
	}))
	if err != nil {
		t.Fatalf("LoadEgressTLSConfig: %v", err)
	}
	if source != SourceEnv || cfg.Certificates[0].Leaf.DNSNames[0] != "from-env" {
		t.Fatalf("got source %q SAN %v, want env cert", source, cfg.Certificates[0].Leaf.DNSNames)
	}
}

func TestLoadEgressTLSConfig_NothingConfiguredFallsBackToSelfSigned(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no env":                       {},
		"credentials dir without ours": {"CREDENTIALS_DIRECTORY": t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, source, err := LoadEgressTLSConfig(envOf(env))
			if err != nil {
				t.Fatalf("LoadEgressTLSConfig: %v", err)
			}
			if source != SourceSelfSigned {
				t.Fatalf("source = %q, want %q", source, SourceSelfSigned)
			}
			assertBroflakeConfig(t, cfg)
		})
	}
}

func TestLoadEgressTLSConfig_ConfiguredButBrokenIsAnError(t *testing.T) {
	ca := newTestCA(t)
	good, goodKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{testSAN})
	_, otherKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []string{testSAN})
	serverOnly, serverOnlyKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{testSAN})
	noSAN, noSANKey := ca.issue(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)

	dir := t.TempDir()
	goodPath := writeFile(t, dir, "good.pem", good)
	goodKeyPath := writeFile(t, dir, "good.key", goodKey)
	otherKeyPath := writeFile(t, dir, "other.key", otherKey)
	serverOnlyPath := writeFile(t, dir, "server.pem", serverOnly)
	serverOnlyKeyPath := writeFile(t, dir, "server.key", serverOnlyKey)
	noSANPath := writeFile(t, dir, "nosan.pem", noSAN)
	noSANKeyPath := writeFile(t, dir, "nosan.key", noSANKey)
	garbagePath := writeFile(t, dir, "garbage.pem", []byte("not a pem"))

	credOnlyCert := t.TempDir()
	writeFile(t, credOnlyCert, CredentialCert, good)

	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"only cert env var": {map[string]string{EnvCertFile: goodPath}, EnvKeyFile},
		"only key env var":  {map[string]string{EnvKeyFile: goodKeyPath}, EnvCertFile},
		"missing cert file": {map[string]string{EnvCertFile: filepath.Join(dir, "nope"), EnvKeyFile: goodKeyPath}, "nope"},
		"garbage cert":      {map[string]string{EnvCertFile: garbagePath, EnvKeyFile: goodKeyPath}, "key pair"},
		"mismatched key":    {map[string]string{EnvCertFile: goodPath, EnvKeyFile: otherKeyPath}, "key pair"},
		"no clientAuth EKU": {map[string]string{EnvCertFile: serverOnlyPath, EnvKeyFile: serverOnlyKeyPath}, "clientAuth"},
		"no SAN":            {map[string]string{EnvCertFile: noSANPath, EnvKeyFile: noSANKeyPath}, "SAN"},
		"credential without key": {
			map[string]string{"CREDENTIALS_DIRECTORY": credOnlyCert}, CredentialKey,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, _, err := LoadEgressTLSConfig(envOf(tc.env))
			if err == nil {
				t.Fatalf("expected an error, got config with %d certs", len(cfg.Certificates))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func assertBroflakeConfig(t *testing.T, cfg *tls.Config) {
	t.Helper()
	if cfg == nil || len(cfg.Certificates) != 1 {
		t.Fatalf("want exactly one certificate, got %+v", cfg)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "broflake" {
		t.Fatalf("NextProtos = %v, want [broflake]", cfg.NextProtos)
	}
	// The egress never verifies the anonymous consumer's self-signed cert.
	if !cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must stay true: consumers are anonymous")
	}
}

// A credential that exists but cannot be stat'ed (permissions, a broken mount)
// must not read as "not configured": that would silently fall back to the
// self-signed certificate.
func TestLoadEgressTLSConfig_UnstatableCredentialIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, source, err := LoadEgressTLSConfig(envOf(map[string]string{"CREDENTIALS_DIRECTORY": dir}))
	if err == nil {
		t.Fatalf("expected an error, got a %q config", source)
	}
	if !strings.Contains(err.Error(), CredentialCert) {
		t.Fatalf("error %q does not name the credential", err)
	}
}

// An expired leaf still loads: refusing to start would also cut off consumers
// that do not verify. The egress package logs it at ERROR instead.
func TestLoadClientCertTLSConfig_ExpiredLeafLoads(t *testing.T) {
	ca := newTestCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{testSAN},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg, err := LoadClientCertTLSConfig(
		writeFile(t, dir, "c.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		writeFile(t, dir, "k.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	)
	if err != nil {
		t.Fatalf("expired leaf should load, got %v", err)
	}
	assertBroflakeConfig(t, cfg)
}
