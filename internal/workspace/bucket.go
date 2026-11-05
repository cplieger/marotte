package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
)

// SessionsBucket is the directory kiro-cli names a set of roots by under KiroHome's sessions/ and workspace-roots/
// trees: the first 16 hex characters of the sha256 over the absolute, cleaned roots, sorted and joined by NUL, KAS
// 3.0.0's Pw on Linux. One root hashes as itself. A divergent value names a directory KAS never reads.
func SessionsBucket(roots ...string) string {
	canon := make([]string, len(roots))
	for i, r := range roots {
		canon[i] = canonicalWorkDir(r)
	}
	slices.Sort(canon)
	sum := sha256.Sum256([]byte(strings.Join(canon, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// canonicalWorkDir is workDir absolute and cleaned. The explicit Clean covers Abs's error branch (a relative
// workDir with no readable cwd), so the fallback stays canonical too.
func canonicalWorkDir(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return filepath.Clean(workDir)
	}
	return filepath.Clean(abs)
}
