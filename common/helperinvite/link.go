package helperinvite

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// LinkPrefix starts every invite link. The secret is in the fragment, so
// browsers and link-preview fetchers never send it to a server.
const LinkPrefix = "https://lantern.io/help/#"

// ErrLink is returned for anything that is not a helper invite link.
var ErrLink = errors.New("helperinvite: not a helper invite link")

// Link is the invite link for secret s.
func Link(s []byte) string { return LinkPrefix + base64.RawURLEncoding.EncodeToString(s) }

// ParseLink returns the secret in an invite link. It accepts
// https://lantern.io/help/#s and https://www.lantern.io/help/#s, with or
// without the slash after help, or the bare base64url secret someone pasted.
// Surrounding white space is ignored.
func ParseLink(link string) ([]byte, error) {
	link = strings.TrimSpace(link)
	frag := link
	if strings.Contains(link, "://") {
		u, err := url.Parse(link)
		if err != nil || u.Scheme != "https" || (u.Host != "lantern.io" && u.Host != "www.lantern.io") ||
			(u.Path != "/help" && u.Path != "/help/") || u.RawQuery != "" {
			return nil, ErrLink
		}
		frag = u.Fragment
	}
	s, err := base64.RawURLEncoding.Strict().DecodeString(frag)
	if err != nil || len(s) != SecretLen {
		return nil, ErrLink
	}
	return s, nil
}
