// Package helperinvite is the device side of Lantern helper invites: the
// invite secret and link, the keys derived from the secret, the matching
// code, the approval signature and the encrypted display names. The Lantern
// app and the browser widget both use it, so there is one implementation.
//
// The formats are lantern-cloud's (cmd/api/pairing/invite.go and the plan 1
// wire-format table). testdata/lantern-cloud-vectors.json is a verbatim copy
// of lantern-cloud's cmd/api/pairing/testdata/vectors.json, and the two must
// not drift. testdata/names.json holds the name-blob vectors, which
// lantern-cloud cannot produce because it never sees a name.
//
// Secrets, names and codes are private: callers must never log them.
package helperinvite

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// SecretLen is the length of an invite secret in bytes.
const SecretLen = 32

const (
	infoKey           = "lantern-helper-invite-v1/key"
	infoApprove       = "lantern-helper-invite-v1/approve"
	infoVolunteerName = "lantern-helper-invite-v1/volunteer-name"
	infoInviterName   = "lantern-helper-invite-v1/inviter-name"
)

// ErrSecret is returned for a secret that is not SecretLen bytes.
var ErrSecret = errors.New("helperinvite: an invite secret is 32 bytes")

// NewSecret reads a fresh invite secret from r, which is crypto/rand.Reader
// outside tests.
func NewSecret(r io.Reader) ([]byte, error) {
	s := make([]byte, SecretLen)
	if _, err := io.ReadFull(r, s); err != nil {
		return nil, fmt.Errorf("helperinvite: read secret: %w", err)
	}
	return s, nil
}

func expand(s []byte, info string) []byte {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, s, nil, []byte(info)), out); err != nil {
		panic(err) // HKDF-SHA256 cannot fail for 32 bytes of output
	}
	return out
}

// InviteHash is what lantern-cloud stores in place of the secret.
func InviteHash(s []byte) []byte {
	h := sha256.Sum256(s)
	return h[:]
}

// DeriveKey is K, the key of the matching code.
func DeriveKey(s []byte) []byte { return expand(s, infoKey) }

// ApprovalKey is the key an inviter signs an approval with.
func ApprovalKey(s []byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(expand(s, infoApprove))
}

// ApprovalPublic is ApprovalKey's public half, registered with the invite.
func ApprovalPublic(s []byte) []byte {
	return []byte(ApprovalKey(s).Public().(ed25519.PublicKey))
}

// ApprovalMessage is what an approval signature covers.
func ApprovalMessage(inviteHash, volunteerPub []byte) []byte {
	msg := make([]byte, 0, len("approve")+len(inviteHash)+len(volunteerPub))
	msg = append(msg, "approve"...)
	msg = append(msg, inviteHash...)
	return append(msg, volunteerPub...)
}

// Approve signs the approval of volunteerPub for the invite with secret s.
func Approve(s, volunteerPub []byte) []byte {
	return ed25519.Sign(ApprovalKey(s), ApprovalMessage(InviteHash(s), volunteerPub))
}

// MatchingCode is the four-digit code both people compare.
func MatchingCode(k, volunteerPub []byte) string {
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte("code"))
	mac.Write(volunteerPub)
	return fmt.Sprintf("%04d", binary.BigEndian.Uint32(mac.Sum(nil)[:4])%10000)
}

// VolunteerNameKey is K_v, which seals the volunteer's name for the inviter.
func VolunteerNameKey(s []byte) []byte { return expand(s, infoVolunteerName) }

// InviterNameKey is K_i, which seals the inviter's name for the volunteer.
func InviterNameKey(s []byte) []byte { return expand(s, infoInviterName) }

// DeviceKey is the volunteer device's keypair for a 32-byte seed.
func DeviceKey(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("helperinvite: a device seed is 32 bytes")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
