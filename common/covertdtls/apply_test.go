package covertdtls

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

// Apply's contract has to hold identically under both build tags, because a
// caller writing against it cannot see which implementation it will link. This
// test compiles for native and for js/wasm and asserts the parts that must not
// diverge; the wasm CI job (.github/workflows/build-widget-wasm.yml) only builds
// ./cmd, so run it explicitly for wasm with:
//
//	GOOS=js GOARCH=wasm go vet ./common/covertdtls/
//
// (Go cannot *execute* js/wasm tests without a JS host, so the wasm side is
// compile-checked rather than run.)
func TestApply_NilSettingEngineIsAnErrorUnderEveryBuildTag(t *testing.T) {
	// A nil engine is a caller bug, not a platform limitation, so both
	// implementations must reject it. Validation that fires under only one build
	// tag means the bug is caught on native and passes silently in the widget.
	if err := Apply(Config{Randomize: true}, nil); err == nil {
		t.Error("Apply(cfg, nil) returned nil; a nil SettingEngine must be an error on every platform")
	}
	if err := Apply(Config{}, nil); err == nil {
		t.Error("Apply(zero cfg, nil) returned nil; the nil check must not depend on the config")
	}
}

// A config asking for fingerprint shaping must never fail startup. On native it
// installs hooks; on wasm it is a documented no-op. Either way, success.
func TestApply_ConfiguredModesDoNotError(t *testing.T) {
	for name, cfg := range map[string]Config{
		"disabled":       {},
		"randomize":      {Randomize: true},
		"mimic":          {Mimic: true},
		"randomizemimic": {Randomize: true, Mimic: true},
	} {
		if err := Apply(cfg, &webrtc.SettingEngine{}); err != nil {
			t.Errorf("Apply(%s) = %v, want nil: a covertdtls mode must not fail startup", name, err)
		}
	}
}

// ParseModeString and Enabled are platform-independent and deliberately stayed in
// the shared file rather than being split by build tag. Pin that they behave the
// same, so a future split doesn't quietly move them.
func TestParseModeString(t *testing.T) {
	for _, mode := range []string{ModeRandomize, ModeMimic, ModeRandomizeMimic} {
		cfg, err := ParseModeString(mode)
		if err != nil {
			t.Errorf("ParseModeString(%q) errored: %v", mode, err)
			continue
		}
		if !cfg.Enabled() {
			t.Errorf("ParseModeString(%q) produced a disabled config", mode)
		}
	}

	cfg, err := ParseModeString(ModeDisable)
	if err != nil {
		t.Errorf("ParseModeString(%q) errored: %v", ModeDisable, err)
	}
	if cfg.Enabled() {
		t.Errorf("ParseModeString(%q) produced an enabled config", ModeDisable)
	}

	if _, err := ParseModeString("nonsense"); err == nil {
		t.Error("ParseModeString(\"nonsense\") = nil error, want a rejection")
	}
}
