package git

import (
	"strings"
	"testing"
)

func FuzzValidateFilePath(f *testing.F) {
	f.Add("src/main.go")
	f.Add("-flag")
	f.Add("../escape")
	f.Add("/absolute")
	f.Add("has\x00null")
	f.Add("has\x01ctrl")
	f.Add("")
	f.Add("normal/path/file.txt")
	f.Add("v1..v2.txt")
	f.Add("a..b/main.go")
	f.Add("..extras/movie.mkv")
	f.Add("...")
	f.Add("a/../b")
	f.Add("a/..")

	f.Fuzz(func(t *testing.T, path string) {
		ok := validateFilePath(path)
		// Both directions: an accepted path never carries a ".." component, and any path that does
		// is refused.
		for comp := range strings.SplitSeq(path, "/") {
			if comp == ".." && ok {
				t.Fatalf("accepted path with a .. component: %q", path)
			}
		}
		if !ok {
			return
		}
		if strings.HasPrefix(path, "-") {
			t.Fatal("accepted path with leading dash")
		}
		if strings.HasPrefix(path, "/") {
			t.Fatal("accepted absolute path")
		}
		for _, r := range path {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("accepted path with control byte: %q", path)
			}
		}
	})
}
