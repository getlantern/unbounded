package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"

	"github.com/quic-go/quic-go"
)

// Consumers verify the egress's client certificate, and a consumer that rejects it
// fails silently: quic-go does not surface server-side handshake failures from
// Accept, so the consumer's route just carries nothing. The TLS alert the consumer
// sends back to the egress is the only place a certificate mismatch shows up, so
// it is counted here, labelled by alert and nothing else (no peer identifiers).

// handshakeAlert is a TLS alert name drawn from a fixed set, so a peer cannot
// choose label values.
type handshakeAlert string

const (
	alertBadCertificate         handshakeAlert = "bad_certificate"
	alertUnsupportedCertificate handshakeAlert = "unsupported_certificate"
	alertCertificateRevoked     handshakeAlert = "certificate_revoked"
	alertCertificateExpired     handshakeAlert = "certificate_expired"
	alertCertificateUnknown     handshakeAlert = "certificate_unknown"
	alertUnknownCA              handshakeAlert = "unknown_ca"
	alertAccessDenied           handshakeAlert = "access_denied"
	alertCertificateRequired    handshakeAlert = "certificate_required"
	alertNoApplicationProtocol  handshakeAlert = "no_application_protocol"
	alertOther                  handshakeAlert = "other"
)

var alertNames = map[tls.AlertError]handshakeAlert{
	42:  alertBadCertificate,
	43:  alertUnsupportedCertificate,
	44:  alertCertificateRevoked,
	45:  alertCertificateExpired,
	46:  alertCertificateUnknown,
	48:  alertUnknownCA,
	49:  alertAccessDenied,
	116: alertCertificateRequired,
	120: alertNoApplicationProtocol,
}

var handshakeFailures = newLabeledTally()

// A misissued or missing certificate fails every dial, so the log is throttled per
// alert; the counter stays exact.
var handshakeFailureLogs = newLogThrottle(time.Minute)

// recordHandshakeFailure counts err if it is a QUIC CRYPTO_ERROR, which is how a
// TLS alert travels in QUIC. Anything else (timeouts, idle closes, application
// closes) is not a handshake failure and is ignored.
func recordHandshakeFailure(err error) {
	alert, ok := classifyHandshakeError(err)
	if !ok {
		return
	}
	handshakeFailures.add(string(alert))
	if emit, suppressed := handshakeFailureLogs.allow(string(alert), time.Now()); emit {
		slog.Warn("Consumer rejected the egress QUIC handshake; check the egress client certificate against the consumers' egress_ca and egress_server_name",
			"alert", string(alert), "suppressed", suppressed)
	}
}

// watchHandshake records a rejection that arrives after Dial returned. In TLS 1.3
// the client finishes its side of the handshake before the server has verified the
// client certificate, so quic-go's Dial succeeds and the consumer's alert only
// shows up as the connection's close cause.
func watchHandshake(conn *quic.Conn) {
	ctx := conn.Context()
	<-ctx.Done()
	recordHandshakeFailure(context.Cause(ctx))
}

func classifyHandshakeError(err error) (handshakeAlert, bool) {
	var te *quic.TransportError
	// Remote only: the egress never verifies its peer, so the alert that matters
	// is the one the consumer sent.
	if !errors.As(err, &te) || !te.Remote || !te.ErrorCode.IsCryptoError() {
		return "", false
	}
	if name, ok := alertNames[tls.AlertError(te.ErrorCode-0x100)]; ok {
		return name, true
	}
	return alertOther, true
}

func eachHandshakeFailure(f func(handshakeAlert, int64)) {
	handshakeFailures.each(func(label string, count int64) { f(handshakeAlert(label), count) })
}
