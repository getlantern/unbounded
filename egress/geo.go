package egress

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/geo"
)

// unknownCountry labels traffic we could not geolocate. It is deliberately a
// real value rather than an empty string: an absent attribute silently drops
// the series out of grouped queries, which makes "we don't know" look identical
// to "no traffic".
const unknownCountry = "unknown"

// donorGeo resolves the country of a connecting donor. It defaults to
// geo.NoLookup, whose CountryCode always returns "", so the egress runs
// unchanged when GEODB is unset — every series is simply labelled
// unknownCountry. Geolocation is observability, never a gate on serving
// traffic.
var donorGeo geo.CountryLookup = geo.NoLookup{}

// initDonorGeoOnce guards initDonorGeo. NewListener is deliberately safe to call
// more than once per process (multiple embedded listeners, graceful restarts,
// tests), and without this each call would kick off another database download
// and swap donorGeo out from under live connections.
var initDonorGeoOnce sync.Once

// initDonorGeo configures donorGeo from the GEODB environment variable, which
// should be a URL to a gzipped MaxMind tarball. Mirrors netstated's
// configuration (see netstate/d/netstated.go) so operators have one convention
// to learn rather than two. Safe to call repeatedly; only the first call acts.
//
// This intentionally does not block on geolookup.Ready(): the database download
// can take a while, and until it lands CountryCode returns "" and connections
// are labelled unknownCountry. Refusing to serve donors while a telemetry
// database downloads would be the wrong trade.
func initDonorGeo() {
	initDonorGeoOnce.Do(initDonorGeoLocked)
}

func initDonorGeoLocked() {
	geoDb := os.Getenv("GEODB")
	if geoDb == "" {
		slog.Debug("GEODB not specified, egress metrics will not carry donor country")
		return
	}

	nameInTarball, ok := dbNameFromURL(geoDb)
	if !ok {
		slog.Debug(fmt.Sprintf("Cannot derive a database name from GEODB %q, donor country will be %q", geoDb, unknownCountry))
		return
	}
	donorGeo = geo.FromWeb(geoDb, nameInTarball, 24*time.Hour, nameInTarball, geo.CountryCode)
	slog.Debug(fmt.Sprintf("Using %v to geolocate donors", geoDb))
}

// dbNameFromURL derives the MaxMind member filename (also used as the local
// cache path) from a GEODB URL.
//
// It reads the URL's *path* rather than the raw string, because MaxMind's own
// download endpoint carries the edition in the query rather than the path:
//
//	https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=...&suffix=tar.gz
//
// A naive path.Base over the whole URL folds the query string into the filename
// and yields an unusable member name and cache path. TrimSuffix rather than
// ReplaceAll so a name containing ".tar.gz" mid-string survives intact.
//
// netstated derives these the naive way (netstate/d/netstated.go) and carries the
// same latent problem; it happens to work there only because its GEODB is a plain
// .tar.gz URL.
func dbNameFromURL(geoDb string) (string, bool) {
	// url.Parse rather than url.ParseRequestURI: the latter is defined over
	// request URIs, which by construction have no fragment, so it leaves "#frag"
	// embedded in Path. url.Parse splits the fragment off properly, at the cost
	// of also accepting relative references — hence the explicit scheme/host
	// check to keep rejecting bare filenames.
	u, err := url.Parse(geoDb)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}

	// Fall back to the edition_id query parameter, which is how the MaxMind
	// permalink names the database when the path cannot.
	base := path.Base(u.Path)
	if editionID := u.Query().Get("edition_id"); editionID != "" {
		base = editionID
	}

	name := strings.TrimSuffix(base, ".tar.gz")
	switch name {
	case "", ".", "/", "..":
		return "", false
	}
	return name, true
}

// donorCountry returns the ISO country code for a donor address, or
// unknownCountry. Callers should resolve this once per WebSocket rather than
// per packet: it is a database lookup, and the read path is hot.
func donorCountry(addr net.Addr) string {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok || tcpAddr == nil || tcpAddr.IP == nil {
		return unknownCountry
	}
	if cc := donorGeo.CountryCode(tcpAddr.IP); cc != "" {
		return cc
	}
	return unknownCountry
}
