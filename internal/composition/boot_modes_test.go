package composition

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const (
	msgEngineRootTightened = "a folder the tools engine checks let other accounts write; removing that, but anything they wrote there stays"
	msgModeChanged         = "changed the mode of a folder marotte uses"
	msgModeLeft            = "a folder marotte sets the mode of is a symlink or not a folder, so it and the folders below it keep their modes"
)

type modeLog struct {
	Msg     string `json:"msg"`
	Dir     string `json:"dir"`
	Mode    string `json:"mode"`
	NewMode string `json:"new_mode"`
}

func modeLogs(t *testing.T, logs *bytes.Buffer) []modeLog {
	t.Helper()
	var out []modeLog
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec modeLog
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec.Dir != "" {
			out = append(out, rec)
		}
	}
	return out
}

func permOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// bootLayout is the image's layout under one temp root: the config dir holding the tools dir and
// HOME, KIRO_HOME at HOME's .kiro, every folder the boot sets created at mode.
type bootLayout struct{ config, tools, home, kiroHome string }

func newBootLayout(t *testing.T, mode fs.FileMode) bootLayout {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config")
	l := bootLayout{config: config, tools: filepath.Join(config, "tools"), home: filepath.Join(config, "home")}
	l.kiroHome = filepath.Join(l.home, ".kiro")
	for _, d := range l.dirs() {
		mkdirMode(t, d, mode)
	}
	return l
}

func (l bootLayout) dirs() []string {
	dirs := []string{l.config, l.tools, l.home, l.kiroHome, filepath.Join(l.tools, "kiro-cli-versions"), filepath.Join(l.home, ".ssh")}
	for _, rel := range []string{"bin", "opt", "npm", "npm/bin", "npm/lib", "npm/lib/node_modules", "python", "python/bin", "python/tools", "python/interpreters"} {
		dirs = append(dirs, filepath.Join(l.tools, rel))
	}
	for _, rel := range []string{".local", ".local/share", ".local/share/kiro-cli", ".cache", ".cache/go-build", ".docker", ".docker/cli-plugins"} {
		dirs = append(dirs, filepath.Join(l.home, rel))
	}
	return dirs
}

func (l bootLayout) heal() { healBootModes(l.config, l.tools, l.home, l.kiroHome) }

func TestHealBootModes_SetsEveryBootFolderAndWarnsWithTheOldMode(t *testing.T) {
	l := newBootLayout(t, 0o777)
	logs := captureDefaultLogger(t)

	l.heal()

	for _, d := range l.dirs() {
		want := fs.FileMode(0o755)
		if d == filepath.Join(l.home, ".ssh") {
			want = 0o700
		}
		if got := permOf(t, d); got != want {
			t.Errorf("%s mode = %04o, want %04o", d, got, want)
		}
	}
	got := modeLogs(t, logs)
	if len(got) != len(l.dirs()) {
		t.Fatalf("logged %d mode lines, want one per folder (%d): %+v", len(got), len(l.dirs()), got)
	}
	engineRoot := map[string]bool{l.config: true, l.tools: true}
	for _, rel := range []string{"bin", "opt", "npm", "npm/bin", "npm/lib", "npm/lib/node_modules", "python", "python/bin", "python/tools", "python/interpreters"} {
		engineRoot[filepath.Join(l.tools, rel)] = true
	}
	for _, rec := range got {
		wantMsg := msgModeChanged
		if engineRoot[rec.Dir] {
			wantMsg = msgEngineRootTightened
		}
		if rec.Msg != wantMsg || rec.Mode != "0777" {
			t.Errorf("logged %+v, want msg %q with mode 0777", rec, wantMsg)
		}
	}

	again := captureDefaultLogger(t)
	l.heal()
	if rest := modeLogs(t, again); len(rest) != 0 {
		t.Errorf("a second boot logged %+v, want nothing", rest)
	}
}

// HOME 755 widens a 0700 HOME; a folder already narrower than go-w stays as it is.
func TestHealBootModes_SetsHomeExactlyAndOnlyTightensTheRest(t *testing.T) {
	l := newBootLayout(t, 0o700)
	captureDefaultLogger(t)

	l.heal()

	if got := permOf(t, l.home); got != 0o755 {
		t.Errorf("HOME mode = %04o, want 0755", got)
	}
	for _, d := range []string{l.config, l.kiroHome, filepath.Join(l.tools, "bin"), filepath.Join(l.home, ".cache")} {
		if got := permOf(t, d); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700 kept", d, got)
		}
	}
}

// A symlink planted at any folder the boot sets is never followed: its target keeps its mode, the
// planted folder is named once, and the real folders around it are still set.
func TestHealBootModes_LeavesEveryPlantedSymlink(t *testing.T) {
	cases := map[string]struct {
		at func(l bootLayout) string
	}{
		"config dir":        {at: func(l bootLayout) string { return l.config }},
		"tools dir":         {at: func(l bootLayout) string { return l.tools }},
		"npm":               {at: func(l bootLayout) string { return filepath.Join(l.tools, "npm") }},
		"npm lib":           {at: func(l bootLayout) string { return filepath.Join(l.tools, "npm", "lib") }},
		"kiro-cli-versions": {at: func(l bootLayout) string { return filepath.Join(l.tools, "kiro-cli-versions") }},
		"HOME":              {at: func(l bootLayout) string { return l.home }},
		".kiro":             {at: func(l bootLayout) string { return l.kiroHome }},
		".ssh":              {at: func(l bootLayout) string { return filepath.Join(l.home, ".ssh") }},
		".docker":           {at: func(l bootLayout) string { return filepath.Join(l.home, ".docker") }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l := newBootLayout(t, 0o777)
			planted := tc.at(l)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Rename(planted, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, planted); err != nil {
				t.Fatal(err)
			}
			logs := captureDefaultLogger(t)

			l.heal()

			if got := permOf(t, outside); got != 0o777 {
				t.Errorf("the symlink's target became %04o, want 0777 kept", got)
			}
			var left []string
			for _, rec := range modeLogs(t, logs) {
				if rec.Msg == msgModeLeft {
					left = append(left, rec.Dir)
				}
			}
			if !slices.Equal(left, []string{planted}) {
				t.Errorf("left folders named %v, want only %s", left, planted)
			}
			if sibling := filepath.Join(l.tools, "python"); planted != l.config && planted != l.tools {
				if got := permOf(t, sibling); got != 0o755 {
					t.Errorf("python mode = %04o, want 0755 beside the planted link", got)
				}
			}
		})
	}
}

// HOME given with a trailing slash, and KIRO_HOME named through it, still reach .kiro from HOME.
func TestHealBootModes_TrailingSlashHomeKeepsTheWalk(t *testing.T) {
	l := newBootLayout(t, 0o777)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(l.kiroHome, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, l.kiroHome); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	healBootModes(l.config, l.tools, l.home+"/", l.home+"/.kiro")

	if got := permOf(t, outside); got != 0o777 {
		t.Errorf("the planted .kiro's target became %04o, want 0777 kept", got)
	}
	if got := permOf(t, filepath.Join(l.home, ".ssh")); got != 0o700 {
		t.Errorf(".ssh mode = %04o, want 0700", got)
	}
}

// A symlinked HOME is left; a KIRO_HOME named at its target is opened through its parent, and a
// .kiro planted there is not followed.
func TestHealBootModes_SymlinkedHomeWithKiroHomeAtItsTarget(t *testing.T) {
	l := newBootLayout(t, 0o777)
	realHome := filepath.Join(t.TempDir(), "home")
	if err := os.Rename(l.home, realHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, l.home); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(filepath.Join(realHome, ".kiro"), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(realHome, ".kiro")); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	healBootModes(l.config, l.tools, l.home, filepath.Join(realHome, ".kiro"))

	if got := permOf(t, outside); got != 0o777 {
		t.Errorf("the planted .kiro's target became %04o, want 0777 kept", got)
	}
	if got := permOf(t, realHome); got != 0o777 {
		t.Errorf("the symlinked HOME's target became %04o, want 0777 kept", got)
	}
}

func TestHealBootModes_SkipsMissingFoldersAndEmptyPaths(t *testing.T) {
	root := t.TempDir()
	logs := captureDefaultLogger(t)

	healBootModes(filepath.Join(root, "config"), "", filepath.Join(root, "home"), "")

	if got := modeLogs(t, logs); len(got) != 0 {
		t.Errorf("logged %+v for missing folders, want nothing", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("created %v, want nothing", entries)
	}
}

func TestClaimAndHealRoots_SetsModesOnceClaimed(t *testing.T) {
	l := newBootLayout(t, 0o777)
	t.Setenv("HOME", l.home)
	captureDefaultLogger(t)

	roots, err := claimAndHealRoots(&Config{ConfigDir: l.config, ToolsDir: l.tools})
	if err != nil {
		t.Fatalf("claimAndHealRoots = %v", err)
	}
	t.Cleanup(roots.release)

	for d, want := range map[string]fs.FileMode{l.config: 0o755, l.kiroHome: 0o755, filepath.Join(l.home, ".ssh"): 0o700} {
		if got := permOf(t, d); got != want {
			t.Errorf("%s mode = %04o, want %04o", d, got, want)
		}
	}
}
