// Package workspace provides path helpers for kiro-cli's per-user state directory.
package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var kiroHome string

var kiroHomeOnce sync.Once

// kiroHomeResolver resolves the kiro home path, set at startup by SetKiroHomeResolver;
// unset, KiroHome falls back to $HOME/.kiro.
var kiroHomeResolver func() string

// SetKiroHomeResolver sets the function that resolves the kiro home directory. Call it
// once at startup, before any KiroHome call.
func SetKiroHomeResolver(fn func() string) {
	kiroHomeResolver = fn
}

// KiroHome returns the directory kiro-cli uses for per-user state, as the startup resolver
// (ResolveKiroHome) decided it, else $HOME/.kiro. All kiro-cli state access goes through this
// helper.
func KiroHome() string {
	if kiroHomeResolver != nil {
		kiroHomeOnce.Do(func() {
			kiroHome = kiroHomeResolver()
		})
		return kiroHome
	}
	// Not cached, so tests using t.Setenv("HOME", ...) see the updated value.
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kiro"
	}
	return filepath.Join(home, ".kiro")
}

// errNoKiroHome is KiroHomeDir's answer when no home directory resolved.
var errNoKiroHome = errors.New("workspace: no home directory, so no kiro-cli state directory")

// KiroHomeDir is KiroHome for a caller that writes there: it fails rather than answer the
// relative ".kiro" fallback, which would land under the process's cwd.
func KiroHomeDir() (string, error) {
	dir := KiroHome()
	if !filepath.IsAbs(dir) {
		return "", errNoKiroHome
	}
	return dir, nil
}

// KiroSteeringPath returns the path to a file under KiroHome()/steering/.
func KiroSteeringPath(name string) string {
	return filepath.Join(KiroHome(), "steering", name)
}

// KiroSettingsPath returns the path to a file under KiroHome()/settings/.
func KiroSettingsPath(name string) string {
	return filepath.Join(KiroHome(), "settings", name)
}

// ResolveKiroHome returns the config root KAS takes from kiroHome (the KIRO_HOME value) and home:
// kiroHome when it is absolute, not a filesystem root, and neither equal to home nor holding home
// inside it, else home/.kiro. Symlinks are resolved, and "inside" is tested as KAS tests it: a home
// whose relative path from kiroHome starts with ".." (such as "..home" directly below it) is not
// inside. A mismatch with KAS's own rule (kiro-cli 2.31.0 acp-server.js P8o/O9u) would have
// marotte read and write a different tree from the agent's.
func ResolveKiroHome(kiroHome, home string) string {
	fallback := filepath.Join(home, ".kiro")
	v := strings.TrimSpace(kiroHome)
	if v == "" || !filepath.IsAbs(v) {
		return fallback
	}
	resolved := realpathPrefix(filepath.Clean(v))
	if filepath.Dir(resolved) == resolved {
		return fallback
	}
	rel, err := filepath.Rel(resolved, realpathPrefix(filepath.Clean(home)))
	// O9u's startsWith("..") is a byte-prefix test, so a home whose relative path from the
	// candidate starts with ".." (such as "..home" directly below it) is outside it, and KAS
	// honours the candidate.
	if err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return fallback
	}
	return filepath.Clean(v)
}

// realpathPrefix resolves symlinks in the longest existing prefix of p and keeps the rest, as
// KAS's A8o does, so a not-yet-created KIRO_HOME still compares against home.
func realpathPrefix(p string) string {
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, tail...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}
