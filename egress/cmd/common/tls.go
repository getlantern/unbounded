package common

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"time"
)

// Environment variables naming the egress's QUIC certificate and key, the
// same names examples/private-swarm/egress-server uses.
const (
	TLSCertFileEnv = "TLS_CERT_FILE"
	TLSKeyFileEnv  = "TLS_KEY_FILE"
)

// TLSConfigFromEnv returns the egress's QUIC TLS config. With TLSCertFileEnv
// and TLSKeyFileEnv set it presents that certificate; without either it falls
// back to a self-signed certificate generated per process, as before.
//
// The egress dials the consumer, so this is a TLS client certificate. A
// consumer verifies it against its egress_ca and egress_server_name options,
// so the certificate needs the clientAuth extended key usage and a SAN equal
// to egress_server_name. A fresh self-signed certificate on every start can
// never satisfy that, which is why a consumer that verifies needs the stable
// one.
//
// The egress never verifies the consumer, which is anonymous by design.
func TLSConfigFromEnv() (*tls.Config, error) {
	certFile, keyFile := os.Getenv(TLSCertFileEnv), os.Getenv(TLSKeyFileEnv)
	switch {
	case certFile == "" && keyFile == "":
		warnSelfSignedTLS()
		return GenerateSelfSignedTLSConfig(true), nil
	case certFile == "" || keyFile == "":
		return nil, fmt.Errorf("set both %s and %s, or neither", TLSCertFileEnv, TLSKeyFileEnv)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load egress TLS certificate: %w", err)
	}
	slog.Info("Presenting a stable egress TLS certificate", "cert_file", certFile)
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{"broflake"},
		InsecureSkipVerify: true,
	}, nil
}

func warnSelfSignedTLS() {
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@ DANGER                                                @")
	slog.Warn("@                                                       @")
	slog.Warn("@ This standalone egress server does not use secure TLS @")
	slog.Warn("@ at the QUIC layer!                                    @")
	slog.Warn("@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n")
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
