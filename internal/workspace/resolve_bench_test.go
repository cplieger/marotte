package workspace

import (
	"path/filepath"
	"testing"
)

// BenchmarkResolveInsideAbs measures the production resolver. b.TempDir() is
// symlink-resolved, or every case would time the containment-failure branch.
func BenchmarkResolveInsideAbs(b *testing.B) {
	workDir, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatalf("EvalSymlinks: %v", err)
	}

	b.Run("relative", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, "src/main.go")
		}
	})

	b.Run("absolute_inside", func(b *testing.B) {
		abs := filepath.Join(workDir, "deep", "nested", "file.txt")
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, abs)
		}
	})

	b.Run("missing_parent", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = ResolveInsideAbs(workDir, "nonexistent/dir/file.txt")
		}
	})
}
