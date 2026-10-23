package helperinvite

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
		"\u202eilA", "Al\u200bi", "\ufeffAli", "A\u2066li", "A\u00adli", "A\u2060li", "A\u061cli",
		"A\u2028li", "A\u2029li", "A\ue000li", "A\U000f0000li", "A\U00100000li"} {
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
		"https://www.lantern.io/help#" + enc,
		"HTTPS://lantern.io/help/#" + enc,
		"Https://www.lantern.io/help/#" + enc,
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
		"https://evil.example@lantern.io/help/#" + enc,
		"https://lantern.io@evil.example/help/#" + enc,
		"https://lantern.io:443/help/#" + enc,
		"https://lantern.io/help/?#" + enc,
		"https://lantern.io/hel%70/#" + enc,
		"https://lantern.io//help/#" + enc,
		"https://LANTERN.IO/help/#" + enc,
		"https://lantern.io/help/#" + enc[:10] + "\n" + enc[10:],
		"https://lantern.io/help/#" + enc[:10] + "\r\n" + enc[10:],
		enc[:10] + "\n" + enc[10:],
		"https://lantern.io/help/#" + enc[:10] + "%0A" + enc[10:],
		"https://lantern.io/help/#" + enc[:10] + "%0d" + enc[10:],
		"https://lantern.io/help/#" + fmt.Sprintf("%%%02X", enc[0]) + enc[1:],
		"https://lantern.io/help/##" + enc,
		"https://lantern.io/help/#" + enc + "#",
		"lantern.io/help/#" + enc,
		"https:" + enc,
		enc[:42] + string(b64url[strings.IndexByte(b64url, enc[42])|1]),
		link + strings.Repeat(" ", maxLinkLen),
		strings.Repeat(" ", maxLinkLen) + link,
	} {
		_, err := ParseLink(bad)
		isErr(t, err, ErrLink, bad)
	}
}

const b64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// TestLinkTrailingBits pins that a secret has one encoding: the last
// character's two spare bits must be zero.
func TestLinkTrailingBits(t *testing.T) {
	enc := strings.TrimPrefix(Link(bytes.Repeat([]byte{0xfb}, SecretLen)), LinkPrefix)
	if strings.IndexByte(b64url, enc[42])&3 != 0 {
		t.Fatalf("canonical last character %q has spare bits set", enc[42])
	}
	if enc[42] == b64url[strings.IndexByte(b64url, enc[42])|1] {
		t.Fatal("the mutated last character must differ")
	}
}

func TestLinkLengthBound(t *testing.T) {
	longest := "https://www.lantern.io/help/#" + strings.TrimPrefix(Link(make([]byte, SecretLen)), LinkPrefix)
	if len(longest) > maxLinkLen/2 {
		t.Errorf("maxLinkLen %d leaves too little room over the %d-byte longest link", maxLinkLen, len(longest))
	}
	padded := longest + strings.Repeat(" ", maxLinkLen-len(longest))
	if _, err := ParseLink(padded); err != nil {
		t.Errorf("a padded link of exactly maxLinkLen bytes must parse: %v", err)
	}
}

// TestSecretLenEnforced pins that the helpers taking a secret refuse one of
// the wrong length with a zero value rather than deriving from it.
func TestSecretLenEnforced(t *testing.T) {
	for _, s := range [][]byte{nil, make([]byte, SecretLen-1), make([]byte, SecretLen+1)} {
		pub := make([]byte, 32)
		eq(t, "", Link(s), "Link")
		eq(t, 0, len(InviteHash(s)), "InviteHash")
		eq(t, 0, len(DeriveKey(s)), "DeriveKey")
		eq(t, 0, len(ApprovalKey(s)), "ApprovalKey")
		eq(t, 0, len(ApprovalPublic(s)), "ApprovalPublic")
		eq(t, 0, len(Approve(s, pub)), "Approve")
		eq(t, 0, len(VolunteerNameKey(s)), "VolunteerNameKey")
		eq(t, 0, len(InviterNameKey(s)), "InviterNameKey")
	}
	eq(t, "", MatchingCode(make([]byte, 31), make([]byte, 32)), "MatchingCode short key")
	eq(t, "", MatchingCode(make([]byte, 32), make([]byte, 31)), "MatchingCode short volunteer key")
	eq(t, 0, len(Approve(make([]byte, SecretLen), make([]byte, 31))), "Approve short volunteer key")
	eq(t, 0, len(ApprovalMessage(make([]byte, 31), make([]byte, 32))), "ApprovalMessage short hash")
	eq(t, 0, len(ApprovalMessage(make([]byte, 32), make([]byte, 31))), "ApprovalMessage short volunteer key")
}

// lanternCloudVectorsSHA256 pins testdata/lantern-cloud-vectors.json. When
// lantern-cloud regenerates cmd/api/pairing/testdata/vectors.json, re-copy it
// verbatim from lantern-cloud's main branch and update this hash.
const lanternCloudVectorsSHA256 = "6320f5bde6da73304b57a1d6116d2fc578708cd15c3c59ba506a77e2e032173f"

func TestLanternCloudVectorsPinned(t *testing.T) {
	data, err := os.ReadFile("testdata/lantern-cloud-vectors.json")
	must(t, err, "read vectors")
	sum := sha256.Sum256(data)
	eq(t, lanternCloudVectorsSHA256, hex.EncodeToString(sum[:]),
		"lantern-cloud-vectors.json changed: re-copy it from lantern-cloud and update the pinned hash")
}

func TestNameErrorPaths(t *testing.T) {
	key := VolunteerNameKey(make([]byte, SecretLen))
	_, err := SealName(key, "Ali", bytes.NewReader(make([]byte, 5)))
	if err == nil || errors.Is(err, ErrName) {
		t.Errorf("a failed nonce read: got %v", err)
	}
	_, err = sealName(make([]byte, 5), "Ali", make([]byte, 12))
	isErr(t, err, ErrKey, "sealName with a short key")
	_, err = SealName(make([]byte, 5), "Ali", rand.Reader)
	isErr(t, err, ErrKey, "SealName with a short key")
	bad, err := sealName(key, "\u202e", make([]byte, 12))
	must(t, err, "seal an invalid name directly")
	_, err = OpenName(key, bad)
	isErr(t, err, ErrBlob, "a blob that opens to an invalid name")
	_, err = DeviceKey(make([]byte, 31))
	isErr(t, err, ErrKey, "a short device seed")
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
	isErr(t, err, ErrKey, "a short seed")
	_, err = NewApproval(s, c.VolunteerPubkey[:5], "Sara", rand.Reader)
	isErr(t, err, ErrKey, "a short volunteer key")
	_, err = NewApproval(s, c.VolunteerPubkey, "", rand.Reader)
	isErr(t, err, ErrName, "an empty inviter name")
	_, err = NewApproval(s, c.VolunteerPubkey, "Sa\u202era", rand.Reader)
	isErr(t, err, ErrName, "an inviter name with a bidi override")
	_, err = NewApproval(s[:5], c.VolunteerPubkey, "Sara", rand.Reader)
	isErr(t, err, ErrSecret, "short secret approval")
}
