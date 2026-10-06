// Package systembin resolves an image-baked system binary to an absolute path from a fixed
// directory set, so a server spawn's argv[0] does not come from PATH, whose first entry
// (/config/tools/bin, on the persistent volume) an agent shell can write.
//
// Only for binaries whose trust argument holds: git and bash are apt packages in /usr/bin,
// which nothing at runtime writes. NOT a general resolver: a toolbelt-managed binary (npm)
// lives in that writable directory, so pinning it would confine nothing. It reads no
// environment (no PATH, no override), and excludes /usr/local/bin.
package systembin

import (
	"os"
	"path/filepath"
	"strings"
)

// systemDirs is the trusted candidate set, in order. A var rather than a const
// slice so a test can point it at a temp dir; production never reassigns it.
var systemDirs = []string{"/usr/bin", "/bin"}

// Resolve returns the absolute path of an image-baked binary and whether it was found. On a
// miss the caller must REFUSE: one site's bare-name fallback voids the pin everywhere. A name
// containing a path separator is refused.
func Resolve(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", false
	}
	for _, dir := range systemDirs {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		// Regular AND executable, or a spawn would fail as if the binary were broken.
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}
