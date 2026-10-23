package egress

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"
)

// The egress is the QUIC client on the inner layer, and consumers verify the
// certificate it offers as a TLS client certificate. These record which
// certificate is live, so that "what is this egress presenting, and when does it
// expire" can be answered from the exported logs and metrics rather than by asking
// the host.

type egressCertInfo struct {
	sha256     string
	notAfter   time.Time
	sans       []string
	clientAuth bool
}

// describeEgressCert summarises the leaf the egress offers. It carries no key
// material.
func describeEgressCert(cfg *tls.Config) (egressCertInfo, bool) {
	if cfg == nil || len(cfg.Certificates) == 0 || len(cfg.Certificates[0].Certificate) == 0 {
		return egressCertInfo{}, false
	}
	leaf := cfg.Certificates[0].Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(cfg.Certificates[0].Certificate[0]); err != nil {
			return egressCertInfo{}, false
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	sans := slices.Clone(leaf.DNSNames)
	for _, ip := range leaf.IPAddresses {
		sans = append(sans, ip.String())
	}
	return egressCertInfo{
		sha256:     hex.EncodeToString(sum[:]),
		notAfter:   leaf.NotAfter,
		sans:       sans,
		clientAuth: slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth),
	}, true
}

// egressCertNotAfter is the verifiable certificate's NotAfter in Unix seconds, or 0
// when the egress has none. Process-global like the other observed tallies: the
// otel callback is registered once per process.
var egressCertNotAfter atomic.Int64

// recordEgressCert logs the certificate the egress will offer consumers and arms
// the expiry gauge. Call it after telemetry is up so the log line is exported.
func recordEgressCert(cfg *tls.Config) {
	info, ok := describeEgressCert(cfg)
	if !ok {
		egressCertNotAfter.Store(0)
		return
	}
	slog.Info("Egress QUIC client certificate",
		"sha256", info.sha256,
		"not_after", info.notAfter.UTC().Format(time.RFC3339),
		"sans", info.sans,
		"client_auth", info.clientAuth,
	)
	if !info.clientAuth {
		// The self-signed fallback lands here. Its 100-year NotAfter would make
		// the expiry gauge read as healthy, so it reports nothing instead.
		egressCertNotAfter.Store(0)
		slog.Warn("Egress QUIC client certificate is not CA-issued for client auth; consumers that verify the egress will reject it")
		return
	}
	egressCertNotAfter.Store(info.notAfter.Unix())
	if time.Now().After(info.notAfter) {
		slog.Error("Egress QUIC client certificate has expired; consumers that verify the egress will reject it",
			"not_after", info.notAfter.UTC().Format(time.RFC3339))
	}
}

func egressCertSecondsToExpiry(now time.Time) (int64, bool) {
	notAfter := egressCertNotAfter.Load()
	if notAfter == 0 {
		return 0, false
	}
	return notAfter - now.Unix(), true
}
