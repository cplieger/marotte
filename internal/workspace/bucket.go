package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SessionsBucket is the directory kiro-cli names workDir by under KiroHome's sessions/ and workspace-roots/ trees:
// the first 16 hex characters of the sha256 over the absolute, cleaned root, KAS's computeWorkspaceHash on Linux. A
// divergent value names a directory KAS never reads.
func SessionsBucket(workDir string) string {
	sum := sha256.Sum256([]byte(canonicalWorkDir(workDir)))
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
