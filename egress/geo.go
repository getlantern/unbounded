package egress

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
//
// Held in an atomic pointer rather than a plain package variable. initDonorGeo's
// sync.Once already makes the write happen once, before the first listener serves
// anything, so today every read is ordered after it by a happens-before chain
// through goroutine creation — but that argument is subtle, invisible at the read
// site, and silently broken by any future caller that writes this from elsewhere.
// donorCountry runs once per WebSocket session rather than per packet, so an
// atomic load costs nothing worth measuring and makes the safety local.
var donorGeo atomic.Pointer[geo.CountryLookup]

func init() {
	var l geo.CountryLookup = geo.NoLookup{}
	donorGeo.Store(&l)
}

// setDonorGeo swaps in a new lookup.
func setDonorGeo(l geo.CountryLookup) {
	donorGeo.Store(&l)
}

// lookupDonorGeo returns the current lookup, never nil (init seeds NoLookup).
func lookupDonorGeo() geo.CountryLookup {
	return *donorGeo.Load()
}

// initDonorGeoOnce guards initDonorGeo. NewListener is deliberately safe to call
// more than once per process (multiple embedded listeners, graceful restarts,
// tests), and without this each call would kick off another database download
// and swap donorGeo out from under live connections.
var initDonorGeoOnce sync.Once

// defaultGeoDBURL is Lantern's own mirror of the MaxMind country database, and
// the same source the rest of the fleet reads.
//
// Deliberately a default rather than required configuration. The first version of
// this required GEODB to be set, and the result was that it never was: the egress
// logged "GEODB not specified" on every start and reported donor_country=unknown
// for every connection, which on a dashboard reads as "all our traffic comes from
// nowhere" rather than "this is switched off". Telemetry that needs an env var to
// exist is telemetry that does not exist.
//
// This is the URL lantern-cloud's maxmind package uses (see its cityURL/asnURL
// constants), for two reasons worth keeping:
//
//   - No credential. MaxMind's own download endpoint needs a license key in the
//     query string, so pointing at it would mean distributing a secret to every
//     egress host to fetch a public geolocation database. The mirror is a plain
//     public object — verified anonymously readable — so there is nothing to leak
//     and nothing to rotate.
//   - One source of truth. Country codes attributed here now agree with the ones
//     lantern-cloud attributes; two independently-sourced databases would disagree
//     at the edges and quietly make cross-system comparisons wrong.
//
// The Country edition rather than City: it is a third the size and the egress only
// ever reads CountryCode.
const defaultGeoDBURL = "https://storage.googleapis.com/lanterngeo/GeoLite2-Country.mmdb.tar.gz"

// initDonorGeo configures donorGeo from the GEODB environment variable, falling
// back to defaultGeoDBURL. GEODB should be a URL to a gzipped MaxMind tarball.
// Mirrors netstated's configuration (see netstate/d/netstated.go) so operators have
// one convention to learn rather than two. Safe to call repeatedly; only the first
// call acts.
//
// This intentionally does not block on geolookup.Ready(): the database download
// can take a while, and until it lands CountryCode returns "" and connections
// are labelled unknownCountry. Refusing to serve donors while a telemetry
// database downloads would be the wrong trade — and it is also why defaulting this
// on is safe, since a host that cannot reach the mirror degrades to exactly the
// behavior it had before rather than failing to serve.
func initDonorGeo() {
	initDonorGeoOnce.Do(initDonorGeoLocked)
}

// resolveGeoDBURL returns the database URL to use: GEODB when set, otherwise
// defaultGeoDBURL.
//
// A named function rather than two lines inside initDonorGeoLocked so the rule is
// reachable from a test. initDonorGeo is guarded by a sync.Once and kicks off a real
// download, so it cannot be called from a test to check which URL it picked — and
// the first attempt at covering this instead re-implemented the same conditional in
// the test, which passes whether or not the production fallback exists.
func resolveGeoDBURL() string {
	if u := os.Getenv("GEODB"); u != "" {
		return u
	}
	return defaultGeoDBURL
}

func initDonorGeoLocked() {
	geoDb := resolveGeoDBURL()

	nameInTarball, ok := dbNameFromURL(geoDb)
	if !ok {
		slog.Debug(fmt.Sprintf("Cannot derive a database name from GEODB %q, donor country will be %q", geoDb, unknownCountry))
		return
	}
	// An absolute cache path, not the bare member name. geo.FromWeb persists the
	// database to this path so a restart does not re-download, but a relative path is
	// resolved against the process's working directory — and the egress unit sets no
	// WorkingDirectory and runs as root, so CWD is "/". Passing nameInTarball would
	// drop an 8.7MB /GeoLite2-Country.mmdb at the filesystem root of every egress
	// host, and write a copy into the source tree on every `go test ./egress/...`.
	// Both observed; the second is how it was caught.
	//
	// TempDir rather than a user cache dir: it is writable without depending on HOME,
	// which systemd does not necessarily set. Losing the cache to /tmp cleanup only
	// costs one 4MB download on the next start.
	//
	// netstated has the same relative-path shape (netstate/d/netstated.go) and so the
	// same latent behavior; it just has not been bitten because its GEODB is unset in
	// most deployments.
	cachePath := filepath.Join(os.TempDir(), nameInTarball)
	setDonorGeo(geo.FromWeb(geoDb, nameInTarball, 24*time.Hour, cachePath, geo.CountryCode))
	slog.Debug(fmt.Sprintf("Using %v to geolocate donors, cached at %v", geoDb, cachePath))
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
	//
	// The ".mmdb" is not decoration. keepcurrent.FromTarGz compares this against
	// each tar member's base name with ==, and a MaxMind tarball contains
	// "GeoLite2-Country_20260804/GeoLite2-Country.mmdb" — so a bare edition id
	// matches nothing, walks the whole archive and fails at EOF. The first version
	// of this returned the bare id and could therefore never have worked on the one
	// URL form the fallback exists to support. It went unnoticed because the
	// convention here and in netstated is a mirror URL ending in ".mmdb.tar.gz",
	// where the path already carries the suffix and this branch never runs.
	base := path.Base(u.Path)
	if editionID := u.Query().Get("edition_id"); editionID != "" {
		// path.Base on the edition id too, not just on the URL path. The path branch
		// above is sanitized by construction and this one was not, which is the whole
		// asymmetry: edition_id is taken verbatim from a query string.
		base = path.Base(editionID) + ".mmdb"
	}

	name := strings.TrimSuffix(base, ".tar.gz")

	// The return value must be a plain filename, and this is the one place that can
	// promise it. Callers use it two ways: as a tar member name, and joined onto
	// os.TempDir() as a path this process writes — as root, per the egress unit. A
	// value like "../../etc/cron.d/evil.mmdb" survives filepath.Join by escaping the
	// directory it was joined to, so the guarantee has to be "no separators at all"
	// rather than "not literally dot-dot".
	//
	// The previous check compared against "", ".", "/" and ".." exactly, which a
	// traversal walks straight past: it needs separators, and separators were what
	// went unchecked.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
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
	if cc := lookupDonorGeo().CountryCode(tcpAddr.IP); cc != "" {
		return cc
	}
	return unknownCountry
}
