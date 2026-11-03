package common

import "testing"

func TestNormalizeVolunteerKind(t *testing.T) {
	for in, want := range map[string]string{
		VolunteerKindBrowser: VolunteerKindBrowser,
		VolunteerKindApp:     VolunteerKindApp,
		VolunteerKindServer:  VolunteerKindServer,
		"":                   "unreported",
		"Server":             "invalid",
		"router":             "invalid",
	} {
		if got := NormalizeVolunteerKind(in); got != want {
			t.Errorf("NormalizeVolunteerKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeVolunteerMapped(t *testing.T) {
	for in, want := range map[string]string{"1": "true", "0": "false", "": "unreported", "true": "invalid", "2": "invalid"} {
		if got := NormalizeVolunteerMapped(in); got != want {
			t.Errorf("NormalizeVolunteerMapped(%q) = %q, want %q", in, got, want)
		}
	}
}
