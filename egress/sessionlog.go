package egress

import (
	"log/slog"
	"time"

	"github.com/getlantern/broflake/common"
)

// sessionSummary is what one WebSocket session's end-of-session log line
// reports.
type sessionSummary struct {
	csid            string
	protocolVersion string
	consumerCountry string
	donorCountry    string
	ingressBytes    int64
	quicStreams     int64
	teardown        teardownReason
	duration        time.Duration
	hello           common.ConsumerHello
}

// logSessionEnd writes one Info line per WebSocket session. The
// egress.websocket_session span carries the same fields but is sampled at 1%,
// so it cannot answer whether a particular consumer reached the egress; this
// line can, keyed by CSID prefix and by the consumer's hello.
//
// Unlike the egress's other exported Info lines this is not rate-limited: its
// volume is one line per session, so it grows with traffic.
//
// Keys match the span's attribute names, so a query works on either.
func logSessionEnd(s sessionSummary) {
	attrs := []any{
		attrConsumerSessionID, csidPrefix(s.csid),
		attrProtocolVersion, s.protocolVersion,
		attrIngressBytes, s.ingressBytes,
		attrQUICStreams, s.quicStreams,
		attrTeardownReason, string(s.teardown),
		attrSessionDuration, s.duration.Seconds(),
	}
	for _, kv := range []struct{ key, value string }{
		{attrConsumerCountry, s.consumerCountry},
		{attrDonorCountry, s.donorCountry},
		{attrConsumerTag, s.hello.Tag},
		{attrConsumerVersion, s.hello.ClientVersion},
		{attrConsumerPlatform, s.hello.Platform},
	} {
		if kv.value != "" {
			attrs = append(attrs, kv.key, kv.value)
		}
	}
	slog.Info("WebSocket session ended", attrs...)
}
