package common

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The egress is the QUIC/TLS *client* on the inner layer: it dials the consumer,
// and the consumer (lantern-box's unbounded outbound) verifies the certificate the
// egress offers. That certificate comes from one of these sources, checked in
// order:
//
//  1. EnvCertFile and EnvKeyFile, an explicit operator override. Setting either
//     one means both are required.
//  2. systemd credentials: when $CREDENTIALS_DIRECTORY is set and holds either
//     CredentialCert or CredentialKey, both are required.
//  3. Neither: a throwaway self-signed certificate that no consumer can verify.
//
// A source that is configured but unusable is an error, never a fallback to 3.
const (
	EnvCertFile = "EGRESS_TLS_CERT_FILE"
	EnvKeyFile  = "EGRESS_TLS_KEY_FILE"

	envCredentialsDirectory = "CREDENTIALS_DIRECTORY"
	CredentialCert          = "egress-tls.crt"
	CredentialKey           = "egress-tls.key"
)

const (
	SourceEnv         = "env"
	SourceCredentials = "systemd-credentials"
	SourceSelfSigned  = "self-signed"
)

// LoadEgressTLSConfig returns the egress's inner QUIC TLS config and the source
// its certificate came from. getenv is os.Getenv outside tests.
func LoadEgressTLSConfig(getenv func(string) string) (*tls.Config, string, error) {
	certFile, keyFile := getenv(EnvCertFile), getenv(EnvKeyFile)
	if certFile != "" || keyFile != "" {
		if certFile == "" {
			return nil, SourceEnv, fmt.Errorf("%s is set but %s is not", EnvKeyFile, EnvCertFile)
		}
		if keyFile == "" {
			return nil, SourceEnv, fmt.Errorf("%s is set but %s is not", EnvCertFile, EnvKeyFile)
		}
		cfg, err := LoadClientCertTLSConfig(certFile, keyFile)
		return cfg, SourceEnv, err
	}

	if dir := getenv(envCredentialsDirectory); dir != "" {
		certFile, keyFile = filepath.Join(dir, CredentialCert), filepath.Join(dir, CredentialKey)
		certOK, keyOK := exists(certFile), exists(keyFile)
		if certOK || keyOK {
			if !certOK {
				return nil, SourceCredentials, fmt.Errorf("systemd credential %s is present but %s is not", CredentialKey, CredentialCert)
			}
			if !keyOK {
				return nil, SourceCredentials, fmt.Errorf("systemd credential %s is present but %s is not", CredentialCert, CredentialKey)
			}
			cfg, err := LoadClientCertTLSConfig(certFile, keyFile)
			return cfg, SourceCredentials, err
		}
	}

	return GenerateSelfSignedTLSConfig(true), SourceSelfSigned, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// LoadClientCertTLSConfig loads a CA-issued egress certificate. certFile holds the
// leaf first, followed by any intermediates; all of them are sent to the consumer.
//
// It rejects a leaf that lantern-box could never accept: Go verifies client
// certificates against the clientAuth EKU, and lantern-box pins an exact DNS or IP
// SAN, so a leaf missing either would load fine here and fail every handshake.
func LoadClientCertTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read egress TLS certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read egress TLS key: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load egress TLS key pair from %s and %s: %w", certFile, keyFile, err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parse egress TLS certificate: %w", err)
		}
		cert.Leaf = leaf
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return nil, errors.New("egress TLS certificate lacks the clientAuth extended key usage; consumers verify it as a client certificate and would reject it")
	}
	if len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0 {
		return nil, errors.New("egress TLS certificate has no DNS or IP SAN; consumers match the SAN exactly and would reject it")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"broflake"},
		// The egress does not verify the consumer: consumers are anonymous and
		// present a fresh self-signed certificate.
		InsecureSkipVerify: true,
	}, nil
}

func GenerateSelfSignedTLSConfig(insecureSkipVerify bool) *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().AddDate(100, 0, 0),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{tlsCert},
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: insecureSkipVerify,
	}
}
