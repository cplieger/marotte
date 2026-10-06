package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
)

// TestSyncPushPreferences_SparsePatchDoesNotResetAnOmittedKind pins that a sparse patch does
// not re-enable a kind it omits (a switched-off preference reverting itself), both directions
// for every configurable kind.
func TestSyncPushPreferences_SparsePatchDoesNotResetAnOmittedKind(t *testing.T) {
	const (
		finished = settings.KeyNotifyAgentFinished
		prStatus = settings.KeyNotifyPRStatus
	)
	tests := map[string]struct {
		persisted    string // config.json contents; "" writes no file at all
		patch        string
		wantFinished bool
		wantPR       bool
		why          string
	}{
		"omitted key keeps a disabled preference": {
			persisted:    `{"notify_agent_finished":false,"notify_pr_status":false}`,
			patch:        `{"debug_logs":true}`,
			wantFinished: false,
			wantPR:       false,
			why:          "a patch naming neither kind must not turn either back on",
		},
		"omitted key keeps an enabled preference": {
			persisted:    `{"notify_agent_finished":true,"notify_pr_status":true}`,
			patch:        `{"debug_logs":true}`,
			wantFinished: true,
			wantPR:       true,
			why:          "the other direction: an untouched enabled kind stays enabled",
		},
		"a present key wins over the persisted value": {
			persisted:    `{"notify_agent_finished":false,"notify_pr_status":false}`,
			patch:        `{"notify_agent_finished":true}`,
			wantFinished: true,
			wantPR:       false,
			why:          "the patch is the freshest layer, and it moves only the kind it names",
		},
		"a present key can disable while its sibling is untouched": {
			persisted:    `{"notify_agent_finished":true,"notify_pr_status":true}`,
			patch:        `{"notify_pr_status":false}`,
			wantFinished: true,
			wantPR:       false,
			why:          "one toggle is one kind; the sibling reads from disk, not from the default",
		},
		"no settings file falls back to the registry defaults": {
			persisted:    "",
			patch:        `{}`,
			wantFinished: true,
			wantPR:       false,
			why:          "nothing on disk and nothing in the patch leaves each kind at its own registry default, which is ON for agent_finished and OFF for pr_status",
		},
		"an unparseable settings file falls back to the registry defaults": {
			persisted:    `{not json`,
			patch:        `{}`,
			wantFinished: true,
			wantPR:       false,
			// Runs after the write succeeded, so the registry default is the only answer; it is logged.
			why: "an unreadable config.json leaves every kind at its own registry default, which is per-kind and OFF for pr_status",
		},
		"a malformed patch value falls back to the registry default": {
			persisted:    `{"notify_agent_finished":false}`,
			patch:        `{"notify_agent_finished":"nonsense"}`,
			wantFinished: true,
			wantPR:       false,
			// The key is present and persisted verbatim, so the next reload resolves it the same way.
			why: "a present-but-unreadable value is not an absent one; it resolves the way a reload would",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.persisted != "" {
				path := filepath.Join(dir, settings.Filename)
				if err := os.WriteFile(path, []byte(tc.persisted), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatalf("unmarshal patch %s: %v", tc.patch, err)
			}
			mp := &testPush{}
			s := &Server{push: mp, configDir: dir}

			s.syncPushPreferences(patch)

			if got := mp.prefs[marotte.PushKindAgentFinished]; got != tc.wantFinished {
				t.Errorf("prefs[%s] = %v, want %v (%s)", finished, got, tc.wantFinished, tc.why)
			}
			if got := mp.prefs[marotte.PushKindPRStatus]; got != tc.wantPR {
				t.Errorf("prefs[%s] = %v, want %v (%s)", prStatus, got, tc.wantPR, tc.why)
			}
			// The floor rides every case: no resolution path may lower it.
			if !mp.prefs[marotte.PushKindPermission] {
				t.Errorf("prefs[Permission] = false, want true (the ask is a floor, %s)", tc.why)
			}
		})
	}
}

// TestSyncPushPreferences_CarriesTheRunOutcomeKind pins that a registry row is the whole
// wiring for a keyed kind.
func TestSyncPushPreferences_CarriesTheRunOutcomeKind(t *testing.T) {
	for name, tc := range map[string]struct {
		patch string
		want  bool
	}{
		"the patch switches it off": {patch: `{"notify_run_outcome":false}`, want: false},
		"the patch switches it on":  {patch: `{"notify_run_outcome":true}`, want: true},
		// Absent from both the patch and the file, so the registry's DefaultOn answers.
		"absent takes the registry default": {patch: `{}`, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatalf("unmarshal patch %s: %v", tc.patch, err)
			}
			mp := &testPush{}
			s := &Server{push: mp, configDir: t.TempDir()}

			s.syncPushPreferences(patch)

			got, known := mp.prefs[marotte.PushKindRunOutcome]
			if !known {
				t.Fatal("run_outcome reached SetPreferences with no entry; preflightSend drops such a kind")
			}
			if got != tc.want {
				t.Errorf("prefs[run_outcome] = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSyncPushPreferences_MergedPatchNeedsNoDiskRead pins the fast path with a config.json
// that DISAGREES with the patch.
func TestSyncPushPreferences_MergedPatchNeedsNoDiskRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"notify_agent_finished":true,"notify_pr_status":true}`), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mp := &testPush{}
	s := &Server{push: mp, configDir: dir}

	s.syncPushPreferences(map[string]json.RawMessage{
		settings.KeyNotifyAgentFinished: json.RawMessage(`false`),
		settings.KeyNotifyPRStatus:      json.RawMessage(`false`),
	})

	if mp.prefs[marotte.PushKindAgentFinished] || mp.prefs[marotte.PushKindPRStatus] {
		t.Errorf("prefs = %+v, want both false: a key the patch carries must not be re-read from disk", mp.prefs)
	}
}

// TestExistingSettingsForMerge_SizeCapIsInclusive pins that a file exactly at the cap merges
// and one past it is an ERROR, not an empty map.
func TestExistingSettingsForMerge_SizeCapIsInclusive(t *testing.T) {
	// Trailing whitespace keeps the padded document parseable.
	seed := func(t *testing.T, size int) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.json")
		doc := `{"theme":"dark"}`
		if len(doc) > size {
			t.Fatalf("seed document is %d bytes, over the %d target", len(doc), size)
		}
		data := append([]byte(doc), bytes.Repeat([]byte(" "), size-len(doc))...)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return path
	}

	tests := []struct {
		name      string
		size      int
		wantErr   bool
		wantKeys  int
		wantTheme string
	}{
		{name: "at_the_cap", size: maxSettingsBytes, wantKeys: 1, wantTheme: `"dark"`},
		{name: "one_past_the_cap", size: maxSettingsBytes + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readStoredSettings(seed(t, tt.size))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("readStoredSettings of a %d-byte file = (%d keys, nil), want an error",
						tt.size, len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("readStoredSettings of a %d-byte file: err = %v, want nil", tt.size, err)
			}
			if len(got) != tt.wantKeys {
				t.Fatalf("readStoredSettings of a %d-byte file returned %d keys, want %d",
					tt.size, len(got), tt.wantKeys)
			}
			if tt.wantTheme != "" && string(got["theme"]) != tt.wantTheme {
				t.Errorf("theme = %s, want %s", got["theme"], tt.wantTheme)
			}
		})
	}
}

// TestSyncPushPreferences_MasterSwitch pins the master switch's server-side enforcement:
// only an EXPLICIT false zeroes the set, permission included.
func TestSyncPushPreferences_MasterSwitch(t *testing.T) {
	tests := map[string]struct {
		persisted      string // config.json contents; "" writes no file at all
		patch          string
		wantFinished   bool
		wantPermission bool
		why            string
	}{
		"an explicit false in the PATCH zeroes every kind": {
			persisted:      `{"notify_agent_finished":true}`,
			patch:          `{"notifications_enabled":false}`,
			wantFinished:   false,
			wantPermission: false,
			why:            "the switch is applied last, so no per-kind value can re-widen it",
		},
		"an explicit false in the PERSISTED doc zeroes every kind": {
			persisted:      `{"notifications_enabled":false,"notify_agent_finished":true}`,
			patch:          `{"debug_logs":true}`,
			wantFinished:   false,
			wantPermission: false,
			why: "a patch touching something else must still honour the stored refusal, " +
				"or every unrelated save would re-enable notifications",
		},
		"the patch outranks a persisted true": {
			persisted:      `{"notifications_enabled":true,"notify_agent_finished":true}`,
			patch:          `{"notifications_enabled":false}`,
			wantFinished:   false,
			wantPermission: false,
			why:            "the patch is the freshest layer, so switching off takes effect immediately",
		},
		"the patch outranks a persisted false": {
			persisted:      `{"notifications_enabled":false,"notify_agent_finished":true}`,
			patch:          `{"notifications_enabled":true}`,
			wantFinished:   true,
			wantPermission: true,
			why:            "the other direction: switching back on must not read the stale disk value",
		},
		"an explicit true leaves the per-kind switches deciding": {
			persisted:      `{"notify_agent_finished":false}`,
			patch:          `{"notifications_enabled":true}`,
			wantFinished:   false,
			wantPermission: true,
			why:            "the master is a gate, not an override: it never turns a kind back on",
		},
		"an ABSENT master leaves the registry defaults standing": {
			persisted:      "",
			patch:          `{}`,
			wantFinished:   true,
			wantPermission: true,
			why: "the polarity decision: absent means not-yet-opted-in, and treating it as a " +
				"refusal would silence every never-configured workspace",
		},
		"a malformed value is not a refusal": {
			persisted:      `{"notifications_enabled":"nonsense"}`,
			patch:          `{}`,
			wantFinished:   true,
			wantPermission: true,
			why: "matching settings.decodeInto at the read path: a value the server cannot " +
				"parse is not the reader asking for silence",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.persisted != "" {
				path := filepath.Join(dir, settings.Filename)
				if err := os.WriteFile(path, []byte(tc.persisted), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatalf("unmarshal patch %s: %v", tc.patch, err)
			}
			mp := &testPush{}
			s := &Server{push: mp, configDir: dir}

			s.syncPushPreferences(patch)

			if got := mp.prefs[marotte.PushKindAgentFinished]; got != tc.wantFinished {
				t.Errorf("prefs[%s] = %v, want %v (%s)",
					settings.KeyNotifyAgentFinished, got, tc.wantFinished, tc.why)
			}
			// Unsilenceable by its own key, silenceable by the master.
			if got := mp.prefs[marotte.PushKindPermission]; got != tc.wantPermission {
				t.Errorf("prefs[Permission] = %v, want %v (%s)", got, tc.wantPermission, tc.why)
			}
		})
	}
}

// TestSyncPushPreferences_MasterSwitchZeroesEveryRegisteredKind pins the zeroing over every
// push.Kinds() entry.
func TestSyncPushPreferences_MasterSwitchZeroesEveryRegisteredKind(t *testing.T) {
	mp := &testPush{}
	s := &Server{push: mp, configDir: t.TempDir()}

	s.syncPushPreferences(map[string]json.RawMessage{
		settings.KeyNotificationsEnabled: json.RawMessage(`false`),
	})

	if len(mp.prefs) == 0 {
		t.Fatal("no preferences reached SetPreferences at all")
	}
	for kind, on := range mp.prefs {
		if on {
			t.Errorf("prefs[%s] = true with the master switch off; every kind goes off together", kind)
		}
	}
	if len(mp.prefs) != len(push.Kinds()) {
		t.Errorf("prefs carries %d kinds, want %d: the set comes from the registry",
			len(mp.prefs), len(push.Kinds()))
	}
}
