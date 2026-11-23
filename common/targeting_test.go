package common

import (
	"slices"
	"testing"
)

func TestNormalizeCountries(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{""}, nil},
		{[]string{"ir"}, []string{"IR"}},
		{[]string{"ir", "IR", "Ir"}, []string{"IR"}},
		{[]string{"zz", "irn", "1R", "ru"}, []string{"RU"}},
		{[]string{"cn", "ru", "ir"}, []string{"CN", "RU", "IR"}},
	}
	for _, c := range cases {
		if got := NormalizeCountries(c.in); !slices.Equal(got, c.want) {
			t.Errorf("NormalizeCountries(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeCountries_Caps(t *testing.T) {
	in := []string{"AA", "AB", "AC", "AD", "AE", "AF", "AG", "AH", "AI", "AJ", "AK"}
	got := NormalizeCountries(in)
	if len(got) != MaxTargetCountries {
		t.Fatalf("got %d countries, want %d", len(got), MaxTargetCountries)
	}
	// AA is declined as a non-country, so the cap counts valid entries only
	if got[0] != "AB" {
		t.Errorf("got[0] = %q, want AB", got[0])
	}
}
