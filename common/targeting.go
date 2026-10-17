package common

import "slices"

// Volunteer country targeting: a producer (volunteer) may prefer to serve consumers from
// particular countries. Freddie honors the preference when it can and ignores it when it can't,
// so it is strictly a preference, never a filter — a targeting volunteer is never left idle.
//
// Both fields ride outside the JSON signaling payload so Freddie stays payload-agnostic, and both
// are optional: an old Freddie ignores them, and an old client simply never sends them.
const (
	// ConsumerCountryParam is the query parameter on GET /v1/signal by which a consumer declares
	// its country so Freddie can index it.
	ConsumerCountryParam = "country"

	// TargetCountryField is the (repeatable) form field on a genesis POST naming a country the
	// producer prefers to serve.
	TargetCountryField = "target-country"

	// MaxTargetCountries bounds how many target countries Freddie will consider per genesis
	// message, so a client can't make one POST fan out across an arbitrary number of lookups.
	MaxTargetCountries = 8
)

// NormalizeCountry is normalizeCountry for callers outside this package, so every hop agrees on
// what counts as a country code: a two-letter uppercase code, or "" for "not supplied".
func NormalizeCountry(country string) string {
	return normalizeCountry(country)
}

// NormalizeCountries normalizes a list of untrusted country codes, dropping invalid entries and
// duplicates and truncating to MaxTargetCountries. Returns nil if nothing valid remains.
func NormalizeCountries(countries []string) []string {
	var out []string
	for _, c := range countries {
		if len(out) == MaxTargetCountries {
			break
		}
		c = normalizeCountry(c)
		if c == "" || slices.Contains(out, c) {
			continue
		}
		out = append(out, c)
	}
	return out
}
