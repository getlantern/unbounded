package helperinvite

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func eq[T comparable](t *testing.T, want, got T, what string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", what, want, got)
	}
}

func isErr(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: want %v, got %v", what, target, err)
	}
}

// TestLanternCloudVectors checks every derivation against lantern-cloud's own
// vectors, so the app, the browser and the server agree byte for byte.
func TestLanternCloudVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/lantern-cloud-vectors.json")
	must(t, err, "read vectors")
	var v struct {
		Invites []struct {
			SecretB64url    string `json:"secret_b64url"`
			InviteHashHex   string `json:"invite_hash_hex"`
			KeyHex          string `json:"key_hex"`
			ApprovalPubHex  string `json:"approval_pub_hex"`
			VolunteerPubHex string `json:"volunteer_pub_hex"`
			MatchingCode    string `json:"matching_code"`
			ApprovalSigHex  string `json:"approval_sig_hex"`
		} `json:"invites"`
	}
	must(t, json.Unmarshal(data, &v), "parse vectors")
	if len(v.Invites) == 0 {
		t.Fatal("no invite vectors")
	}
	for i, iv := range v.Invites {
		s, err := base64.RawURLEncoding.DecodeString(iv.SecretB64url)
		must(t, err, "decode secret")
		vol := unhex(t, iv.VolunteerPubHex)
		eq(t, iv.InviteHashHex, hex.EncodeToString(InviteHash(s)), "invite hash")
		eq(t, iv.KeyHex, hex.EncodeToString(DeriveKey(s)), "key")
		eq(t, iv.ApprovalPubHex, hex.EncodeToString(ApprovalPublic(s)), "approval public key")
		eq(t, iv.MatchingCode, MatchingCode(DeriveKey(s), vol), "matching code")
		eq(t, iv.ApprovalSigHex, hex.EncodeToString(Approve(s, vol)), "approval signature")
		if t.Failed() {
			t.Fatalf("vector %d", i)
		}
	}
}

type nameVectors struct {
	SecretB64url    string `json:"secret_b64url"`
	NonceHex        string `json:"nonce_hex"`
	VolunteerKeyHex string `json:"volunteer_name_key_hex"`
	InviterKeyHex   string `json:"inviter_name_key_hex"`
	Blobs           []struct {
		Key     string `json:"key"`
		NameHex string `json:"name_hex"`
		BlobHex string `json:"blob_hex"`
	} `json:"blobs"`
	DeviceSeedHex   string `json:"device_seed_hex"`
	DevicePubHex    string `json:"device_pub_hex"`
	DeviceCode      string `json:"device_matching_code"`
	DeviceApprovSig string `json:"device_approval_sig_hex"`
}

func loadNameVectors(t *testing.T) nameVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/names.json")
	must(t, err, "read names.json")
	var v nameVectors
	must(t, json.Unmarshal(data, &v), "parse names.json")
	return v
}

func TestNameVectors(t *testing.T) {
	v := loadNameVectors(t)
	s, err := base64.RawURLEncoding.DecodeString(v.SecretB64url)
	must(t, err, "decode secret")
	eq(t, v.VolunteerKeyHex, hex.EncodeToString(VolunteerNameKey(s)), "volunteer name key")
	eq(t, v.InviterKeyHex, hex.EncodeToString(InviterNameKey(s)), "inviter name key")
	for _, b := range v.Blobs {
		key := VolunteerNameKey(s)
		if b.Key == "inviter" {
			key = InviterNameKey(s)
		}
		name := string(unhex(t, b.NameHex))
		got, err := sealName(key, name, unhex(t, v.NonceHex))
		must(t, err, "seal")
		eq(t, b.BlobHex, hex.EncodeToString(got), "seal "+b.NameHex)
		opened, err := OpenName(key, unhex(t, b.BlobHex))
		must(t, err, "open")
		eq(t, name, opened, "open "+b.NameHex)
	}
	priv, err := DeviceKey(unhex(t, v.DeviceSeedHex))
	must(t, err, "device key")
	pubBytes := []byte(priv[32:]) // an ed25519.PrivateKey ends with its public key
	eq(t, v.DevicePubHex, hex.EncodeToString(pubBytes), "device public key")
	eq(t, v.DeviceCode, MatchingCode(DeriveKey(s), pubBytes), "device matching code")
	eq(t, v.DeviceApprovSig, hex.EncodeToString(Approve(s, pubBytes)), "device approval signature")

	_, err = DeviceKey(make([]byte, 31))
	if err == nil {
		t.Error("a 31-byte seed must be refused")
	}
}

func TestNamesSealUnderTheirOwnKeyOnly(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	must(t, err, "new secret")
	blob, err := SealName(VolunteerNameKey(s), "  Ali  ", rand.Reader)
	must(t, err, "seal")
	name, err := OpenName(VolunteerNameKey(s), blob)
	must(t, err, "open")
	eq(t, "Ali", name, "surrounding spaces are trimmed")
	_, err = OpenName(InviterNameKey(s), blob)
	isErr(t, err, ErrBlob, "the inviter key cannot open the volunteer's name")
	blob[len(blob)-1] ^= 1
	_, err = OpenName(VolunteerNameKey(s), blob)
	isErr(t, err, ErrBlob, "a tampered blob does not open")
	_, err = OpenName(VolunteerNameKey(s), blob[:10])
	isErr(t, err, ErrBlob, "a short blob does not open")
	_, err = OpenName(make([]byte, 5), blob)
	isErr(t, err, ErrBlob, "a bad key does not open")
	_, err = NewSecret(bytes.NewReader(make([]byte, 5)))
	if err == nil {
		t.Error("a short reader must fail NewSecret")
	}
}

func TestCleanName(t *testing.T) {
	for _, bad := range []string{"", "   ", "a\x00b", "line\nbreak", strings.Repeat("a", MaxNameBytes+1), string([]byte{0xff}),
		"\u202eilA", "Al\u200bi", "\ufeffAli", "A\u2066li", "A\u00adli", "A\u2060li", "A\u061cli"} {
		_, err := CleanName(bad)
		isErr(t, err, ErrName, "CleanName "+strconv.QuoteToASCII(bad))
	}
	zwnjName := "\u0645\u06cc\u200c\u0631\u0627" // Persian, with a zero-width non-joiner
	got, err := CleanName(zwnjName)
	must(t, err, "ZWNJ is part of Persian spelling")
	eq(t, zwnjName, got, "ZWNJ name")
	_, err = CleanName("\U0001f469\u200d\U0001f4bb")
	must(t, err, "ZWJ joins emoji")
	got, err = CleanName(" \u0633\u0627\u0631\u0627 ")
	must(t, err, "Persian name")
	eq(t, "\u0633\u0627\u0631\u0627", got, "trimmed Persian name")
	got, err = CleanName(strings.Repeat("a", MaxNameBytes))
	must(t, err, "longest name")
	eq(t, MaxNameBytes, len(got), "longest name length")
	blob, err := SealName(InviterNameKey(make([]byte, SecretLen)), got, rand.Reader)
	must(t, err, "seal longest name")
	if len(blob) > 512 {
		t.Errorf("the largest blob is %d bytes, over lantern-cloud's 512", len(blob))
	}
}

func TestLinks(t *testing.T) {
	s := bytes.Repeat([]byte{0xfb}, SecretLen)
	link := Link(s)
	if !strings.HasPrefix(link, LinkPrefix) {
		t.Fatalf("link %q lacks the prefix", link)
	}
	enc := strings.TrimPrefix(link, LinkPrefix)
	for _, in := range []string{
		link,
		"  " + link + "\n",
		"https://www.lantern.io/help/#" + enc,
		"https://lantern.io/help#" + enc,
		enc,
	} {
		got, err := ParseLink(in)
		must(t, err, in)
		if !bytes.Equal(s, got) {
			t.Errorf("%s: wrong secret", in)
		}
	}
	for _, bad := range []string{
		"",
		"https://evil.example/help/#" + enc,
		"http://lantern.io/help/#" + enc,
		"https://lantern.io/other/#" + enc,
		"https://lantern.io/help/?s=" + enc,
		"https://lantern.io/help/#" + enc[:20],
		"https://lantern.io/help/#" + enc + "==",
		enc + "x",
	} {
		_, err := ParseLink(bad)
		isErr(t, err, ErrLink, bad)
	}
}

// TestClaimAndApprovalRoundTrip runs both sides with the package alone: the
// volunteer's claim and the inviter's approval agree on the code, the
// signature verifies, and each name opens on the other side.
func TestClaimAndApprovalRoundTrip(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	must(t, err, "new secret")
	seed := bytes.Repeat([]byte{7}, 32)
	c, err := NewClaim(s, seed, "Ali", rand.Reader)
	must(t, err, "new claim")
	if !bytes.Equal(InviteHash(s), c.InviteHash) {
		t.Error("claim invite hash")
	}
	if !regexp.MustCompile(`^[0-9]{4}$`).MatchString(c.Code) {
		t.Errorf("code %q is not four digits", c.Code)
	}
	name, err := OpenName(VolunteerNameKey(s), c.VolunteerBlob)
	must(t, err, "open volunteer name")
	eq(t, "Ali", name, "volunteer name")
	eq(t, c.Code, MatchingCode(DeriveKey(s), c.VolunteerPubkey), "the inviter computes the same code")

	a, err := NewApproval(s, c.VolunteerPubkey, "Sara", rand.Reader)
	must(t, err, "new approval")
	if !bytes.Equal(Approve(s, c.VolunteerPubkey), a.Signature) {
		t.Error("approval signature")
	}
	inviter, err := OpenName(InviterNameKey(s), a.InviterBlob)
	must(t, err, "open inviter name")
	eq(t, "Sara", inviter, "inviter name")

	_, err = NewClaim(s[:5], seed, "Ali", rand.Reader)
	isErr(t, err, ErrSecret, "short secret claim")
	_, err = NewClaim(s, seed, "", rand.Reader)
	isErr(t, err, ErrName, "empty name claim")
	_, err = NewClaim(s, seed[:5], "Ali", rand.Reader)
	if err == nil {
		t.Error("a short seed must fail NewClaim")
	}
	_, err = NewApproval(s, c.VolunteerPubkey[:5], "Sara", rand.Reader)
	if err == nil {
		t.Error("a short volunteer key must fail NewApproval")
	}
	_, err = NewApproval(s[:5], c.VolunteerPubkey, "Sara", rand.Reader)
	isErr(t, err, ErrSecret, "short secret approval")
}
