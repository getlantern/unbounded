package clientcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// PeerIdentity wraps an Ed25519 keypair that serves as a peer's persistent
// cryptographic identity. The public key is used as the PeerID for bandwidth
// attribution, and the keypair doubles as a Solana wallet (Ed25519 is Solana's
// native curve).
type PeerIdentity struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

// NewPeerIdentity generates a new random Ed25519 keypair.
func NewPeerIdentity() (*PeerIdentity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ed25519 keypair: %w", err)
	}
	return &PeerIdentity{privateKey: priv, publicKey: pub}, nil
}

// PeerIdentityFromPrivateKeyHex reconstructs a PeerIdentity from a hex-encoded
// 64-byte Ed25519 private key (128 hex characters).
func PeerIdentityFromPrivateKeyHex(hexKey string) (*PeerIdentity, error) {
	keyBytes, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decoding hex private key: %w", err)
	}
	if len(keyBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key length: got %d bytes, want %d", len(keyBytes), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(keyBytes)
	pub := priv.Public().(ed25519.PublicKey)
	return &PeerIdentity{privateKey: priv, publicKey: pub}, nil
}

// PeerID returns the hex-encoded 32-byte public key (64 hex characters),
// suitable for use as EgressOptions.PeerID.
func (id *PeerIdentity) PeerID() string {
	return hex.EncodeToString(id.publicKey)
}

// PrivateKeyHex returns the hex-encoded 64-byte private key (128 hex characters),
// the value to persist to disk or localStorage.
func (id *PeerIdentity) PrivateKeyHex() string {
	return hex.EncodeToString(id.privateKey)
}
