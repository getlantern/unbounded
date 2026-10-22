package helperticket

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// vectors is the "ticket" section of lantern-cloud's
// cmd/api/pairing/testdata/vectors.json.
func vectors(t testing.TB) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Ticket map[string]string `json:"ticket"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Ticket
}

func unixAt(t *testing.T, s string) time.Time {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.Unix(n, 0)
}

func pubFromHex(t *testing.T, s string) ed25519.PublicKey {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		t.Fatalf("bad public key %q: %v", s, err)
	}
	return b
}

// lanternCloudTestKey is lantern-cloud's pairing test key, seed 0x09 x 32.
func lanternCloudTestKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
}

// signRaw signs an arbitrary payload, for tickets Sign refuses to make.
func signRaw(priv ed25519.PrivateKey, payload string) string {
	return b64.EncodeToString([]byte(payload)) + "." + b64.EncodeToString(ed25519.Sign(priv, []byte(payload)))
}

// setUnusedBit re-encodes a 64-byte signature with its last character's
// lowest bit, one the decoder discards, flipped.
func setUnusedBit(sig string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	i := strings.IndexByte(alphabet, sig[len(sig)-1])
	return sig[:len(sig)-1] + string(alphabet[i^1])
}

func TestVectors(t *testing.T) {
	v := vectors(t)
	cur := NewKey(pubFromHex(t, v["public_key_hex"]))
	prev := NewKey(pubFromHex(t, v["previous_public_key_hex"]))
	if cur.ID != v["kid"] || prev.ID != v["previous_kid"] {
		t.Fatalf("key IDs %s, %s; vectors say %s, %s", cur.ID, prev.ID, v["kid"], v["previous_kid"])
	}
	at := unixAt(t, v["verify_at_unix"])
	exp := unixAt(t, v["expired_at_unix"])
	keys := []Key{cur, prev}

	tk, err := Verify(keys, v["token"], at)
	if err != nil {
		t.Fatalf("Verify(token): %v", err)
	}
	if tk.PairingID != v["pairing_id"] || tk.Role != RoleVolunteer || tk.KeyID != v["kid"] || !tk.Expires.Equal(exp) {
		t.Errorf("token decoded to %+v", tk)
	}
	enc, _, _ := strings.Cut(v["token"], ".")
	if payload, _ := b64.DecodeString(enc); string(payload) != v["payload_json"] {
		t.Errorf("payload %s, want %s", payload, v["payload_json"])
	}

	if tk, err := Verify(keys, v["inviter_token"], at); err != nil || tk.Role != RoleInviter {
		t.Errorf("inviter_token: %+v, %v", tk, err)
	}
	if tk, err := Verify(keys, v["previous_token"], at); err != nil || tk.KeyID != v["previous_kid"] {
		t.Errorf("previous_token with both keys: %+v, %v", tk, err)
	}
	if _, err := Verify([]Key{cur}, v["previous_token"], at); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("previous_token without the previous key: %v, want ErrUnknownKey", err)
	}
	if _, err := Verify(keys, v["token"], exp); !errors.Is(err, ErrExpired) {
		t.Errorf("token at its expiry: %v, want ErrExpired", err)
	}

	// The signer is lantern-cloud's format byte for byte: ed25519 is
	// deterministic, so signing the same ticket reproduces the token.
	priv := lanternCloudTestKey()
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), cur.Public) {
		t.Fatal("seed 0x09 is not the vectors' key")
	}
	got, err := Sign(priv, Ticket{PairingID: v["pairing_id"], Role: RoleVolunteer, Expires: exp})
	if err != nil || got != v["token"] {
		t.Errorf("Sign = %s, %v; want the vector token", got, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	priv := lanternCloudTestKey()
	keys := []Key{NewKey(priv.Public().(ed25519.PublicKey))}
	now := time.Unix(1_800_000_000, 0)
	good, err := Sign(priv, Ticket{PairingID: "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab", Role: RoleInviter, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	payload, sig, _ := strings.Cut(good, ".")
	const p = `"p":"6f1c2b0e-8a7d-4c1e-9f00-0123456789ab"`
	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"no separator":        {"abc", ErrInvalid},
		"tampered payload":    {"f" + good[1:], ErrInvalid},
		"short signature":     {payload + "." + sig[:20], ErrInvalid},
		"too long":            {good + strings.Repeat("A", 512), ErrInvalid},
		"version 2":           {signRaw(priv, `{"v":2,"k":"dbc29825",`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"no key id":           {signRaw(priv, `{"v":1,`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"non-canonical uuid":  {signRaw(priv, `{"v":1,"k":"dbc29825","p":"6F1C2B0E-8A7D-4C1E-9F00-0123456789AB","r":"inviter","e":1800000060}`), ErrInvalid},
		"unknown role":        {signRaw(priv, `{"v":1,"k":"dbc29825",`+p+`,"r":"admin","e":1800000060}`), ErrInvalid},
		"key id of another":   {signRaw(priv, `{"v":1,"k":"00000000",`+p+`,"r":"inviter","e":1800000060}`), ErrUnknownKey},
		"signed by another":   {signRaw(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)), `{"v":1,"k":"dbc29825",`+p+`,"r":"inviter","e":1800000060}`), ErrInvalid},
		"not JSON":            {signRaw(priv, `not json`), ErrInvalid},
		"base64 with padding": {payload + "==." + sig, ErrInvalid},
		"newline in payload":  {payload[:10] + "\n" + payload[10:] + "." + sig, ErrInvalid},
		"newline in sig":      {payload + "." + sig[:10] + "\r\n" + sig[10:], ErrInvalid},
		"trailing bits set":   {payload + "." + setUnusedBit(sig), ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(keys, tc.token, now); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := Verify(nil, good, now); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("no keys: %v, want ErrUnknownKey", err)
	}
	if _, err := Sign(priv, Ticket{PairingID: "not-a-uuid", Role: RoleInviter}); err == nil {
		t.Error("Sign accepted a malformed pairing ID")
	}
	if _, err := Sign(priv, Ticket{PairingID: "6f1c2b0e-8a7d-4c1e-9f00-0123456789ab", Role: "admin"}); err == nil {
		t.Error("Sign accepted an unknown role")
	}
}

func TestPeek(t *testing.T) {
	v := vectors(t)
	if role, id := Peek(v["inviter_token"]); role != RoleInviter || id != v["pairing_id"] {
		t.Errorf("Peek(inviter_token) = %q, %q", role, id)
	}
	if role, id := Peek(v["token"]); role != RoleVolunteer || id != v["pairing_id"] {
		t.Errorf("Peek(token) = %q, %q", role, id)
	}
	for _, junk := range []string{"", "x", "!!.!!", base64.RawURLEncoding.EncodeToString([]byte(`{"r":"admin"}`)) + ".x"} {
		if role, id := Peek(junk); role != "" || id != "" {
			t.Errorf("Peek(%q) = %q, %q; want nothing", junk, role, id)
		}
	}
}

func TestRoomFunc(t *testing.T) {
	v := vectors(t)
	keys := []Key{NewKey(pubFromHex(t, v["public_key_hex"]))}
	now := unixAt(t, v["verify_at_unix"])
	room := RoomFunc(func() []Key { return keys }, func() time.Time { return now })

	if got, ok := room(v["inviter_token"], RoleInviter); !ok || got != v["pairing_id"] {
		t.Errorf("inviter ticket as inviter: %q, %v", got, ok)
	}
	if _, ok := room(v["inviter_token"], RoleVolunteer); ok {
		t.Error("an inviter ticket admitted a volunteer")
	}
	now = unixAt(t, v["expired_at_unix"])
	if _, ok := room(v["inviter_token"], RoleInviter); ok {
		t.Error("an expired ticket admitted its bearer")
	}
}

// FuzzVerify feeds Verify and Peek arbitrary tokens: neither may panic, and
// anything Verify accepts must be a well-formed ticket that re-signs to itself.
func FuzzVerify(f *testing.F) {
	priv := lanternCloudTestKey()
	keys := []Key{NewKey(priv.Public().(ed25519.PublicKey))}
	now := time.Unix(1_800_000_000, 0)
	for _, s := range []string{"", ".", "abc", "a.b.c", "!!.!!"} {
		f.Add(s)
	}
	for _, tok := range vectors(f) {
		f.Add(tok)
	}
	f.Fuzz(func(t *testing.T, token string) {
		Peek(token)
		tk, err := Verify(keys, token, now)
		if err != nil {
			return
		}
		again, err := Sign(priv, tk)
		if err != nil || again != token {
			t.Fatalf("accepted %q, which re-signs to %q, %v", token, again, err)
		}
	})
}

func TestExpiry(t *testing.T) {
	v := vectors(t)
	if exp, ok := Expiry(v["token"]); !ok || !exp.Equal(unixAt(t, v["expired_at_unix"])) {
		t.Errorf("Expiry(token) = %v, %v; want %s", exp, ok, v["expired_at_unix"])
	}
	for _, junk := range []string{"", "x", "!!.!!", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1}`)) + ".x"} {
		if _, ok := Expiry(junk); ok {
			t.Errorf("Expiry(%q) claimed an expiry", junk)
		}
	}
}
