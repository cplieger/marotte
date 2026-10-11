package settings

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestMemoryPreferenceFor_MapsEachDropdownValue(t *testing.T) {
	cases := []struct {
		mode string
		want marotte.MemoryPreference
	}{
		{MemoryOff, marotte.MemoryPreference{Mode: "disabled"}},
		{memoryReadOnly, marotte.MemoryPreference{Mode: "read_only"}},
		{memoryReadWrite, marotte.MemoryPreference{Mode: "read_write"}},
		{memoryLearn, marotte.MemoryPreference{Mode: "read_write", Reflection: true}},
		// kiro-cli's own default, so an unrecognised value never silently reads as Off.
		{"", marotte.MemoryPreference{Mode: "read_write", Reflection: true}},
		{"purple", marotte.MemoryPreference{Mode: "read_write", Reflection: true}},
	}
	for _, tc := range cases {
		if got := MemoryPreferenceFor(tc.mode); got != tc.want {
			t.Errorf("MemoryPreferenceFor(%q) = %+v, want %+v", tc.mode, got, tc.want)
		}
	}
}

func TestEffectiveFrom_MemoryModeDefaultsAndRejectsUnknownValues(t *testing.T) {
	if got := EffectiveDefaults().MemoryMode; got != memoryLearn {
		t.Errorf("EffectiveDefaults().MemoryMode = %q, want %q (kiro-cli's read_write + reflection)", got, memoryLearn)
	}
	eff, rejected := EffectiveFrom(map[string]json.RawMessage{KeyMemoryMode: json.RawMessage(`"off"`)})
	if eff.MemoryMode != MemoryOff || len(rejected) != 0 {
		t.Errorf("stored off: MemoryMode = %q rejected = %v, want off and nothing rejected", eff.MemoryMode, rejected)
	}
	eff, rejected = EffectiveFrom(map[string]json.RawMessage{KeyMemoryMode: json.RawMessage(`"always"`)})
	if eff.MemoryMode != DefaultMemoryMode || !slices.Contains(rejected, KeyMemoryMode) {
		t.Errorf("stored always: MemoryMode = %q rejected = %v, want the default and %s rejected",
			eff.MemoryMode, rejected, KeyMemoryMode)
	}
}

func TestKnownKeys_HasNoMemoryEnabled(t *testing.T) {
	if _, ok := KnownKeys["memory_enabled"]; ok {
		t.Error("memory_enabled is a known key; memory_mode is the only memory setting")
	}
}
