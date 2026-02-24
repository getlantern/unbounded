package clientcore

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewPeerIdentity(t *testing.T) {
	id, err := NewPeerIdentity()
	if err != nil {
		t.Fatalf("NewPeerIdentity() error: %v", err)
	}

	// PeerID should be 64 hex chars (32 bytes)
	peerID := id.PeerID()
	if len(peerID) != 64 {
		t.Errorf("PeerID length = %d, want 64", len(peerID))
	}
	if _, err := hex.DecodeString(peerID); err != nil {
		t.Errorf("PeerID is not valid hex: %v", err)
	}

	// PrivateKeyHex should be 128 hex chars (64 bytes)
	privHex := id.PrivateKeyHex()
	if len(privHex) != 128 {
		t.Errorf("PrivateKeyHex length = %d, want 128", len(privHex))
	}
	if _, err := hex.DecodeString(privHex); err != nil {
		t.Errorf("PrivateKeyHex is not valid hex: %v", err)
	}
}

func TestNewPeerIdentityUniqueness(t *testing.T) {
	id1, err := NewPeerIdentity()
	if err != nil {
		t.Fatalf("NewPeerIdentity() error: %v", err)
	}
	id2, err := NewPeerIdentity()
	if err != nil {
		t.Fatalf("NewPeerIdentity() error: %v", err)
	}
	if id1.PeerID() == id2.PeerID() {
		t.Error("two generated identities have the same PeerID")
	}
}

func TestPeerIdentityFromPrivateKeyHex_RoundTrip(t *testing.T) {
	original, err := NewPeerIdentity()
	if err != nil {
		t.Fatalf("NewPeerIdentity() error: %v", err)
	}

	restored, err := PeerIdentityFromPrivateKeyHex(original.PrivateKeyHex())
	if err != nil {
		t.Fatalf("PeerIdentityFromPrivateKeyHex() error: %v", err)
	}

	if original.PeerID() != restored.PeerID() {
		t.Errorf("PeerID mismatch: original=%s, restored=%s", original.PeerID(), restored.PeerID())
	}
	if original.PrivateKeyHex() != restored.PrivateKeyHex() {
		t.Errorf("PrivateKeyHex mismatch after round-trip")
	}
}

func TestPeerIdentityFromPrivateKeyHex_SignVerify(t *testing.T) {
	id, err := NewPeerIdentity()
	if err != nil {
		t.Fatalf("NewPeerIdentity() error: %v", err)
	}

	restored, err := PeerIdentityFromPrivateKeyHex(id.PrivateKeyHex())
	if err != nil {
		t.Fatalf("PeerIdentityFromPrivateKeyHex() error: %v", err)
	}

	msg := []byte("test message for signing")
	sig := ed25519.Sign(restored.privateKey, msg)

	pubBytes, _ := hex.DecodeString(id.PeerID())
	pub := ed25519.PublicKey(pubBytes)
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("signature verification failed after round-trip")
	}
}

func TestPeerIdentityFromPrivateKeyHex_InvalidHex(t *testing.T) {
	_, err := PeerIdentityFromPrivateKeyHex("not-valid-hex!")
	if err == nil {
		t.Error("expected error for invalid hex, got nil")
	}
}

func TestPeerIdentityFromPrivateKeyHex_WrongLength(t *testing.T) {
	// 32 bytes (64 hex chars) instead of 64 bytes
	shortKey := strings.Repeat("ab", 32)
	_, err := PeerIdentityFromPrivateKeyHex(shortKey)
	if err == nil {
		t.Error("expected error for wrong key length, got nil")
	}
}

func TestPeerIdentityFromPrivateKeyHex_Empty(t *testing.T) {
	_, err := PeerIdentityFromPrivateKeyHex("")
	if err == nil {
		t.Error("expected error for empty string, got nil")
	}
}
