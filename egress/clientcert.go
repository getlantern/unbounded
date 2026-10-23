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
	notBefore  time.Time
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
		notBefore:  leaf.NotBefore,
		notAfter:   leaf.NotAfter,
		sans:       sans,
		clientAuth: slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth),
	}, true
}

// fleetSAN is the name consumers pin as egress_server_name. A leaf without it
// loads, since a test rig may pin another name, but production consumers reject it.
const fleetSAN = "egress.unbounded.lantern"

// Certificate sources, as reported on client-cert-verifiable. They mirror the
// constants in egress/cmd/common, which this package cannot import.
var knownCertSources = map[string]bool{"env": true, "systemd-credentials": true, "self-signed": true}

var clientCertSource atomic.Pointer[string]

// SetClientCertSource names where the egress certificate came from ("env",
// "systemd-credentials" or "self-signed"; anything else reports "unknown"). Call
// it before NewListener.
func SetClientCertSource(source string) {
	if !knownCertSources[source] {
		source = "unknown"
	}
	clientCertSource.Store(&source)
}

type verifiableState struct {
	value  int64
	source string
}

// certVerifiable backs the client-cert-verifiable gauge; nil until a certificate
// has been recorded.
var certVerifiable atomic.Pointer[verifiableState]

func clientCertVerifiable() (int64, string, bool) {
	st := certVerifiable.Load()
	if st == nil {
		return 0, "", false
	}
	return st.value, st.source, true
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
		certVerifiable.Store(nil)
		return
	}
	source := "unknown"
	if p := clientCertSource.Load(); p != nil {
		source = *p
	}
	st := &verifiableState{source: source}
	if info.clientAuth {
		st.value = 1
	}
	certVerifiable.Store(st)

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
	now := time.Now()
	if now.After(info.notAfter) {
		slog.Error("Egress QUIC client certificate has expired; consumers that verify the egress will reject it",
			"not_after", info.notAfter.UTC().Format(time.RFC3339))
	}
	if now.Before(info.notBefore) {
		slog.Error("Egress QUIC client certificate is not valid yet; consumers that verify the egress will reject it until then",
			"not_before", info.notBefore.UTC().Format(time.RFC3339))
	}
	if !slices.Contains(info.sans, fleetSAN) {
		slog.Warn("Egress QUIC client certificate does not carry the fleet SAN; consumers pinning it will reject this egress",
			"want_san", fleetSAN, "sans", info.sans)
	}
}

func egressCertSecondsToExpiry(now time.Time) (int64, bool) {
	notAfter := egressCertNotAfter.Load()
	if notAfter == 0 {
		return 0, false
	}
	return notAfter - now.Unix(), true
}
