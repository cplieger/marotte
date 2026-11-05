package steering

import (
	"strings"
	"testing"
)

// An accepted path is one defuse would leave alone, so writing it unchanged forges nothing.
func FuzzCheckPath_acceptedPathsNeedNoDefusing(f *testing.F) {
	for _, s := range []string{"/config", "/srv/a b/é", "/a\nb", "/a`b", "/a\u202eb", "/a\U000E0000", "/\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, dir string) {
		if CheckPath(dir) != nil {
			return
		}
		if got := defuse(dir); got != dir {
			t.Errorf("CheckPath accepted %q, which defuse rewrites to %q", dir, got)
		}
		if strings.ContainsAny(dir, "\n\r`") {
			t.Errorf("CheckPath accepted %q, which can leave its code span or line", dir)
		}
	})
}
