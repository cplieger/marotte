package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzCheckDirWritable asserts checkDirWritable never panics and every rejection names the env var.
func FuzzCheckDirWritable(f *testing.F) {
	f.Add("subdir")
	f.Add("")
	f.Add("a/b/c")

	const envVar = "TEST_DIR"
	f.Fuzz(func(t *testing.T, subpath string) {
		tmp := t.TempDir()
		target := filepath.Join(tmp, subpath)
		if target != tmp && !strings.HasPrefix(target, tmp+string(os.PathSeparator)) {
			t.Skip()
		}
		_ = os.MkdirAll(target, 0o755)

		err := checkDirWritable(t.Context(), target, envVar)
		if err == nil {
			return
		}
		msg := err.Error()
		if msg == "" {
			t.Fatalf("checkDirWritable(%q) returned an error with an empty message", target)
		}
		if !strings.Contains(msg, envVar) {
			t.Errorf("checkDirWritable(%q) error %q does not name env var %q", target, msg, envVar)
		}
	})
}
