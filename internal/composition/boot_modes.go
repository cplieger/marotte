package composition

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/kirorelease"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/workspace"
)

// kiroCLIVersionsDir is pin's install root below its Config.Root: the release name plus
// "-versions".
const kiroCLIVersionsDir = kirorelease.Name + "-versions"

// engineRootDirs are the folders below the tools dir that toolbelt's root-integrity check judges,
// beside the config and tools dirs (its docs/configuration.md "Root integrity check").
var engineRootDirs = []string{
	"bin", "opt", "npm", "npm/bin", "npm/lib", "npm/lib/node_modules",
	"python", "python/bin", "python/tools", "python/interpreters",
}

var homeDirs = []string{
	".local", ".local/share", ".local/share/kiro-cli", ".cache", ".cache/go-build",
	".docker", ".docker/cli-plugins",
}

// claimAndHealRoots claims the folders this instance writes into, then sets their modes before
// toolbelt.New judges them; a refused claim changes no mode in the live instance's folders.
func claimAndHealRoots(cfg *Config) (*heldRoots, error) {
	roots, err := claimRoots(ownedRoots(cfg))
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	toolsDir, _ := cfg.Tools.use()
	healBootModes(cfg.ConfigDir, toolsDir, home, workspace.KiroHome())
	return roots, nil
}

// healBootModes sets the folder modes a boot needs, since an inheritable ACL or umask on the host
// volume can stamp any mode: the tools engine refuses to start over a judged folder another account
// can write, and ssh refuses a lax ~/.ssh. It warns for each change and each folder it had to leave.
// An empty dir is skipped.
func healBootModes(configDir, toolsDir, home, kiroHome string) {
	var dirs []filemode.DirMode
	engineRoots := map[string]bool{}
	add := func(dir string, want func(fs.FileMode) fs.FileMode, engineRoot bool) {
		dirs = append(dirs, filemode.DirMode{Path: dir, Want: want})
		if engineRoot {
			engineRoots[filepath.Clean(dir)] = true
		}
	}
	tighten := filemode.NoGroupOrOtherWrite
	if configDir != "" {
		add(configDir, tighten, true)
	}
	if toolsDir != "" {
		add(toolsDir, tighten, true)
		for _, rel := range engineRootDirs {
			add(filepath.Join(toolsDir, rel), tighten, true)
		}
		add(filepath.Join(toolsDir, kiroCLIVersionsDir), tighten, false)
	}
	if home != "" {
		add(home, filemode.Exactly(0o755), false)
		for _, rel := range homeDirs {
			add(filepath.Join(home, rel), tighten, false)
		}
		add(filepath.Join(home, ".ssh"), filemode.Exactly(0o700), false)
	}
	if kiroHome != "" {
		add(kiroHome, tighten, false)
	}
	for _, r := range filemode.SetDirModes(dirs) {
		logModeResult(r, engineRoots[r.Path])
	}
}

func logModeResult(r filemode.DirModeResult, engineRoot bool) {
	dir := logsafe.Field(r.Path)
	switch {
	case r.Err == nil && engineRoot:
		slog.Warn("a folder the tools engine checks let other accounts write; removing that, but anything they wrote there stays",
			"dir", dir, "mode", octal(r.Old), "new_mode", octal(r.New))
	case r.Err == nil:
		slog.Warn("changed the mode of a folder marotte uses", "dir", dir, "mode", octal(r.Old), "new_mode", octal(r.New))
	case errors.Is(r.Err, filemode.ErrNotADir):
		slog.Warn("found a symlink or a non-folder where marotte sets folder modes, so it and the folders below it keep their modes",
			"dir", dir)
	case r.Stopped:
		slog.Warn("could not open a folder where marotte sets folder modes, so it and the folders below it keep their modes",
			"dir", dir, "error", logsafe.Field(r.Err.Error()))
	default:
		slog.Warn("could not set the mode of a folder marotte uses, so it keeps its mode",
			"dir", dir, "error", logsafe.Field(r.Err.Error()))
	}
}

func octal(m fs.FileMode) string { return fmt.Sprintf("%04o", uint32(m)) }
