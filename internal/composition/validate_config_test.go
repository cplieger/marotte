package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v4"
)

// TestCheckDirWritable covers the probe's verdicts, including a teardown failure after a flushed
// write, which warns rather than fails.
func TestCheckDirWritable(t *testing.T) {
	t.Run("writable dir returns nil", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err != nil {
			t.Errorf("checkDirWritable(writable dir) = %v, want nil", err)
		}
	})

	t.Run("successful probe leaves nothing behind", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err != nil {
			t.Fatalf("checkDirWritable(writable dir) = %v, want nil", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("probe left %v behind, want an empty directory", names)
		}
	})

	t.Run("missing dir returns error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		if err := checkDirWritable(t.Context(), dir, "TEST_DIR"); err == nil {
			t.Error("checkDirWritable(missing dir) = nil, want error")
		}
	})

	t.Run("file where a dir belongs returns error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkDirWritable(t.Context(), path, "TEST_DIR"); err == nil {
			t.Error("checkDirWritable(regular file) = nil, want error")
		}
	})
}

// TestCheckDirWritable_ProbeNameIsSweepable asserts that whatever name the writability probe creates,
// sweepStaleTemps recognises it.
func TestCheckDirWritable_ProbeNameIsSweepable(t *testing.T) {
	name := atomicfile.TempName()
	if !atomicfile.IsPackageTemp(name) {
		t.Fatalf("atomicfile.TempName() = %q, which IsPackageTemp rejects", name)
	}
	if atomicfile.IsPackageTemp(".marotte-probe-123") {
		t.Error("the retired app-invented probe name reads as sweepable; the sweep never reclaimed it")
	}
}

// TestValidateConfig_MissingCLIIsNotFatal asserts that validation must not probe for kiro-cli, which may still
// be installing.
func TestValidateConfig_MissingCLIIsNotFatal(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir(), WorkDir: t.TempDir()}
	if err := validateConfig(t.Context(), cfg); err != nil {
		t.Fatalf("validateConfig with no kiro-cli installed yet = %v, want nil (degraded, not fatal)", err)
	}
}

// TestValidateConfig_PropagatesAnUnusableConfigDir asserts that the config-dir verdict reaches the caller,
// joined with the other check so both are seen.
func TestValidateConfig_PropagatesAnUnusableConfigDir(t *testing.T) {
	work := t.TempDir()

	t.Run("an absent config dir is fatal and names its variable", func(t *testing.T) {
		cfg := &Config{ConfigDir: filepath.Join(t.TempDir(), "absent"), WorkDir: work}
		err := validateConfig(t.Context(), cfg)
		if err == nil {
			t.Fatal("validateConfig with an absent KIRO_CONFIG_DIR = nil, want a fatal error")
		}
		if !strings.Contains(err.Error(), "KIRO_CONFIG_DIR") {
			t.Errorf("err = %v, want it to name KIRO_CONFIG_DIR so the operator knows which path to fix", err)
		}
	})

	t.Run("a config dir that is a file is fatal and names its variable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		cfg := &Config{ConfigDir: path, WorkDir: work}
		err := validateConfig(t.Context(), cfg)
		if err == nil {
			t.Fatal("validateConfig with a file as KIRO_CONFIG_DIR = nil, want a fatal error")
		}
		if !strings.Contains(err.Error(), "KIRO_CONFIG_DIR") {
			t.Errorf("err = %v, want it to name KIRO_CONFIG_DIR", err)
		}
	})
}

// Both folders are written verbatim into the generated steering documents, so a path they cannot
// carry, or a relative one, refuses the boot naming its variable, though the folder exists.
func TestValidateConfig_RefusesAFolderTheSteeringDocsCannotCarry(t *testing.T) {
	t.Chdir(t.TempDir())
	good := t.TempDir()
	for _, tc := range []struct {
		name, env, config, work string
	}{
		{"config dir with a newline", "KIRO_CONFIG_DIR", good + "/c\n## forged", good},
		{"config dir with a backtick", "KIRO_CONFIG_DIR", good + "/c`x", good},
		{"relative config dir", "KIRO_CONFIG_DIR", "config", good},
		{"work dir with a line separator", "KIRO_WORK_DIR", good, good + "/w\u2028x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, d := range []string{tc.config, tc.work} {
				if err := os.MkdirAll(d, 0o750); err != nil {
					t.Fatalf("Setup: %v", err)
				}
			}
			err := validateConfig(t.Context(), &Config{ConfigDir: tc.config, WorkDir: tc.work})
			if err == nil || !strings.Contains(err.Error(), tc.env+"=") {
				t.Errorf("validateConfig = %v, want an error naming %s", err, tc.env)
			}
		})
	}
}

// TestValidateConfig_RefusesAWorkspaceOverTheConfigDir asserts that a workspace equal to or
// containing the config directory, spelled directly or through a symlink, is fatal and names both
// variables, while a workspace beside it starts.
func TestValidateConfig_RefusesAWorkspaceOverTheConfigDir(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "real")
	config := filepath.Join(parent, "config")
	beside := filepath.Join(root, "workspace")
	for _, dir := range []string{config, beside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, work string
	}{
		{"the workspace is the config dir", config},
		{"the workspace contains the config dir", parent},
		{"the workspace contains the config dir through a symlink", link},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(t.Context(), &Config{ConfigDir: config, WorkDir: tc.work})
			if err == nil {
				t.Fatalf("validateConfig(KIRO_WORK_DIR=%s, KIRO_CONFIG_DIR=%s) = nil, want a fatal error", tc.work, config)
			}
			if msg := err.Error(); !strings.Contains(msg, "KIRO_WORK_DIR") || !strings.Contains(msg, "KIRO_CONFIG_DIR") {
				t.Errorf("err = %v, want it to name KIRO_WORK_DIR and KIRO_CONFIG_DIR", err)
			}
		})
	}

	t.Run("a workspace beside the config dir starts", func(t *testing.T) {
		if err := validateConfig(t.Context(), &Config{ConfigDir: config, WorkDir: beside}); err != nil {
			t.Errorf("validateConfig with KIRO_WORK_DIR beside KIRO_CONFIG_DIR = %v, want nil", err)
		}
	})
}
