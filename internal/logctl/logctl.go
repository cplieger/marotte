// Package logctl owns the process-wide slog handler so the debug_logs setting
// (<configDir>/config.json) flips the level at runtime: Install once at startup picks the boot
// level, and SetDebug moves a shared slog.LevelVar the handler reads at log time (Info by default,
// Debug when set).
package logctl

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/slogx"
)

// levelVar is the shared LevelVar the installed handler follows. Pre-initialized so no exported
// call can nil-deref before Install (FuzzSetDebug trips that directly); a pre-Install Set lands on
// this throwaway, and Install swaps in slogx's.
var levelVar = new(slog.LevelVar)

// Install wires the shared LevelVar into slog's default logger and reads the initial level from
// configDir/config.json (settings.DebugLogs); call once at startup, before any slog call that matters.
func Install(ctx context.Context, configDir string) {
	levelVar = slogx.Setup(slogx.Options{})
	if settings.DebugLogs(ctx, configDir) {
		levelVar.Set(slog.LevelDebug)
	}
}

// Debug reports whether the active log level is debug.
func Debug() bool { return levelVar.Level() <= slog.LevelDebug }

// SetDebug flips the active log level at runtime.
func SetDebug(on bool) {
	if on {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
}
