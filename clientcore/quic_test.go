package clientcore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/quic-go/quic-go"
)

// TestClassifyQUICError pins down the mapping from quic-go / context
// error types to the short tags we emit in the structured-log
// "err_class" field. These tags are the SLA between the consumer-side
// instrumentation and any future analysis tool that aggregates them by
// failure mode.
//
// The QUIC-specific structs (ApplicationError, TransportError, etc.)
// are exported by quic-go so we construct them directly. IdleTimeoutError,
// HandshakeTimeoutError, VersionNegotiationError, and StatelessResetError
// are also exported; we build them with their zero values, which is fine
// for type-discrimination purposes.
func TestClassifyQUICError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"context.Canceled", context.Canceled, "context_canceled"},
		{"context.DeadlineExceeded", context.DeadlineExceeded, "deadline_exceeded"},
		{"plain other error", errors.New("eof on bfconn"), "other"},
		{"wrapped context.Canceled", fmt.Errorf("doing thing: %w", context.Canceled), "context_canceled"},
		{"IdleTimeoutError", &quic.IdleTimeoutError{}, "idle_timeout"},
		{"HandshakeTimeoutError", &quic.HandshakeTimeoutError{}, "handshake_timeout"},
		{"ApplicationError remote (peer-initiated — egress flushing record, normal teardown)",
			&quic.ApplicationError{ErrorCode: 42069, Remote: true, ErrorMessage: "egress closed"},
			"application_close_remote"},
		{"ApplicationError local (we closed mid-flight — should not happen in steady state)",
			&quic.ApplicationError{ErrorCode: 42069, Remote: false, ErrorMessage: "local close"},
			"application_close_local"},
		{"TransportError",
			&quic.TransportError{ErrorCode: quic.ConnectionRefused, ErrorMessage: "refused"},
			"transport_error"},
		{"VersionNegotiationError", &quic.VersionNegotiationError{}, "version_negotiation"},
		{"StatelessResetError", &quic.StatelessResetError{}, "stateless_reset"},
		{"wrapped remote ApplicationError",
			fmt.Errorf("AcceptStream: %w", &quic.ApplicationError{ErrorCode: 1, Remote: true}),
			"application_close_remote"},
		{"wrapped local ApplicationError",
			fmt.Errorf("AcceptStream: %w", &quic.ApplicationError{ErrorCode: 1, Remote: false}),
			"application_close_local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyQUICError(tc.err); got != tc.want {
				t.Errorf("classifyQUICError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
