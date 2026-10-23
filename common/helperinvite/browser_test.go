package helperinvite

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// TestBrowserClaim checks the string API the landing page uses: the body is
// exactly what lantern-cloud's claim endpoint takes, in standard base64 as
// protojson reads it.
func TestBrowserClaim(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	must(t, err, "new secret")
	seedB64, err := BrowserNewSeed(rand.Reader)
	must(t, err, "new seed")
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	must(t, err, "decode seed")
	eq(t, 32, len(seed), "seed length")

	secret := base64.RawURLEncoding.EncodeToString(s)
	body, code, err := BrowserClaim(secret, seedB64, "Ali", rand.Reader)
	must(t, err, "claim")

	var fields map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(body), &fields), "parse body fields")
	var names []string
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	eq(t, "invite_hash,volunteer_blob,volunteer_pubkey", strings.Join(names, ","),
		"the body has exactly HelperClaimInviteRequest's fields")

	var req struct {
		InviteHash      []byte `json:"invite_hash"`
		VolunteerPubkey []byte `json:"volunteer_pubkey"`
		VolunteerBlob   []byte `json:"volunteer_blob"`
	}
	must(t, json.Unmarshal([]byte(body), &req), "parse body")
	if !bytes.Equal(InviteHash(s), req.InviteHash) {
		t.Error("invite hash")
	}
	priv, err := DeviceKey(seed)
	must(t, err, "device key")
	if !bytes.Equal([]byte(priv[32:]), req.VolunteerPubkey) {
		t.Error("volunteer public key")
	}
	name, err := OpenName(VolunteerNameKey(s), req.VolunteerBlob)
	must(t, err, "open volunteer name")
	eq(t, "Ali", name, "volunteer name")

	again, err := BrowserMatchingCode(secret, seedB64)
	must(t, err, "matching code")
	eq(t, code, again, "the page can recompute the code after a reload")
	eq(t, MatchingCode(DeriveKey(s), req.VolunteerPubkey), code, "the inviter computes the same code")

	a, err := NewApproval(s, req.VolunteerPubkey, "Sara", rand.Reader)
	must(t, err, "approval")
	inviter, err := BrowserOpenInviterName(secret, base64.StdEncoding.EncodeToString(a.InviterBlob))
	must(t, err, "open inviter name")
	eq(t, "Sara", inviter, "inviter name")
}

func TestBrowserRejectsBadInput(t *testing.T) {
	good, err := BrowserNewSeed(rand.Reader)
	must(t, err, "new seed")
	link := Link(bytes.Repeat([]byte{1}, SecretLen))
	secret := strings.TrimPrefix(link, LinkPrefix)

	_, _, err = BrowserClaim("https://evil.example/#x", good, "Ali", rand.Reader)
	isErr(t, err, ErrLink, "a foreign link")
	_, _, err = BrowserClaim("", good, "Ali", rand.Reader)
	isErr(t, err, ErrLink, "an empty secret")
	_, _, err = BrowserClaim(link, "not base64!", "Ali", rand.Reader)
	isErr(t, err, ErrKey, "a seed that is not base64")
	_, _, err = BrowserClaim(secret, base64.StdEncoding.EncodeToString(make([]byte, 31)), "Ali", rand.Reader)
	isErr(t, err, ErrKey, "a 31-byte seed")
	_, _, err = BrowserClaim(secret, good, "", rand.Reader)
	isErr(t, err, ErrName, "an empty name")
	_, _, err = BrowserClaim(secret, good, "Ali", bytes.NewReader(nil))
	if err == nil {
		t.Error("a failed nonce read must fail BrowserClaim")
	}

	_, err = BrowserMatchingCode("x", good)
	isErr(t, err, ErrLink, "matching code with a bad secret")
	_, err = BrowserMatchingCode(secret, "")
	isErr(t, err, ErrKey, "matching code with no seed")

	_, err = BrowserOpenInviterName(link, base64.StdEncoding.EncodeToString([]byte("short")))
	isErr(t, err, ErrBlob, "a short blob")
	_, err = BrowserOpenInviterName(secret, "not base64!")
	isErr(t, err, ErrBlob, "a blob that is not base64")
	_, err = BrowserOpenInviterName("x", base64.StdEncoding.EncodeToString(make([]byte, 40)))
	isErr(t, err, ErrLink, "open with a bad secret")

	_, err = BrowserNewSeed(bytes.NewReader(make([]byte, 5)))
	if err == nil {
		t.Error("a short reader must fail BrowserNewSeed")
	}
}

// TestBrowserNamesNotOpenedWithWrongKey pins that the page cannot mistake the
// volunteer's own blob for the inviter's.
func TestBrowserNamesNotOpenedWithWrongKey(t *testing.T) {
	s, err := NewSecret(rand.Reader)
	must(t, err, "new secret")
	blob, err := SealName(VolunteerNameKey(s), "Ali", rand.Reader)
	must(t, err, "seal")
	_, err = BrowserOpenInviterName(base64.RawURLEncoding.EncodeToString(s), base64.StdEncoding.EncodeToString(blob))
	isErr(t, err, ErrBlob, "the volunteer's blob under the inviter key")
}
