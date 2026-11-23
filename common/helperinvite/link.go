package helperinvite

import (
	"encoding/base64"
	"errors"
	"strings"
)

// LinkPrefix starts every invite link. The secret is in the fragment, so
// browsers and link-preview fetchers never send it to a server.
const LinkPrefix = "https://lantern.io/help/#"

// maxLinkLen bounds ParseLink's input. The longest valid link,
// https://www.lantern.io/help/# and 43 characters of secret, is 72 bytes; the
// rest is room for white space pasted around it.
const maxLinkLen = 256

// linkPrefixes are the only forms ParseLink accepts in front of the secret.
// Matching them literally, not through net/url, leaves exactly one spelling
// of each: no userinfo, port, query, percent-encoding or extra fragment.
var linkPrefixes = []string{
	"https://lantern.io/help/#",
	"https://www.lantern.io/help/#",
	"https://lantern.io/help#",
	"https://www.lantern.io/help#",
}

// ErrLink is returned for anything that is not a helper invite link.
var ErrLink = errors.New("helperinvite: not a helper invite link")

// Link is the invite link for secret s, or "" if s is not SecretLen bytes.
func Link(s []byte) string {
	if len(s) != SecretLen {
		return ""
	}
	return LinkPrefix + base64.RawURLEncoding.EncodeToString(s)
}

// ParseLink returns the secret in an invite link. It accepts
// https://lantern.io/help/#s and https://www.lantern.io/help/#s, with or
// without the slash after help, or the bare base64url secret someone pasted.
// Surrounding white space is ignored. The scheme is matched without regard
// to case, as RFC 3986 section 3.1 requires; everything else must be exactly
// as Link writes it.
func ParseLink(link string) ([]byte, error) {
	if len(link) > maxLinkLen {
		return nil, ErrLink
	}
	link = strings.TrimSpace(link)
	// The base64 decoder skips CR and LF, so they must be refused here.
	if strings.ContainsAny(link, "\r\n") {
		return nil, ErrLink
	}
	enc := link
	if strings.Contains(link, ":") {
		const scheme = "https://"
		if len(link) < len(scheme) || !strings.EqualFold(link[:len(scheme)], scheme) {
			return nil, ErrLink
		}
		link = scheme + link[len(scheme):]
		enc = ""
		for _, p := range linkPrefixes {
			if strings.HasPrefix(link, p) {
				enc = link[len(p):]
				break
			}
		}
		if enc == "" {
			return nil, ErrLink
		}
	}
	s, err := base64.RawURLEncoding.Strict().DecodeString(enc)
	if err != nil || len(s) != SecretLen {
		return nil, ErrLink
	}
	return s, nil
}
