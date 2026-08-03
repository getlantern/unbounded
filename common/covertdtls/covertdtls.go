// Package covertdtls wraps github.com/theodorsm/covert-dtls so broflake
// widgets can randomize or mimic their DTLS ClientHello fingerprint. The
// default pion/dtls fingerprint was being DPI-filtered in Russia starting
// 2026-03-30 (net4people/bbs#603), blocking Snowflake and, by extension, any
// pion-based WebRTC transport — including unbounded.
//
// The API mirrors the equivalent package in Snowflake v2.13.1 so operators
// familiar with one project can drop into the other.
package covertdtls

import (
	"errors"
	"strings"

	"github.com/theodorsm/covert-dtls/pkg/fingerprints"
)

// Mode names accepted by ParseModeString.
const (
	ModeRandomize      = "randomize"
	ModeMimic          = "mimic"
	ModeRandomizeMimic = "randomizemimic"
	ModeDisable        = "disable"
)

// Config configures the ClientHello fingerprint-resistance layer.
//
//   - Mimic replays a real browser ClientHello (latest Chrome/Firefox).
//   - Randomize shuffles cipher suites, extensions, and other fields so every
//     handshake has a unique fingerprint.
//   - Mimic+Randomize picks a random browser fingerprint from the bundled set
//     on each handshake (the most stable mode; matches Snowflake's default).
//   - Fingerprint pins a specific ClientHello by hex string, overriding the
//     other flags.
type Config struct {
	Randomize   bool
	Mimic       bool
	Fingerprint fingerprints.ClientHelloFingerprint
}

// Enabled reports whether any covert-dtls behavior is configured. Call sites
// should skip the SettingEngine wiring when this returns false to avoid
// allocating the covert-dtls machinery.
func (c Config) Enabled() bool {
	return c.Randomize || c.Mimic || len(c.Fingerprint) > 0
}

// ParseModeString produces a Config from a CLI-style mode string
// (randomize/mimic/randomizemimic/disable). Case-insensitive.
func ParseModeString(s string) (Config, error) {
	cfg := Config{}
	switch strings.ToLower(s) {
	case ModeRandomize:
		cfg.Randomize = true
	case ModeMimic:
		cfg.Mimic = true
	case ModeRandomizeMimic:
		cfg.Randomize = true
		cfg.Mimic = true
	case ModeDisable:
	default:
		return cfg, errors.New("covertdtls: unknown mode (want randomize, mimic, randomizemimic, or disable)")
	}
	return cfg, nil
}
