package egress

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"log/slog"
	"slices"
	"sync"
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

// certEntry is one live listener's certificate health. NewListener may be called
// more than once per process, so health is registered per listener, removed on
// Close, and aggregated: a healthy listener must not hide an unverifiable one.
type certEntry struct {
	seq        uint64
	verifiable int64
	source     string
	notAfter   int64 // Unix seconds; 0 when the certificate is not verifiable
}

var (
	certHealthMu  sync.Mutex
	certHealth    = map[*certEntry]struct{}{}
	certHealthSeq uint64
)

// clientCertVerifiable is the minimum across live listeners, with the source of
// the listener that set it. Ties go to the certificate expiring soonest, then to
// the listener registered first, so the label is stable between collections.
func clientCertVerifiable() (int64, string, bool) {
	certHealthMu.Lock()
	defer certHealthMu.Unlock()
	var worst *certEntry
	for e := range certHealth {
		if worst == nil || worse(e, worst) {
			worst = e
		}
	}
	if worst == nil {
		return 0, "", false
	}
	return worst.verifiable, worst.source, true
}

func worse(a, b *certEntry) bool {
	if a.verifiable != b.verifiable {
		return a.verifiable < b.verifiable
	}
	if a.notAfter != b.notAfter {
		return a.notAfter < b.notAfter
	}
	return a.seq < b.seq
}

// egressCertSecondsToExpiry reports the verifiable certificate that expires
// soonest across live listeners.
func egressCertSecondsToExpiry(now time.Time) (int64, bool) {
	certHealthMu.Lock()
	defer certHealthMu.Unlock()
	var soonest int64
	for e := range certHealth {
		if e.notAfter != 0 && (soonest == 0 || e.notAfter < soonest) {
			soonest = e.notAfter
		}
	}
	if soonest == 0 {
		return 0, false
	}
	return soonest - now.Unix(), true
}

// registerEgressCert logs the certificate a listener will offer consumers and
// adds it to the certificate gauges. Call it after telemetry is up so the log
// line is exported. The returned release removes it and is safe to call twice.
func registerEgressCert(cfg *tls.Config) func() {
	info, ok := describeEgressCert(cfg)
	if !ok {
		return func() {}
	}
	source := "unknown"
	if p := clientCertSource.Load(); p != nil {
		source = *p
	}
	e := &certEntry{source: source}
	if info.clientAuth {
		// The self-signed fallback's 100-year NotAfter would make the expiry
		// gauge read as healthy, so only a verifiable certificate reports one.
		e.verifiable = 1
		e.notAfter = info.notAfter.Unix()
	}
	certHealthMu.Lock()
	certHealthSeq++
	e.seq = certHealthSeq
	certHealth[e] = struct{}{}
	certHealthMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			certHealthMu.Lock()
			delete(certHealth, e)
			certHealthMu.Unlock()
		})
	}
	logEgressCert(info)
	return release
}

func logEgressCert(info egressCertInfo) {
	slog.Info("Egress QUIC client certificate",
		"sha256", info.sha256,
		"not_after", info.notAfter.UTC().Format(time.RFC3339),
		"sans", info.sans,
		"client_auth", info.clientAuth,
	)
	if !info.clientAuth {
		slog.Warn("Egress QUIC client certificate is not CA-issued for client auth; consumers that verify the egress will reject it")
		return
	}
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
