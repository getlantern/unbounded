package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ConsumerHello is metadata a consumer sends to the egress once per QUIC
// connection, so the egress can say which client and track a session served.
// Nothing else on the egress side identifies the consumer: it sees the donor's
// address, the random CSID and an optional country, all relayed by the donor.
//
// It travels on a unidirectional QUIC stream. Bidirectional streams are the
// proxied connections the egress hands to its SOCKS5 server, so keeping the
// hello off them means no proxied stream is ever inspected or delayed for it.
// An egress that predates the hello never accepts a unidirectional stream, so
// the hello sits unread and nothing else changes. Inside QUIC it is also out of
// the donor's sight, unlike the subprotocol header the CSID rides in.
//
// Every field is optional. Unknown JSON fields are ignored on read, so a newer
// consumer can add one without breaking an older egress.
type ConsumerHello struct {
	// Tag is the consumer's outbound tag, which for a Lantern client names
	// the track and route it was assigned.
	Tag           string `json:"tag,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	Platform      string `json:"platform,omitempty"`
}

const (
	// consumerHelloMagic prefixes the hello so a stream that is not one is
	// rejected rather than decoded.
	consumerHelloMagic = "bfhello1\n"

	// MaxConsumerHelloSize bounds how much of the stream the egress reads.
	// The hello is peer-supplied, so it must not be able to make the egress
	// buffer an arbitrary amount.
	MaxConsumerHelloSize = 1024

	// maxConsumerHelloField bounds each field. These values are logged, so a
	// consumer must not be able to put long or unprintable strings on disk.
	maxConsumerHelloField = 128
)

var ErrNotConsumerHello = errors.New("stream is not a consumer hello")

// WriteConsumerHello writes h to w in the form ReadConsumerHello accepts.
func WriteConsumerHello(w io.Writer, h ConsumerHello) error {
	body, err := json.Marshal(h.sanitized())
	if err != nil {
		return fmt.Errorf("encode consumer hello: %w", err)
	}
	if len(consumerHelloMagic)+len(body) > MaxConsumerHelloSize {
		return fmt.Errorf("consumer hello is %d bytes, over the %d byte limit",
			len(consumerHelloMagic)+len(body), MaxConsumerHelloSize)
	}
	_, err = w.Write(append([]byte(consumerHelloMagic), body...))
	return err
}

// ReadConsumerHello reads one hello from r, which should be a stream the
// consumer closes after writing. It reads at most MaxConsumerHelloSize bytes
// and returns sanitized fields.
func ReadConsumerHello(r io.Reader) (ConsumerHello, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxConsumerHelloSize+1))
	if err != nil {
		return ConsumerHello{}, fmt.Errorf("read consumer hello: %w", err)
	}
	if len(buf) > MaxConsumerHelloSize {
		return ConsumerHello{}, fmt.Errorf("consumer hello exceeds %d bytes", MaxConsumerHelloSize)
	}
	body, ok := bytes.CutPrefix(buf, []byte(consumerHelloMagic))
	if !ok {
		return ConsumerHello{}, ErrNotConsumerHello
	}
	var h ConsumerHello
	if err := json.Unmarshal(body, &h); err != nil {
		return ConsumerHello{}, fmt.Errorf("decode consumer hello: %w", err)
	}
	return h.sanitized(), nil
}

func (h ConsumerHello) sanitized() ConsumerHello {
	return ConsumerHello{
		Tag:           sanitizeHelloField(h.Tag),
		ClientVersion: sanitizeHelloField(h.ClientVersion),
		Platform:      sanitizeHelloField(h.Platform),
	}
}

func sanitizeHelloField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if r := []rune(s); len(r) > maxConsumerHelloField {
		s = string(r[:maxConsumerHelloField])
	}
	return s
}
