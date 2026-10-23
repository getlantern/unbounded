package helperinvite

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

// MaxNameBytes bounds a display name's UTF-8 length. A blob is a 12-byte
// nonce, the name and a 16-byte tag, so the largest is 228 bytes, under
// lantern-cloud's 512.
const MaxNameBytes = 200

var (
	// ErrName is returned for a name that is empty, too long, not UTF-8, or
	// has control or format characters (other than ZWNJ and ZWJ).
	ErrName = errors.New("helperinvite: a name is 1 to 200 bytes of UTF-8 with no control or format characters")
	// ErrBlob is returned for a blob that does not open under the key.
	ErrBlob = errors.New("helperinvite: the name blob does not open")
)

// CleanName trims surrounding white space and checks what is left.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameBytes || !utf8.ValidString(name) {
		return "", ErrName
	}
	for _, r := range name {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != zwnj && r != zwj) {
			return "", ErrName
		}
	}
	return name, nil
}

// Format characters (Unicode Cf) are refused: bidi overrides and invisible
// marks could make one name display as another. ZWNJ and ZWJ are the
// exceptions, because Persian spelling and emoji need them.
const (
	zwnj = '\u200c'
	zwj  = '\u200d'
)

// SealName encrypts name under key with a random nonce from r.
func SealName(key []byte, name string, r io.Reader) ([]byte, error) {
	name, err := CleanName(name)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, fmt.Errorf("helperinvite: read nonce: %w", err)
	}
	return sealName(key, name, nonce)
}

func sealName(key []byte, name string, nonce []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("helperinvite: name key: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(name)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, []byte(name), nil), nil
}

// OpenName decrypts a blob SealName made under key. A blob that does not
// open, or opens to something that is not a valid name, is ErrBlob.
func OpenName(key, blob []byte) (string, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return "", ErrBlob
	}
	if len(blob) < chacha20poly1305.NonceSize+aead.Overhead() {
		return "", ErrBlob
	}
	pt, err := aead.Open(nil, blob[:chacha20poly1305.NonceSize], blob[chacha20poly1305.NonceSize:], nil)
	if err != nil {
		return "", ErrBlob
	}
	name, err := CleanName(string(pt))
	if err != nil {
		return "", ErrBlob
	}
	return name, nil
}
