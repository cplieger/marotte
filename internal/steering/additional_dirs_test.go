package steering

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/settings"
)

func writeAdditionalDirsSetting(t *testing.T, configDir string, dirs []string) {
	t.Helper()
	data, err := json.Marshal(map[string][]string{settings.KeyAdditionalDirectories: dirs})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(configDir, "config.json"), string(data))
}

// grantableDir is a fresh directory as Check requires it, reached through no symlink (t.TempDir
// may sit under a symlinked temp root, which Check refuses).
func grantableDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGenerate_NamesTheAdditionalDirectoriesAsWritable(t *testing.T) {
	steeringPath := setupKiroHome(t)
	workDir, configDir := t.TempDir(), t.TempDir()
	extra := grantableDir(t)
	missing := filepath.Join(t.TempDir(), "gone")
	writeAdditionalDirsSetting(t, configDir, []string{extra, missing, configDir})

	New(workDir, configDir).Generate(t.Context())

	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	_, section, found := strings.Cut(got, "## Additional directories\n")
	if !found {
		t.Fatalf("environment.md has no additional-directories section:\n%s", got)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	if !strings.Contains(section, "- `"+extra+"`") {
		t.Errorf("section does not list %s:\n%s", extra, section)
	}
	if !strings.Contains(section, "read, write") {
		t.Errorf("section does not say the directories are writable:\n%s", section)
	}
	for _, refused := range []string{missing, configDir} {
		if strings.Contains(section, refused) {
			t.Errorf("section names %q, which agents are not granted:\n%s", refused, section)
		}
	}
	ws, ad := strings.Index(got, "## Workspace"), strings.Index(got, "## Additional directories")
	if ws < 0 || ad < ws || ad > strings.Index(got, "## Limitations") {
		t.Errorf("additional directories section is not between Workspace and Limitations:\n%s", got)
	}
}

func TestGenerate_NoAdditionalDirectoriesOmitsTheSection(t *testing.T) {
	for name, dirs := range map[string][]string{"unset": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			steeringPath := setupKiroHome(t)
			workDir, configDir := t.TempDir(), t.TempDir()
			if dirs != nil {
				writeAdditionalDirsSetting(t, configDir, dirs)
			}
			New(workDir, configDir).Generate(t.Context())
			out, err := os.ReadFile(steeringPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "## Additional directories") {
				t.Errorf("environment.md carries the section with no directory granted:\n%s", out)
			}
		})
	}
}

func TestGenerate_RemovingAnAdditionalDirectoryDropsItFromTheNextRender(t *testing.T) {
	steeringPath := setupKiroHome(t)
	workDir, configDir := t.TempDir(), t.TempDir()
	extra := grantableDir(t)
	g := New(workDir, configDir)
	writeAdditionalDirsSetting(t, configDir, []string{extra})
	g.Generate(t.Context())
	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), extra) {
		t.Fatalf("environment.md does not name the granted %s before the removal:\n%s", extra, out)
	}
	writeAdditionalDirsSetting(t, configDir, nil)
	g.Generate(t.Context())
	out, err = os.ReadFile(steeringPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), extra) {
		t.Errorf("environment.md still names the removed %s:\n%s", extra, out)
	}
}
