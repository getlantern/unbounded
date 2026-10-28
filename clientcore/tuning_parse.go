package clientcore

import (
	"fmt"
	"slices"
	"strconv"
	"time"
)

// tuningKey is one recognized key of a server's tuning map: the WebRTCTuning field it sets and the
// range a value must fall in. The bounds keep a bad value from making pion retransmit in a tight
// loop, wait for days or allocate gigabytes.
type tuningKey struct {
	set      func(*WebRTCTuning, uint64)
	min, max uint64
}

const maxTuningMs = 10 * 60 * 1000

// tuningKeys maps each recognized key onto its field and bounds. Durations are in milliseconds and
// sizes in bytes; every value is a decimal string.
var tuningKeys = map[string]tuningKey{
	"ice_max_binding_requests": {
		set: func(t *WebRTCTuning, v uint64) { t.ICEMaxBindingRequests = uint16(v) },
		min: 1, max: maxICEBindingRequests,
	},
	"stun_gather_timeout_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.STUNGatherTimeout = ms(v) },
		min: 100, max: maxTuningMs,
	},
	"ice_disconnected_timeout_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.ICEDisconnectedTimeout = ms(v) },
		min: 500, max: maxTuningMs,
	},
	"ice_failed_timeout_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.ICEFailedTimeout = ms(v) },
		min: 1000, max: maxTuningMs,
	},
	"ice_keepalive_interval_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.ICEKeepaliveInterval = ms(v) },
		min: 100, max: maxTuningMs,
	},
	"srflx_acceptance_min_wait_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.SrflxAcceptanceMinWait = ms(v) },
		min: 1, max: maxTuningMs,
	},
	"dtls_retransmit_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.DTLSRetransmitInterval = ms(v) },
		min: 100, max: maxTuningMs,
	},
	// Below pion's 1s RTO floor, the cap becomes the RTO itself.
	"sctp_rto_max_ms": {
		set: func(t *WebRTCTuning, v uint64) { t.SCTPRTOMax = ms(v) },
		min: 1000, max: maxTuningMs,
	},
	"sctp_max_receive_buffer_bytes": {
		set: func(t *WebRTCTuning, v uint64) { t.SCTPMaxReceiveBuffer = uint32(v) },
		min: 64 << 10, max: 64 << 20,
	},
}

func ms(v uint64) time.Duration { return time.Duration(v) * time.Millisecond }

// ParseWebRTCTuning turns a server's tuning map into a WebRTCTuning. Keys it doesn't recognize are
// reported in ignored rather than rejected: the option is a map precisely so that a server can send
// knobs newer clients understand without breaking older ones, which is the whole point of tuning
// from config. A recognized key with a bad value is reported too and left at its default.
func ParseWebRTCTuning(opts map[string]string) (tuning WebRTCTuning, ignored []string) {
	for k, raw := range opts {
		key, ok := tuningKeys[k]
		if !ok {
			ignored = append(ignored, k)
			continue
		}
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v < key.min || v > key.max {
			ignored = append(ignored, fmt.Sprintf("%s=%q", k, raw))
			continue
		}
		key.set(&tuning, v)
	}
	// A keepalive that doesn't fire within the disconnected timeout lets every idle connection go
	// disconnected between keepalives, so drop the ICE liveness overrides rather than apply them.
	if orDefault(tuning.ICEKeepaliveInterval, pionICEKeepaliveInterval) >=
		orDefault(tuning.ICEDisconnectedTimeout, pionICEDisconnectedTimeout) {
		tuning.ICEDisconnectedTimeout, tuning.ICEFailedTimeout, tuning.ICEKeepaliveInterval = 0, 0, 0
		ignored = append(ignored, "ice_*: keepalive interval must be below the disconnected timeout")
	}
	slices.Sort(ignored)
	return tuning, ignored
}
