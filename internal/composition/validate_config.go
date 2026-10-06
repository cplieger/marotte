package composition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/cplieger/atomicfile/v4"
)

// validateConfig performs fail-fast checks so a misconfiguration is a clear startup error naming
// the env var and value. kiro-cli is not checked: the manager installs it after this runs and
// reports through /api/health.
func validateConfig(ctx context.Context, cfg *Config) error {
	var errs []error

	if err := checkDirWritable(ctx, cfg.ConfigDir, "KIRO_CONFIG_DIR"); err != nil {
		errs = append(errs, err)
	}

	if info, err := os.Stat(cfg.WorkDir); err != nil {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: %w", cfg.WorkDir, err))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Errorf("KIRO_WORK_DIR=%q: not a directory", cfg.WorkDir))
	}

	return errors.Join(errs...)
}

// checkDirWritable verifies dir exists, is a directory and accepts a real write via
// atomicfile.ProbeWritable, whose temp shape sweepStaleTemps reclaims. A directory that never
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
	res, err := atomicfile.ProbeWritable(ctx, dir, atomicfile.WithMode(0o600))
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
