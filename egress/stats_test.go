package egress

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getlantern/geo"
)

// resetStats isolates tests from each other and from any package-level state
// left behind by the egress tests that spin up real listeners.
func resetStats(t *testing.T) {
	t.Helper()
	statsMx.Lock()
	statsByCC = map[string]*countryStats{}
	statsMx.Unlock()
	atomic.StoreInt64(&unknownCCs.clients, 0)
	atomic.StoreInt64(&unknownCCs.ingressBytes, 0)
}

func collect(t *testing.T) map[string][2]int64 {
	t.Helper()
	out := map[string][2]int64{}
	eachCountryStats(func(cc string, clients, bytes int64) {
		out[cc] = [2]int64{clients, bytes}
	})
	return out
}

func TestStatsFor_SameCountrySharesOneEntry(t *testing.T) {
	resetStats(t)
	a, b := statsFor("CN"), statsFor("CN")
	if a != b {
		t.Fatal("statsFor returned distinct entries for the same country")
	}
	if statsFor("RU") == a {
		t.Fatal("statsFor returned the same entry for different countries")
	}
}

// An empty country must land in the unknown bucket rather than creating a
// series keyed by the empty string, which reads as missing data in queries.
func TestStatsFor_EmptyCountryGoesToUnknown(t *testing.T) {
	resetStats(t)
	if statsFor("") != unknownCCs {
		t.Fatal("empty country did not map to the unknown bucket")
	}
}

// donorCountry returns the literal unknownCountry, never "", so statsFor must
// route that value to the same block as "". Otherwise the map gains an "unknown"
// key while eachCountryStats also appends its own unknownCountry row, emitting
// two observations with an identical donor_country attribute in one otel
// callback — a duplicate series, with the real traffic in the map entry and the
// always-reported row stuck at zero.
func TestStatsFor_UnknownLiteralRoutesToUnknownBucket(t *testing.T) {
	resetStats(t)
	if statsFor(unknownCountry) != unknownCCs {
		t.Fatal("unknownCountry did not map to the unknown bucket")
	}
	statsMx.Lock()
	_, leaked := statsByCC[unknownCountry]
	statsMx.Unlock()
	if leaked {
		t.Fatalf("statsFor(%q) created a map entry; it must reuse the unknown bucket", unknownCountry)
	}
}

// withNoLookup pins donorGeo to geo.NoLookup for the duration of a test.
//
// These tests need an *unresolvable* peer, and they used to get one for free
// because donorGeo stayed at its init value: GEODB was never set, so initDonorGeo
// returned early and nothing ever replaced it. Since geolocation now defaults on,
// any test that constructs a listener installs a real lookup, and once its 4MB
// download completes mid-suite these assertions start seeing real countries —
// 8.8.8.8 resolves to US. That made the suite depend on whether a network fetch
// finished in time, which showed up as -race-only failures because -race is slow
// enough for it to land.
func withNoLookup(t *testing.T) {
	t.Helper()
	orig := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(orig) })
	setDonorGeo(geo.NoLookup{})
}

// Whatever donorCountry produces for an unresolvable peer must survive a round
// trip through statsFor and eachCountryStats as exactly one series.
func TestEachCountryStats_UnknownEmittedExactlyOnce(t *testing.T) {
	resetStats(t)
	withNoLookup(t)
	s := statsFor(donorCountry(&net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}))
	atomic.AddInt64(&s.clients, 1)
	atomic.AddInt64(&s.ingressBytes, 42)

	counts := map[string]int{}
	var bytesSeen int64
	eachCountryStats(func(cc string, _, bytes int64) {
		counts[cc]++
		if cc == unknownCountry {
			bytesSeen += bytes
		}
	})

	if counts[unknownCountry] != 1 {
		t.Fatalf("unknown series emitted %d times, want exactly 1 (duplicate attribute set)", counts[unknownCountry])
	}
	if bytesSeen != 42 {
		t.Fatalf("unknown series reported %d bytes, want 42 — traffic landed in the wrong bucket", bytesSeen)
	}
}

func TestEachCountryStats_ReportsPerCountryAndDrainsBytes(t *testing.T) {
	resetStats(t)
	cn, ru := statsFor("CN"), statsFor("RU")
	atomic.AddInt64(&cn.clients, 2)
	atomic.AddInt64(&cn.ingressBytes, 500)
	atomic.AddInt64(&ru.clients, 1)
	atomic.AddInt64(&ru.ingressBytes, 100)

	got := collect(t)
	if got["CN"] != [2]int64{2, 500} {
		t.Errorf("CN = %v, want [2 500]", got["CN"])
	}
	if got["RU"] != [2]int64{1, 100} {
		t.Errorf("RU = %v, want [1 100]", got["RU"])
	}

	// Bytes are interval-scoped and must drain, while client counts persist
	// because they describe currently-open connections.
	got = collect(t)
	if got["CN"] != [2]int64{2, 0} {
		t.Errorf("after drain CN = %v, want [2 0]", got["CN"])
	}
	if got["RU"] != [2]int64{1, 0} {
		t.Errorf("after drain RU = %v, want [1 0]", got["RU"])
	}
}

// Idle countries are skipped to bound series count, but "unknown" must always
// report so the metric never disappears entirely. A vanished series is
// indistinguishable from a dead exporter, which is the outage we most need to
// be able to see.
func TestEachCountryStats_AlwaysReportsUnknownEvenWhenIdle(t *testing.T) {
	resetStats(t)
	statsFor("CN") // known but idle

	got := collect(t)
	if _, ok := got[unknownCountry]; !ok {
		t.Fatalf("unknown country series missing from an idle report: %v", got)
	}
	if _, ok := got["CN"]; ok {
		t.Errorf("idle country CN should have been skipped, got %v", got["CN"])
	}
}

// The read path adds bytes concurrently from many connections; the reporting
// callback drains concurrently. No byte may be lost or double counted.
func TestEachCountryStats_NoLostBytesUnderConcurrency(t *testing.T) {
	resetStats(t)
	const writers, perWriter = 8, 1000
	cn := statsFor("CN")

	var drained int64
	stop := make(chan struct{})
	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				eachCountryStats(func(cc string, _, bytes int64) {
					if cc == "CN" {
						atomic.AddInt64(&drained, bytes)
					}
				})
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				atomic.AddInt64(&cn.ingressBytes, 1)
			}
		}()
	}
	wg.Wait()
	close(stop)
	drainWg.Wait()

	// Final drain to sweep anything added after the last concurrent pass.
	eachCountryStats(func(cc string, _, bytes int64) {
		if cc == "CN" {
			atomic.AddInt64(&drained, bytes)
		}
	})

	if got := atomic.LoadInt64(&drained); got != writers*perWriter {
		t.Fatalf("drained %d bytes, want %d (lost or double-counted)", got, writers*perWriter)
	}
}

// Telemetry must never carry the full session identifier, matching the practice
// established by csidPrefix in clientcore/jit_egress_consumer.go. Short inputs
// pass through rather than panicking on the slice bound.
func TestCsidPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"":                                     "",
		"abc":                                  "abc",
		"12345678":                             "12345678",
		"123456789":                            "12345678",
		"0f8b2c1e-4a5d-4c9f-8e7a-1b2c3d4e5f60": "0f8b2c1e",
	} {
		if got := csidPrefix(in); got != want {
			t.Errorf("csidPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// donorGeo is swapped behind an atomic pointer, and the read path must never see
// a nil lookup — including before any listener has run initDonorGeo.
func TestDonorGeoAccessors(t *testing.T) {
	orig := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(orig) })

	// Install NoLookup rather than assuming the package still holds it. init seeds
	// it, but geolocation now defaults on, so any earlier test that built a listener
	// has replaced it with a real one.
	setDonorGeo(geo.NoLookup{})

	if lookupDonorGeo() == nil {
		t.Fatal("lookupDonorGeo returned nil; init should seed geo.NoLookup")
	}
	if got := lookupDonorGeo().CountryCode(net.ParseIP("8.8.8.8")); got != "" {
		t.Errorf("default lookup returned %q, want \"\" (NoLookup)", got)
	}

	setDonorGeo(stubCountryLookup{cc: "CN"})
	if got := donorCountry(&net.TCPAddr{IP: net.ParseIP("8.8.8.8")}); got != "CN" {
		t.Errorf("donorCountry after swap = %q, want CN", got)
	}
}

// stubCountryLookup is a minimal geo.CountryLookup for exercising the swap.
type stubCountryLookup struct{ cc string }

func (s stubCountryLookup) CountryCode(net.IP) string { return s.cc }
func (s stubCountryLookup) Ready() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestDBNameFromURL(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		// The default, and the convention used across the fleet: a mirror URL whose
		// path already carries the member name. Verified against the real object —
		// the tarball contains GeoLite2-Country_20260804/GeoLite2-Country.mmdb, so
		// ".mmdb" is what keepcurrent.FromTarGz must be handed.
		{"lantern mirror (the default)", defaultGeoDBURL, "GeoLite2-Country.mmdb", true},
		{"plain tarball", "https://example.com/dbs/GeoLite2-Country.mmdb.tar.gz", "GeoLite2-Country.mmdb", true},
		// MaxMind's own tarball naming: the path carries no ".mmdb", but the member
		// inside does. Deriving "GeoLite2-Country" matches nothing and degrades
		// silently to unknownCountry, which is what the first .mmdb fix missed by
		// appending in the edition_id branch only.
		{"maxmind-style path without .mmdb", "https://example.com/dbs/GeoLite2-Country.tar.gz", "GeoLite2-Country.mmdb", true},
		{"city edition without .mmdb", "https://example.com/GeoLite2-City.tar.gz", "GeoLite2-City.mmdb", true},
		{"no suffix in path", "https://example.com/dbs/GeoLite2-Country.mmdb", "GeoLite2-Country.mmdb", true},
		{
			// The case that motivated this: MaxMind's real permalink puts the
			// edition in the query, so path.Base over the raw URL would have
			// produced "geoip_download?edition_id=...&license_key=..." and used
			// it as both a tarball member name and a local filename.
			//
			// The expected value is the edition plus ".mmdb", not the bare edition.
			// FromTarGz compares this to each member's base name with ==, so a bare
			// edition id matches nothing in the archive. This test asserted the bare
			// id until 2026-08-06, which made it agree with the code and disagree
			// with every real tarball.
			"maxmind permalink",
			"https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=secret&suffix=tar.gz",
			"GeoLite2-Country.mmdb", true,
		},
		{"signed url with query", "https://cdn.example.com/GeoLite2-Country.mmdb.tar.gz?X-Amz-Signature=deadbeef", "GeoLite2-Country.mmdb", true},
		{"fragment", "https://example.com/GeoLite2-Country.mmdb.tar.gz#frag", "GeoLite2-Country.mmdb", true},
		// TrimSuffix rather than ReplaceAll: the mid-string ".tar.gz" survives. The
		// ".mmdb" on the end is the universal append, which does not disturb that.
		{"suffix mid-string preserved", "https://example.com/my.tar.gz.db.tar.gz", "my.tar.gz.db.mmdb", true},
		{"not a url", "GeoLite2-Country.tar.gz", "", false},
		{"no path", "https://example.com", "", false},
		{"root path", "https://example.com/", "", false},
	} {
		got, ok := dbNameFromURL(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: dbNameFromURL(%q) = (%q, %v), want (%q, %v)", tc.name, tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// With no GEODB configured the lookup is geo.NoLookup, whose CountryCode returns
// "". That must surface as unknownCountry, never as an empty label.
func TestDonorCountry_DefaultsToUnknown(t *testing.T) {
	withNoLookup(t)
	for _, addr := range []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443},
		&net.TCPAddr{}, // no IP
		nil,
		&net.UDPAddr{IP: net.ParseIP("8.8.8.8")}, // not a TCPAddr
	} {
		if got := donorCountry(addr); got != unknownCountry {
			t.Errorf("donorCountry(%v) = %q, want %q", addr, got, unknownCountry)
		}
	}
}

// The default must be usable without configuration, because requiring an env var
// is what produced donor_country=unknown for every connection in production. Pins
// the two properties that make it work: it parses to the member name a real MaxMind
// tarball contains, and it carries no credential.
func TestDefaultGeoDBURL(t *testing.T) {
	name, ok := dbNameFromURL(defaultGeoDBURL)
	if !ok {
		t.Fatalf("the default GEODB URL does not parse: %q", defaultGeoDBURL)
	}
	// Verified against https://storage.googleapis.com/lanterngeo/GeoLite2-Country.mmdb.tar.gz,
	// whose members are GeoLite2-Country_<date>/{GeoLite2-Country.mmdb,COPYRIGHT.txt,LICENSE.txt}.
	if name != "GeoLite2-Country.mmdb" {
		t.Errorf("derived member name = %q, want %q", name, "GeoLite2-Country.mmdb")
	}
	// A URL needing a license key would mean shipping a secret to every egress host
	// to fetch a public database. If this ever gains a query string, that is a
	// decision to make deliberately rather than by editing a constant.
	if strings.Contains(defaultGeoDBURL, "?") || strings.Contains(defaultGeoDBURL, "license") {
		t.Errorf("the default GEODB URL should carry no credential: %q", defaultGeoDBURL)
	}
}

// Exercises the production resolution rule, not a copy of it. The first version of
// this test re-implemented the same conditional and asserted on its own result,
// which passes whether or not initDonorGeoLocked still has the fallback — coverage
// in appearance only.
//
// t.Setenv rather than reading the ambient value: it restores on cleanup and makes
// the unset case reachable even on a machine where GEODB happens to be set, which
// the earlier version could only skip.
func TestResolveGeoDBURL(t *testing.T) {
	t.Run("unset falls back to the default", func(t *testing.T) {
		t.Setenv("GEODB", "")
		if got := resolveGeoDBURL(); got != defaultGeoDBURL {
			t.Errorf("resolveGeoDBURL() = %q, want the default %q", got, defaultGeoDBURL)
		}
	})

	t.Run("set overrides the default", func(t *testing.T) {
		const custom = "https://mirror.example.com/GeoLite2-Country.mmdb.tar.gz"
		t.Setenv("GEODB", custom)
		if got := resolveGeoDBURL(); got != custom {
			t.Errorf("resolveGeoDBURL() = %q, want the configured %q", got, custom)
		}
	})

	// An operator's override must survive the same derivation the default does,
	// otherwise setting GEODB would silently fall back to unknownCountry.
	t.Run("an override still yields a usable member name", func(t *testing.T) {
		t.Setenv("GEODB", "https://mirror.example.com/GeoLite2-Country.mmdb.tar.gz")
		name, ok := dbNameFromURL(resolveGeoDBURL())
		if !ok || name != "GeoLite2-Country.mmdb" {
			t.Errorf("dbNameFromURL(override) = (%q, %v), want (%q, true)", name, ok, "GeoLite2-Country.mmdb")
		}
	})
}

// dbNameFromURL must return a plain filename. Nothing joins it to a directory any
// more — the on-disk cache was removed, so today it is only a tar member name — but
// the guarantee is kept deliberately. It was a root-owned write sink for exactly one
// commit, the edition_id branch takes its value verbatim from a query string, and
// anyone re-adding a cache path would reasonably assume this function returns
// something safe to join.
//
// The earlier check compared against "", ".", "/" and ".." exactly, which a traversal
// walks straight past because it needs separators.
func TestDBNameFromURL_RejectsPathTraversal(t *testing.T) {
	const base = "https://download.maxmind.com/app/geoip_download?suffix=tar.gz&edition_id="
	for name, editionID := range map[string]string{
		"absolute path":      "/etc/shadow",
		"relative traversal": "../../etc/cron.d/evil",
		"single traversal":   "../x",
		"dot dot":            "..",
		"single dot":         ".",
		"bare separator":     "/",
		"trailing separator": "GeoLite2-Country/",
		"embedded separator": "a/b",
		"windows separator":  `..\..\x`,
		"nested traversal":   "GeoLite2-Country/../../../etc/passwd",
	} {
		got, ok := dbNameFromURL(base + url.QueryEscape(editionID))
		if ok {
			// A basename is the contract; anything that could leave TempDir is not one.
			if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
				t.Errorf("%s (edition_id=%q): returned %q, which is not a plain filename", name, editionID, got)
				continue
			}
			// path.Base may legitimately reduce a traversal to a safe leaf
			// ("a/b" -> "b.mmdb"); that is fine, as long as it cannot escape.
			joined := filepath.Join(os.TempDir(), got)
			if !strings.HasPrefix(filepath.Clean(joined), filepath.Clean(os.TempDir())+string(filepath.Separator)) {
				t.Errorf("%s (edition_id=%q): %q joins to %q, outside TempDir", name, editionID, got, joined)
			}
			continue
		}
		// Rejected outright is also a correct outcome.
	}
}

// Every accepted value, from any URL shape, must be safe to join onto a directory.
// Stated as a property rather than a list of payloads so a future change to the
// derivation cannot reintroduce a traversal, whether or not a caller currently joins
// the result to a path.
func TestDBNameFromURL_AlwaysYieldsAContainedPath(t *testing.T) {
	for _, in := range []string{
		defaultGeoDBURL,
		"https://example.com/dbs/GeoLite2-Country.mmdb.tar.gz",
		"https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=k&suffix=tar.gz",
		"https://download.maxmind.com/app/geoip_download?edition_id=" + url.QueryEscape("../../evil") + "&suffix=tar.gz",
		"https://cdn.example.com/GeoLite2-Country.mmdb.tar.gz?X-Amz-Signature=deadbeef",
	} {
		got, ok := dbNameFromURL(in)
		if !ok {
			continue
		}
		joined := filepath.Clean(filepath.Join(os.TempDir(), got))
		if !strings.HasPrefix(joined, filepath.Clean(os.TempDir())+string(filepath.Separator)) {
			t.Errorf("dbNameFromURL(%q) = %q, which joins outside TempDir: %q", in, got, joined)
		}
	}
}

// donor_country read "unknown" for 100% of sessions even with the database loaded,
// because the lookup used r.RemoteAddr — which behind Caddy is always loopback. These
// pin that the forwarded address is used instead.
func TestForwardedDonorIP(t *testing.T) {
	req := func(xff string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr = "[::1]:54321"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	for name, tc := range map[string]struct {
		xff  string
		want string // "" means nil
	}{
		"single entry":        {"203.0.113.7", "203.0.113.7"},
		"with port":           {"203.0.113.7:443", "203.0.113.7"},
		"ipv6":                {"2001:db8::1", "2001:db8::1"},
		"ipv6 bracketed port": {"[2001:db8::1]:443", "2001:db8::1"},
		"spaces":              {"  203.0.113.7  ", "203.0.113.7"},
		"absent":              {"", ""},
		"garbage":             {"not-an-ip", ""},
		"empty entries":       {" , , ", ""},
		// The security-relevant case. Caddy appends the peer it observed, so a donor
		// that sends its own X-Forwarded-For puts a spoofed value on the LEFT and the
		// real address ends up on the right. Reading the left would let any donor
		// choose the country it is reported as.
		"spoofed left, real right":    {"1.2.3.4, 203.0.113.7", "203.0.113.7"},
		"multiple spoofs":             {"9.9.9.9, 8.8.8.8, 203.0.113.7", "203.0.113.7"},
		"trailing garbage falls back": {"203.0.113.7, not-an-ip", "203.0.113.7"},
	} {
		got := forwardedDonorIP(req(tc.xff))
		if tc.want == "" {
			if got != nil {
				t.Errorf("%s: forwardedDonorIP(%q) = %v, want nil", name, tc.xff, got)
			}
			continue
		}
		if got == nil || !got.Equal(net.ParseIP(tc.want)) {
			t.Errorf("%s: forwardedDonorIP(%q) = %v, want %s", name, tc.xff, got, tc.want)
		}
	}
}

// The transport address must keep being used when no proxy supplied one, so a
// deployment without Caddy in front is unaffected.
func TestDonorGeoAddr_FallsBackToTransport(t *testing.T) {
	transport := &net.TCPAddr{IP: net.ParseIP("198.51.100.9"), Port: 9001}

	r := httptest.NewRequest("GET", "/ws", nil)
	if got := donorGeoAddr(r, transport); got != transport {
		t.Errorf("with no X-Forwarded-For, got %v, want the transport addr %v", got, transport)
	}

	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	got := donorGeoAddr(r, transport)
	tcp, ok := got.(*net.TCPAddr)
	if !ok || !tcp.IP.Equal(net.ParseIP("203.0.113.7")) {
		t.Errorf("with X-Forwarded-For set, got %v, want 203.0.113.7", got)
	}
}

// End to end through the real lookup: a loopback peer must yield unknown while the
// forwarded address resolves. This is the exact shape of the production bug.
func TestDonorGeoAddr_LoopbackPeerWithForwardedDonor(t *testing.T) {
	orig := lookupDonorGeo()
	t.Cleanup(func() { setDonorGeo(orig) })
	setDonorGeo(stubCountryLookup{cc: "CN"})

	loopback := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 54321}

	// Without the forwarded header the stub still answers, so use NoLookup to show
	// that a real database would find nothing for loopback.
	setDonorGeo(geo.NoLookup{})
	if got := donorCountry(loopback); got != unknownCountry {
		t.Errorf("loopback with a real lookup = %q, want %q", got, unknownCountry)
	}

	setDonorGeo(stubCountryLookup{cc: "CN"})
	r := httptest.NewRequest("GET", "/ws", nil)
	r.RemoteAddr = "[::1]:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := donorCountry(donorGeoAddr(r, loopback)); got != "CN" {
		t.Errorf("forwarded donor = %q, want CN — the forwarded address was not used", got)
	}
}
