package helperinvite

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// BrowserNewSeed returns a fresh device-key seed in standard base64, for the
// landing page to keep in localStorage.
func BrowserNewSeed(r io.Reader) (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, seed); err != nil {
		return "", fmt.Errorf("helperinvite: read seed: %w", err)
	}
	return base64.StdEncoding.EncodeToString(seed), nil
}

func browserSeed(seedB64 string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: a device seed is 32 bytes of standard base64", ErrKey)
	}
	return seed, nil
}

// BrowserClaim is NewClaim for the landing page. secret is the bare
// base64url secret the page read from its fragment and holds in memory
// (ParseLink also accepts a full link). It returns the JSON body of
// POST /v1/helper/invites/claim and the code to show.
func BrowserClaim(secret, seedB64, name string, r io.Reader) (body, code string, err error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", "", err
	}
	seed, err := browserSeed(seedB64)
	if err != nil {
		return "", "", err
	}
	c, err := NewClaim(s, seed, name, r)
	if err != nil {
		return "", "", err
	}
	b, err := json.Marshal(struct {
		InviteHash      []byte `json:"invite_hash"`
		VolunteerPubkey []byte `json:"volunteer_pubkey"`
		VolunteerBlob   []byte `json:"volunteer_blob"`
	}{c.InviteHash, c.VolunteerPubkey, c.VolunteerBlob})
	if err != nil {
		return "", "", err
	}
	return string(b), c.Code, nil
}

// BrowserMatchingCode recomputes the code for a claim the page already sent.
func BrowserMatchingCode(secret, seedB64 string) (string, error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", err
	}
	seed, err := browserSeed(seedB64)
	if err != nil {
		return "", err
	}
	priv, err := DeviceKey(seed)
	if err != nil {
		return "", err
	}
	return MatchingCode(DeriveKey(s), []byte(priv.Public().(ed25519.PublicKey))), nil
}

// BrowserOpenInviterName opens the inviter_blob a new pairing carries in
// ListPairings, given in standard base64 as lantern-cloud sends it. The page
// must call it before it forgets the secret: the blob comes only once.
func BrowserOpenInviterName(secret, blobB64 string) (string, error) {
	s, err := ParseLink(secret)
	if err != nil {
		return "", err
	}
	blob, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil {
		return "", ErrBlob
	}
	return OpenName(InviterNameKey(s), blob)
}
