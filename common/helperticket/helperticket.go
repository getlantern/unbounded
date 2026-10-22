// Package helperticket verifies Lantern helper pairing tickets: short-lived
// statements, signed by lantern-cloud, that their bearer may use a pairing in
// one role until an expiry. Freddie verifies them to put consumers and
// producers in rooms keyed by pairing ID.
//
// The format is lantern-cloud's (cmd/api/pairing/ticket.go). testdata/
// vectors.json is a copy of the "ticket" section of lantern-cloud's golden
// vectors, and the two must not drift.
//
// Tickets are credentials and pairing IDs are private: callers must never
// log, trace or count either.
package helperticket

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/getlantern/broflake/common"
)

// Role says which side of a pairing a ticket was issued to.
type Role string

const (
	RoleInviter   Role = "inviter"
	RoleVolunteer Role = "volunteer"
)

var (
	ErrInvalid    = errors.New("helper ticket invalid")
	ErrExpired    = errors.New("helper ticket expired")
	ErrUnknownKey = errors.New("helper ticket signed by an unknown key")
)

// Ticket is a ticket's contents. PairingID is the room it admits to: an
// opaque, canonical lowercase UUID.
type Ticket struct {
	KeyID     string
	PairingID string
	Role      Role
	Expires   time.Time
}

// Key is a public key tickets are verified with, and its ID.
type Key struct {
	ID     string
	Public ed25519.PublicKey
}

// KeyID names pub: the first 8 hex characters of SHA-256 of the raw key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:4])
}

// NewKey returns pub with its ID.
func NewKey(pub ed25519.PublicKey) Key {
	return Key{ID: KeyID(pub), Public: append(ed25519.PublicKey(nil), pub...)}
}

// wire is the payload. Its field order is the format's key order.
type wire struct {
	V int    `json:"v"`
	K string `json:"k"`
	P string `json:"p"`
	R Role   `json:"r"`
	E int64  `json:"e"`
}

// b64 is strict so that each ticket has exactly one encoding: a token whose
// unused trailing bits are set is refused rather than read as the same ticket.
var b64 = base64.RawURLEncoding.Strict()

// Sign encodes and signs t with priv, naming priv's key; t.KeyID is ignored.
// lantern-cloud is the only production signer: this is for tests and tools.
func Sign(priv ed25519.PrivateKey, t Ticket) (string, error) {
	if t.Role != RoleInviter && t.Role != RoleVolunteer {
		return "", fmt.Errorf("helperticket: unknown role %q", t.Role)
	}
	id, err := uuid.Parse(t.PairingID)
	if err != nil {
		return "", fmt.Errorf("helperticket: pairing ID: %w", err)
	}
	payload, err := json.Marshal(wire{V: 1, K: KeyID(priv.Public().(ed25519.PublicKey)), P: id.String(), R: t.Role, E: t.Expires.Unix()})
	if err != nil {
		return "", fmt.Errorf("helperticket: encode: %w", err)
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(ed25519.Sign(priv, payload)), nil
}

// isB64 reports whether s is only base64url characters. The decoder skips
// '\r' and '\n', which would let one ticket have many encodings.
func isB64(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// decode splits and decodes token without verifying it.
func decode(token string) (payload, sig []byte, w wire, err error) {
	if len(token) > common.MaxRoomTicketLen {
		return nil, nil, wire{}, ErrInvalid
	}
	enc, encSig, ok := strings.Cut(token, ".")
	if !ok || !isB64(enc) || !isB64(encSig) {
		return nil, nil, wire{}, ErrInvalid
	}
	payload, err1 := b64.DecodeString(enc)
	sig, err2 := b64.DecodeString(encSig)
	if err1 != nil || err2 != nil {
		return nil, nil, wire{}, ErrInvalid
	}
	if err := json.Unmarshal(payload, &w); err != nil {
		return nil, nil, wire{}, ErrInvalid
	}
	return payload, sig, w, nil
}

// Verify checks token against the key in keys that it names, then its
// expiry at now. An expired ticket's contents come back with ErrExpired.
func Verify(keys []Key, token string, now time.Time) (Ticket, error) {
	payload, sig, w, err := decode(token)
	if err != nil {
		return Ticket{}, err
	}
	if w.K == "" || len(sig) != ed25519.SignatureSize {
		return Ticket{}, ErrInvalid
	}
	var key *Key
	for i := range keys {
		if keys[i].ID == w.K {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return Ticket{}, ErrUnknownKey
	}
	if len(key.Public) != ed25519.PublicKeySize || !ed25519.Verify(key.Public, payload, sig) {
		return Ticket{}, ErrInvalid
	}
	// Only now, with the signature checked, is anything else in it read.
	id, err := uuid.Parse(w.P)
	if w.V != 1 || err != nil || id.String() != w.P || (w.R != RoleInviter && w.R != RoleVolunteer) {
		return Ticket{}, ErrInvalid
	}
	t := Ticket{KeyID: w.K, PairingID: w.P, Role: w.R, Expires: time.Unix(w.E, 0)}
	if !now.Before(t.Expires) {
		return t, ErrExpired
	}
	return t, nil
}

// Peek returns the role and pairing ID token claims, without verifying it,
// or "", "" if it cannot be read. Clients use it to sort and compare their
// own tickets; nothing may trust it.
func Peek(token string) (Role, string) {
	_, _, w, err := decode(token)
	if err != nil || (w.R != RoleInviter && w.R != RoleVolunteer) || w.P == "" {
		return "", ""
	}
	return w.R, w.P
}

// Expiry returns the expiry token claims, without verifying it, or false if
// it cannot be read. Like Peek's, nothing may trust it on its own: Freddie
// reads it only from a ticket its verifier has just accepted, to bound how
// long it keeps that result.
func Expiry(token string) (time.Time, bool) {
	_, _, w, err := decode(token)
	if err != nil || w.E == 0 {
		return time.Time{}, false
	}
	return time.Unix(w.E, 0), true
}

// RoomFunc returns a hook for freddie.Freddie.RoomTicket that verifies tickets
// against keys() at now(), requires role, and returns the pairing ID.
func RoomFunc(keys func() []Key, now func() time.Time) func(token string, role Role) (string, bool) {
	return func(token string, role Role) (string, bool) {
		t, err := Verify(keys(), token, now())
		if err != nil || t.Role != role {
			return "", false
		}
		return t.PairingID, true
	}
}
