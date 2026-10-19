package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// agentDirsFixture is a workspace, a config directory and one directory beside them, each
// canonical (t.TempDir may sit under a symlinked /tmp).
type agentDirsFixture struct {
	dirs  AgentDirs
	extra string
}

func newAgentDirsFixture(t *testing.T) agentDirsFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := agentDirsFixture{
		dirs:  AgentDirs{Workspace: filepath.Join(base, "workspace"), Config: filepath.Join(base, "config")},
		extra: filepath.Join(base, "data"),
	}
	for _, d := range []string{f.dirs.Workspace, f.dirs.Config, f.extra} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestAgentDirsCheckEntry(t *testing.T) {
	f := newAgentDirsFixture(t)
	base := filepath.Dir(f.extra)
	file := filepath.Join(f.extra, "file.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(base, "reports ")
	if err := os.Mkdir(spaced, 0o700); err != nil {
		t.Fatal(err)
	}
	linkToExtra := filepath.Join(base, "link-to-data")
	linkToConfig := filepath.Join(base, "link-to-config")
	for target, link := range map[string]string{f.extra: linkToExtra, f.dirs.Config: linkToConfig} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name   string
		entry  string
		reason string // "" accepts
	}{
		{name: "an existing directory beside both trees is accepted", entry: f.extra},
		{name: "a directory whose name ends in a space is accepted as written", entry: spaced},
		{name: "an empty entry is refused", entry: "", reason: "cannot be empty"},
		{name: "a blank entry is refused as relative", entry: " ", reason: "absolute path"},
		{name: "a relative path is refused", entry: "data", reason: "absolute path"},
		{name: "an unclean path is refused with its clean spelling", entry: f.extra + "/", reason: "write it as"},
		{name: "a dot-dot spelling is refused", entry: f.dirs.Workspace + "/../data", reason: "write it as"},
		{name: "the filesystem root is refused", entry: "/", reason: "filesystem root"},
		{name: "the config directory is refused", entry: f.dirs.Config, reason: "config directory"},
		{name: "a path inside the config directory is refused", entry: filepath.Join(f.dirs.Config, "home"), reason: "config directory"},
		{name: "a parent of the config directory is refused", entry: base, reason: "contains marotte's config directory"},
		{name: "a symlink onto the config directory is refused", entry: linkToConfig, reason: "symbolic link"},
		{name: "the workspace is refused", entry: f.dirs.Workspace, reason: "the workspace"},
		{name: "a path inside the workspace is refused", entry: filepath.Join(f.dirs.Workspace, "repo"), reason: "the workspace"},
		{name: "a missing directory is refused", entry: filepath.Join(base, "missing"), reason: "no such directory"},
		{name: "a regular file is refused", entry: file, reason: "not a directory"},
		{name: "a symlink to an allowed directory is refused with its target", entry: linkToExtra, reason: "add \"" + f.extra + "\" instead"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.dirs.checkEntry(tt.entry)
			if tt.reason == "" {
				if err != nil {
					t.Errorf("checkEntry(%q) = %v, want nil", tt.entry, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("checkEntry(%q) = %v, want a refusal naming %q", tt.entry, err, tt.reason)
			}
			if !errors.Is(err, errAdditionalDirectory) {
				t.Errorf("checkEntry(%q) = %v, want it to wrap errAdditionalDirectory", tt.entry, err)
			}
		})
	}
}

func TestAgentDirsCheckEntry_ConfigDirectoryCanNeverBeAdded(t *testing.T) {
	f := newAgentDirsFixture(t)
	link := filepath.Join(f.extra, "config-link")
	if err := os.Symlink(f.dirs.Config, link); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"/", filepath.Dir(f.dirs.Config), f.dirs.Config, filepath.Join(f.dirs.Config, "home", ".kiro"), link} {
		if err := f.dirs.checkEntry(entry); err == nil {
			t.Errorf("checkEntry(%q) = nil, want a refusal: it reaches the config directory", entry)
		}
	}
}

// storeDirs writes config.json holding entries as additional_directories.
func storeDirs(t *testing.T, configDir string, entries ...string) {
	t.Helper()
	data, err := json.Marshal(map[string][]string{KeyAdditionalDirectories: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, Filename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDirsGranted(t *testing.T) {
	ctx := t.Context()

	t.Run("an absent key grants nothing", func(t *testing.T) {
		f := newAgentDirsFixture(t)
		granted, refused, err := f.dirs.Granted(ctx)
		if len(granted) != 0 || len(refused) != 0 || err != nil {
			t.Errorf("Granted = %v, %v, %v; want nothing", granted, refused, err)
		}
		if refused == nil {
			t.Error("refused = nil, want an empty map: the settings answer sends it as an object")
		}
	})

	t.Run("valid entries are granted once, in order, and each refused one carries its reason", func(t *testing.T) {
		f := newAgentDirsFixture(t)
		second := filepath.Join(filepath.Dir(f.extra), "second")
		if err := os.Mkdir(second, 0o700); err != nil {
			t.Fatal(err)
		}
		storeDirs(t, f.dirs.Config, second, f.dirs.Config, f.extra, second, "/")
		granted, refused, err := f.dirs.Granted(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{second, f.extra}; !slices.Equal(granted, want) {
			t.Errorf("granted = %v, want %v", granted, want)
		}
		if len(refused) != 2 {
			t.Errorf("refused = %v, want the config directory and the filesystem root", refused)
		}
		if !strings.Contains(refused[f.dirs.Config], "config directory") {
			t.Errorf("refused[%q] = %q, want the config-directory reason", f.dirs.Config, refused[f.dirs.Config])
		}
		if !strings.Contains(refused["/"], "filesystem root") {
			t.Errorf(`refused["/"] = %q, want the filesystem-root reason`, refused["/"])
		}
	})

	t.Run("a directory removed after it was added is no longer granted", func(t *testing.T) {
		f := newAgentDirsFixture(t)
		storeDirs(t, f.dirs.Config, f.extra)
		if err := os.Remove(f.extra); err != nil {
			t.Fatal(err)
		}
		granted, refused, err := f.dirs.Granted(ctx)
		if len(granted) != 0 || err != nil {
			t.Errorf("Granted = %v, %v; want nothing granted", granted, err)
		}
		if !strings.Contains(refused[f.extra], "no such directory") {
			t.Errorf("refused = %v, want %q refused as missing", refused, f.extra)
		}
	})

	t.Run("an unreadable config grants nothing and says why", func(t *testing.T) {
		f := newAgentDirsFixture(t)
		if err := os.WriteFile(filepath.Join(f.dirs.Config, Filename), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		granted, _, err := f.dirs.Granted(ctx)
		if len(granted) != 0 || err == nil {
			t.Errorf("Granted = %v, %v; want nothing granted and the read error", granted, err)
		}
	})
}

func patchOf(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{KeyAdditionalDirectories: data}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentDirsValidatePatch(t *testing.T) {
	f := newAgentDirsFixture(t)
	none := map[string]json.RawMessage{}
	tests := []struct {
		name   string
		patch  map[string]json.RawMessage
		reason string
	}{
		{name: "a patch without the key passes", patch: map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`)}},
		{name: "an empty list passes", patch: patchOf(t, []string{})},
		{name: "a valid list passes", patch: patchOf(t, []string{f.extra})},
		{name: "a string instead of a list is refused", patch: patchOf(t, f.extra), reason: "must be a list"},
		{name: "null is refused", patch: map[string]json.RawMessage{KeyAdditionalDirectories: json.RawMessage(`null`)}, reason: "must be a list"},
		{name: "a duplicate is refused", patch: patchOf(t, []string{f.extra, f.extra}), reason: "listed twice"},
		{name: "a refused entry is refused with its reason", patch: patchOf(t, []string{f.extra, f.dirs.Config}), reason: "config directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.dirs.ValidatePatch(none, tt.patch)
			if tt.reason == "" {
				if err != nil {
					t.Errorf("ValidatePatch = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("ValidatePatch = %v, want a refusal naming %q", err, tt.reason)
			}
		})
	}
}

// The stored list is the user's intent and the grant is computed from it, so an entry refused
// before the write never blocks one that keeps it.
func TestAgentDirsValidatePatch_EntriesRefusedBeforeNeverBlockAWrite(t *testing.T) {
	f := newAgentDirsFixture(t)
	gone := filepath.Join(filepath.Dir(f.extra), "gone")
	second := filepath.Join(filepath.Dir(f.extra), "second")
	mkdirs(t, second)
	stored := patchOf(t, []string{f.extra, gone, f.dirs.Config})

	t.Run("removing a granted entry passes beside refused ones", func(t *testing.T) {
		if err := f.dirs.ValidatePatch(stored, patchOf(t, []string{gone, f.dirs.Config})); err != nil {
			t.Errorf("ValidatePatch = %v, want nil", err)
		}
	})
	t.Run("removing a refused entry passes", func(t *testing.T) {
		if err := f.dirs.ValidatePatch(stored, patchOf(t, []string{f.extra, f.dirs.Config})); err != nil {
			t.Errorf("ValidatePatch = %v, want nil", err)
		}
	})
	t.Run("adding a valid entry passes beside refused ones", func(t *testing.T) {
		if err := f.dirs.ValidatePatch(stored, patchOf(t, []string{f.extra, gone, f.dirs.Config, second})); err != nil {
			t.Errorf("ValidatePatch = %v, want nil", err)
		}
	})
	t.Run("an added invalid entry is still refused with its reason", func(t *testing.T) {
		missing := filepath.Join(filepath.Dir(f.extra), "missing")
		err := f.dirs.ValidatePatch(stored, patchOf(t, []string{f.extra, gone, missing}))
		if err == nil || !strings.Contains(err.Error(), "no such directory") || !strings.Contains(err.Error(), missing) {
			t.Errorf("ValidatePatch = %v, want %q refused as missing", err, missing)
		}
	})
	t.Run("an added duplicate of a stored entry is refused", func(t *testing.T) {
		err := f.dirs.ValidatePatch(stored, patchOf(t, []string{f.extra, gone, gone}))
		if err == nil || !strings.Contains(err.Error(), "listed twice") {
			t.Errorf("ValidatePatch = %v, want a duplicate refusal", err)
		}
	})
	t.Run("a stored list that is not a list of strings checks every entry as added", func(t *testing.T) {
		malformed := map[string]json.RawMessage{KeyAdditionalDirectories: json.RawMessage(`"` + gone + `"`)}
		if err := f.dirs.ValidatePatch(malformed, patchOf(t, []string{gone})); err == nil {
			t.Error("ValidatePatch = nil, want the missing entry refused: nothing valid was stored")
		}
	})
}

// Granted roots never overlap, so the first root holding a path is the only one, and a root
// is never reached as a deletable path inside another.
func TestAgentDirsValidatePatch_AnAddedEntryOverlappingAGrantedOneIsRefused(t *testing.T) {
	f := newAgentDirsFixture(t)
	inner := filepath.Join(f.extra, "project")
	mkdirs(t, inner)
	tests := []struct {
		name          string
		stored, patch []string
		reason        string
	}{
		{name: "an entry inside a granted one", stored: []string{f.extra}, patch: []string{f.extra, inner}, reason: "lies inside \"" + f.extra + "\""},
		{name: "an entry holding a granted one", stored: []string{inner}, patch: []string{inner, f.extra}, reason: "contains \"" + inner + "\""},
		{name: "an entry holding a granted one listed before it", stored: []string{inner}, patch: []string{f.extra, inner}, reason: "lies inside \"" + f.extra + "\""},
		{name: "two added entries, the second inside the first", patch: []string{f.extra, inner}, reason: "lies inside \"" + f.extra + "\""},
		{name: "two added entries, the second holding the first", patch: []string{inner, f.extra}, reason: "contains \"" + inner + "\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.dirs.ValidatePatch(patchOf(t, tt.stored), patchOf(t, tt.patch))
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("ValidatePatch = %v, want a refusal naming %s", err, tt.reason)
			}
		})
	}
	t.Run("an entry holding one the write's own removal grants", func(t *testing.T) {
		data := filepath.Join(f.extra, "data")
		project := filepath.Join(data, "project")
		mkdirs(t, project)
		err := f.dirs.ValidatePatch(patchOf(t, []string{data, project}), patchOf(t, []string{project, f.extra}))
		if err == nil || !strings.Contains(err.Error(), "contains \""+project+"\"") {
			t.Errorf("ValidatePatch = %v, want %q refused for holding %q", err, f.extra, project)
		}
	})
	t.Run("an entry overlapping only a refused one passes", func(t *testing.T) {
		gone := filepath.Join(f.extra, "gone")
		if err := f.dirs.ValidatePatch(patchOf(t, []string{gone}), patchOf(t, []string{gone, f.extra})); err != nil {
			t.Errorf("ValidatePatch = %v, want nil: %q grants nothing", err, gone)
		}
	})
}

func TestAgentDirsAssess_TheLaterOfAnOverlappingPairIsRefused(t *testing.T) {
	f := newAgentDirsFixture(t)
	inner := filepath.Join(f.extra, "project")
	mkdirs(t, inner)
	for _, tt := range []struct {
		name             string
		entries          []string
		granted, refused string
		reason           string
	}{
		{name: "the outer one first", entries: []string{f.extra, inner}, granted: f.extra, refused: inner, reason: "lies inside"},
		{name: "the inner one first", entries: []string{inner, f.extra}, granted: inner, refused: f.extra, reason: "contains"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			granted, refused := f.dirs.Assess(tt.entries)
			if !slices.Equal(granted, []string{tt.granted}) {
				t.Errorf("granted = %v, want [%s]", granted, tt.granted)
			}
			if !strings.Contains(refused[tt.refused], tt.reason) || !strings.Contains(refused[tt.refused], tt.granted) {
				t.Errorf("refused[%q] = %q, want it to say it %s %q", tt.refused, refused[tt.refused], tt.reason, tt.granted)
			}
		})
	}
	t.Run("an entry overlapping only a refused one is granted", func(t *testing.T) {
		gone := filepath.Join(f.extra, "gone")
		granted, _ := f.dirs.Assess([]string{gone, f.extra})
		if !slices.Equal(granted, []string{f.extra}) {
			t.Errorf("granted = %v, want [%s]", granted, f.extra)
		}
	})
}

func TestEffectiveFrom_AdditionalDirectories(t *testing.T) {
	t.Run("absent reads as an empty list, never null", func(t *testing.T) {
		got, _ := EffectiveFrom(nil)
		if got.AdditionalDirectories == nil || len(got.AdditionalDirectories) != 0 {
			t.Errorf("AdditionalDirectories = %#v, want []", got.AdditionalDirectories)
		}
	})
	t.Run("a stored list is echoed as stored", func(t *testing.T) {
		got, rejected := EffectiveFrom(map[string]json.RawMessage{KeyAdditionalDirectories: json.RawMessage(`["/data"]`)})
		if !slices.Equal(got.AdditionalDirectories, []string{"/data"}) || len(rejected) != 0 {
			t.Errorf("AdditionalDirectories = %v, rejected %v; want [/data] and none", got.AdditionalDirectories, rejected)
		}
	})
}
