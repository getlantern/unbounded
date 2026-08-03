package egress

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
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

// initDonorGeo configures donorGeo from the GEODB environment variable, which
// should be a URL to a gzipped MaxMind tarball. Mirrors netstated's
// configuration (see netstate/d/netstated.go) so operators have one convention
// to learn rather than two.
//
// This intentionally does not block on geolookup.Ready(): the database download
// can take a while, and until it lands CountryCode returns "" and connections
// are labelled unknownCountry. Refusing to serve donors while a telemetry
// database downloads would be the wrong trade.
func initDonorGeo() {
	geoDb := os.Getenv("GEODB")
	if geoDb == "" {
		slog.Debug("GEODB not specified, egress metrics will not carry donor country")
		return
	}

	if _, err := url.ParseRequestURI(geoDb); err != nil {
		slog.Debug(fmt.Sprintf("GEODB %q is not a valid URL, donor country will be %q", geoDb, unknownCountry))
		return
	}

	// The geo API wants the tarball URL, the member filename, and a local cache
	// path as three separate arguments; netstated derives the latter two from
	// the first and we do the same for consistency.
	nameInTarball := strings.ReplaceAll(path.Base(geoDb), ".tar.gz", "")
	donorGeo = geo.FromWeb(geoDb, nameInTarball, 24*time.Hour, nameInTarball, geo.CountryCode)
	slog.Debug(fmt.Sprintf("Using %v to geolocate donors", geoDb))
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
