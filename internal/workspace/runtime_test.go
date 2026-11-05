package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runtimeEntry lays out a runtime-dir entry as kiro-cli 2.37 unpacks it.
func runtimeEntry(t *testing.T, rt, name string, complete bool) string {
	t.Helper()
	dir := filepath.Join(rt, name)
	touch(t, filepath.Join(dir, "node"))
	touch(t, filepath.Join(dir, "kas", kasServerRel))
	if complete {
		touch(t, filepath.Join(dir, ".complete"))
	}
	touch(t, dir+".lock")
	return dir
}

// dataDir lays out kiro-cli's data-dir runtime under home and returns its kiro-cli directory.
func dataDir(t *testing.T, home string, kasEntries ...string) string {
	t.Helper()
	data := filepath.Join(home, ".local", "share", "kiro-cli")
	touch(t, filepath.Join(data, "node"))
	for _, e := range kasEntries {
		touch(t, filepath.Join(data, "kas", e, kasServerRel))
	}
	return data
}

func TestKASLookup_FindMirrorsKiroCLI(t *testing.T) {
	const entry = "2.37.0-692ec22533745f1e"
	const dataEntry = "2.37.0-ddda7332"
	tests := []struct {
		name    string
		version string
		setup   func(t *testing.T, rt, home string) (KASLookup, KASRuntime)
	}{
		{"absolute_runtime_dir_with_a_complete_entry", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			dir := runtimeEntry(t, rt, entry, true)
			dataDir(t, home, dataEntry)
			return KASLookup{RuntimeDir: rt, Home: home},
				KASRuntime{Node: filepath.Join(dir, "node"), Server: filepath.Join(dir, "kas", kasServerRel)}
		}},
		{"entry_without_complete_is_ignored", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			runtimeEntry(t, rt, entry, false)
			data := dataDir(t, home, dataEntry)
			return KASLookup{RuntimeDir: rt, Home: home},
				KASRuntime{Node: filepath.Join(data, "node"), Server: filepath.Join(data, "kas", dataEntry, kasServerRel)}
		}},
		{"relative_runtime_dir_falls_to_the_data_dir", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			runtimeEntry(t, rt, entry, true)
			data := dataDir(t, home, dataEntry)
			rel, err := filepath.Rel(filepath.Dir(rt), rt)
			if err != nil {
				t.Fatal(err)
			}
			return KASLookup{RuntimeDir: rel, Home: home},
				KASRuntime{Node: filepath.Join(data, "node"), Server: filepath.Join(data, "kas", dataEntry, kasServerRel)}
		}},
		{"another_versions_entry_is_not_chosen", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			runtimeEntry(t, rt, "2.37.1-0123456789abcdef", true)
			data := dataDir(t, home)
			return KASLookup{RuntimeDir: rt, Home: home}, KASRuntime{Node: filepath.Join(data, "node")}
		}},
		{"empty_version_takes_the_sole_complete_entry", "", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			dir := runtimeEntry(t, rt, entry, true)
			runtimeEntry(t, rt, "2.38.0-0123456789abcdef", false)
			return KASLookup{RuntimeDir: rt, Home: home},
				KASRuntime{Node: filepath.Join(dir, "node"), Server: filepath.Join(dir, "kas", kasServerRel)}
		}},
		{"empty_version_takes_none_of_two", "", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			runtimeEntry(t, rt, entry, true)
			runtimeEntry(t, rt, "2.38.0-0123456789abcdef", true)
			return KASLookup{RuntimeDir: rt, Home: home}, KASRuntime{}
		}},
		{"node_override_wins", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			dir := runtimeEntry(t, rt, entry, true)
			return KASLookup{NodeOverride: "/opt/node", RuntimeDir: rt, Home: home},
				KASRuntime{Node: "/opt/node", Server: filepath.Join(dir, "kas", kasServerRel)}
		}},
		{"unwritable_root_fallback_finds_the_data_dir", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			data := dataDir(t, home, dataEntry, "2.32.0-aaaa")
			return KASLookup{RuntimeDir: filepath.Join(rt, "missing"), Home: home},
				KASRuntime{Node: filepath.Join(data, "node"), Server: filepath.Join(data, "kas", dataEntry, kasServerRel)}
		}},
		{"xdg_data_home_beats_home", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			dataDir(t, home, dataEntry)
			xdg := filepath.Join(rt, "xdg")
			touch(t, filepath.Join(xdg, "kiro-cli", "node"))
			return KASLookup{DataHome: xdg, Home: home}, KASRuntime{Node: filepath.Join(xdg, "kiro-cli", "node")}
		}},
		{"nothing_set", "2.37.0", func(t *testing.T, rt, home string) (KASLookup, KASRuntime) {
			return KASLookup{}, KASRuntime{}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, want := tc.setup(t, t.TempDir(), t.TempDir())
			if got := l.Find(tc.version); got != want {
				t.Errorf("Find(%q) = %+v, want %+v", tc.version, got, want)
			}
		})
	}
}
