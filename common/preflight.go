package common

import "strconv"

// A native producer checks that its STUN servers answer before it sends genesis (the STUN
// preflight), and doesn't send genesis while they don't. So it reports its preflights on the
// genesis that follows the next one that passes: how many failed since its last report, how the
// last failed, and the embedding app's VPN state at that failure and at the pass. Freddie counts
// them, which shows whether failures follow the VPN tunnel without any client telemetry. Like the
// outcome fields, these carry only enumerations and a bounded count.
const (
	// PreflightVPNField is the VPN state when the reported preflight passed. It's sent with every
	// genesis that follows a preflight, so passes give a baseline to compare failures against.
	PreflightVPNField = "preflight-vpn"
	// PrevPreflightFailuresField is how many preflights failed since the producer's last report.
	PrevPreflightFailuresField = "prev-preflight-failures"
	// PrevPreflightOutcomeField is how the last of them failed.
	PrevPreflightOutcomeField = "prev-preflight-outcome"
	// PrevPreflightVPNField is the VPN state when the last of them failed.
	PrevPreflightVPNField = "prev-preflight-vpn"
	// PrevPreflightCauseField is why the last of them failed, when it got no further than its
	// socket: one of the PreflightCause* values.
	PrevPreflightCauseField = "prev-preflight-cause"
)

// STUN preflight outcomes.
const (
	// PreflightOK: a STUN server answered with the producer's mapped address.
	PreflightOK = "ok"
	// PreflightNoReply: requests went out, but no server answered before the timeout. Seen when
	// something on the path drops UDP, such as a firewall or a tunnel that doesn't carry it.
	PreflightNoReply = "no_reply"
	// PreflightNoSocket: no UDP socket could be opened, or no request could be sent on it.
	PreflightNoSocket = "no_socket"
	// PreflightNoServers: no server in the cohort could be parsed or resolved.
	PreflightNoServers = "no_servers"
)

var preflightFailures = map[string]bool{
	PreflightNoReply:   true,
	PreflightNoSocket:  true,
	PreflightNoServers: true,
}

// NormalizePreflightFailure maps a reported preflight failure onto the known set.
func NormalizePreflightFailure(s string) string {
	if s == "" {
		return "unreported"
	}
	return normalizeEnum(s, preflightFailures)
}

// Why a preflight failed with PreflightNoSocket: the step that failed, and the class of the
// operating system's error. Only the class is sent, never the error itself.
const (
	// PreflightCauseOpenTimeout: the socket hadn't opened when the preflight's time ran out.
	PreflightCauseOpenTimeout = "open_timeout"
	// PreflightCauseBuild: the binding request couldn't be built.
	PreflightCauseBuild = "build"
)

// Steps a socket can fail at, and error classes, which make up the other causes as
// "<step>_<class>", such as "send_unreachable".
var (
	preflightCauseSteps   = []string{"listen", "send"}
	preflightCauseClasses = []string{
		// unreachable: no route to the network or host, as while a VPN tunnel's routes change.
		"unreachable",
		// addr_unavailable: the address to bind or send from no longer exists on the host.
		"addr_unavailable",
		// permission: a firewall or the OS refused the socket.
		"permission",
		// no_buffers: the OS ran out of socket buffers.
		"no_buffers",
		// family_unsupported: the address family, usually IPv6, is unavailable.
		"family_unsupported",
		// other: any other error.
		"other",
	}
)

var preflightCauses = func() map[string]bool {
	m := map[string]bool{PreflightCauseOpenTimeout: true, PreflightCauseBuild: true}
	for _, step := range preflightCauseSteps {
		for _, class := range preflightCauseClasses {
			m[step+"_"+class] = true
		}
	}
	return m
}()

// PreflightCause joins a failed step and an error class into a cause.
func PreflightCause(step, class string) string {
	return step + "_" + class
}

// KnownPreflightCause reports whether s is a cause Freddie counts.
func KnownPreflightCause(s string) bool {
	return preflightCauses[s]
}

// NormalizePreflightCause maps a reported cause onto the known set, "unreported" when none was
// reported, or "invalid".
func NormalizePreflightCause(s string) string {
	if s == "" {
		return "unreported"
	}
	return normalizeEnum(s, preflightCauses)
}

// VPN states a producer can report: radiance's vpn.VPNStatus values. An app without a VPN, or a
// browser, reports nothing.
var vpnStates = map[string]bool{
	"connecting": true, "connected": true, "disconnecting": true, "disconnected": true,
	"restarting": true, "error": true,
}

// NormalizeVPNState maps a reported VPN state onto vpnStates, "unknown" when none was reported, or
// "invalid".
func NormalizeVPNState(s string) string {
	if s == "" {
		return "unknown"
	}
	return normalizeEnum(s, vpnStates)
}

// MaxReportedPreflightFailures bounds one report's failure count, and producers stop counting
// there. A producer backing off from 5s to 5 minutes fails about 300 times a day, so this covers
// more than a month offline.
const MaxReportedPreflightFailures = 10000

// ParsePreflightFailures parses a reported failure count, reporting false for anything but a
// whole number from 1 to MaxReportedPreflightFailures.
func ParsePreflightFailures(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxReportedPreflightFailures || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}
