package common

// A producer says what kind of volunteer it is on every genesis, so Freddie can count each kind's
// connect rate and later prefer the kinds that connect best. Like the outcome fields these carry
// only enumerations, and a producer that predates them sends nothing.
const (
	// VolunteerKindField is one of the VolunteerKind* values.
	VolunteerKindField = "volunteer-kind"
	// VolunteerMappedField is "1" when the producer's WebRTC traffic runs through a port mapped on
	// its router and verified from outside, and "0" otherwise. It is sent with
	// VolunteerKindField.
	VolunteerMappedField = "volunteer-mapped"
)

// Volunteer kinds.
const (
	// VolunteerKindBrowser is the widget running in a web page.
	VolunteerKindBrowser = "browser"
	// VolunteerKindApp is the Lantern app's volunteer, on a laptop, desktop or phone.
	VolunteerKindApp = "app"
	// VolunteerKindServer is a Lantern node: lantern-box's volunteer service on a home server or a
	// VPS.
	VolunteerKindServer = "server"
)

var volunteerKinds = map[string]bool{
	VolunteerKindBrowser: true,
	VolunteerKindApp:     true,
	VolunteerKindServer:  true,
}

// NormalizeVolunteerKind maps a reported kind onto the known set, "unreported" when the producer
// sent none, or "invalid".
func NormalizeVolunteerKind(s string) string {
	if s == "" {
		return "unreported"
	}
	return normalizeEnum(s, volunteerKinds)
}

// NormalizeVolunteerMapped maps a reported mapped flag onto "true", "false", "unreported" when the
// producer sent none, or "invalid".
func NormalizeVolunteerMapped(s string) string {
	switch s {
	case "1":
		return "true"
	case "0":
		return "false"
	case "":
		return "unreported"
	}
	return "invalid"
}
