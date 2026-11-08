package composition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/pathinside"
)

// validateConfig performs fail-fast checks so a misconfiguration is a clear startup error naming
// the env var and value. kiro-cli is not checked: the manager installs it after this runs and
// reports through /healthz.
func validateConfig(ctx context.Context, cfg *Config) error {
	var errs []error
	if cfg.notifyErr != nil {
		errs = append(errs, cfg.notifyErr)
	}

	// Both folders are written into the generated steering documents verbatim.
	for _, d := range []struct{ env, dir string }{{"KIRO_CONFIG_DIR", cfg.ConfigDir}, {"KIRO_WORK_DIR", cfg.WorkDir}} {
		if err := dirProblem(d.env, d.dir); err != nil {
			errs = append(errs, err)
		}
	}

	if err := checkDirWritable(ctx, cfg.ConfigDir, "KIRO_CONFIG_DIR"); err != nil {
		errs = append(errs, err)
	}

	if info, err := os.Stat(cfg.WorkDir); err != nil {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: %w", cfg.WorkDir, err))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: not a directory", cfg.WorkDir))
	}
	if err := checkWorkDirOutsideConfig(cfg.WorkDir, cfg.ConfigDir); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// checkWorkDirOutsideConfig refuses a workspace that is or contains the config directory, compared
// as written and as resolved: agents write anywhere in the workspace, and the config directory
// holds the settings that set their own permissions and the Kiro sign-in.
func checkWorkDirOutsideConfig(workDir, configDir string) error {
	for _, work := range spellings(workDir) {
		if slices.ContainsFunc(spellings(configDir), pathinside.Root(work).Contains) {
			return fmt.Errorf("KIRO_WORK_DIR=%q is or contains KIRO_CONFIG_DIR=%q, so agents could edit their own permission settings and the Kiro sign-in", workDir, configDir)
		}
	}
	return nil
}

// spellings is dir cleaned and, when it resolves elsewhere, resolved; an empty dir yields nothing.
func spellings(dir string) []string {
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	out := []string{clean}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
		out = append(out, resolved)
	}
	return out
}

// checkDirWritable verifies dir exists, is a directory and accepts a real write via
// filesafe.ProbeWritable, whose temp shape sweepStaleTemps reclaims. A directory that never
// accepted a write is fatal; a close or remove failure after a flushed write only warns, since the
// directory is usable.
func checkDirWritable(ctx context.Context, dir, envVar string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", envVar, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s=%q: not a directory", envVar, dir)
	}
	// Stat mode bits can lie (NFS, FUSE, volume permissions), so only a real write proves it.
	// WithMode matches the private writes this directory holds.
	res, err := filesafe.ProbeWritable(ctx, dir, filesafe.WithMode(0o600))
	if err != nil {
		// Not a verdict on the directory: the probe was never attempted, so
		// writability is unproven and this stays fatal.
		return fmt.Errorf("%s=%q: writability probe not attempted: %w", envVar, dir, err)
	}
	if !res.Writable() {
		return fmt.Errorf("%s=%q: not writable: %w", envVar, dir, res.Err)
	}
	if !res.OK() {
		slog.Warn("directory accepts writes but refused probe cleanup",
			"env", envVar, "dir", res.Dir, "stage", res.Stage.String(),
			"probe", res.Name, "leaked", res.Leaked, "error", res.Err)
	}
	return nil
}
