// Package workspace provides path helpers for kiro-cli's per-user state directory.
package workspace

import (
	"os"
	"path/filepath"
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

// KiroHome returns the directory kiro-cli uses for per-user state: $KIRO_HOME if set,
// else $HOME/.kiro. The image sets KIRO_HOME to $HOME/.kiro and the two MUST stay equal:
// the Rust wrapper honors KIRO_HOME, but KAS never reads it (os.homedir()/.kiro, verified
// against the 2.12 bundle). All kiro-cli state access goes through this helper.
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

// KiroSteeringPath returns the path to a file under KiroHome()/steering/.
func KiroSteeringPath(name string) string {
	return filepath.Join(KiroHome(), "steering", name)
}

// KiroSettingsPath returns the path to a file under KiroHome()/settings/.
func KiroSettingsPath(name string) string {
	return filepath.Join(KiroHome(), "settings", name)
}
