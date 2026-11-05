package steering

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWriteTools_staysOneLinePerEntry checks that no inventory field or tools-state.json content
// can forge a line or close a code span in the section.
func FuzzWriteTools_staysOneLinePerEntry(f *testing.F) {
	f.Add([]byte(`{"tools":{"go":{"bins":["go","gofmt"]}}}`), "go", "aqua", "1.26.2")
	f.Add([]byte(`{"tools":{"x":{"pm_bins":["a\nb"]}}}`), "x", "npm", "1.0")
	f.Add([]byte(`{}`), "jq", "apt", "")
	f.Add([]byte(`null`), "a`b", "k\nind", "v\r1")
	f.Add([]byte(`invalid json`), "", "", "")
	f.Add([]byte(``), "\u2028", "aqua", "1")

	f.Fuzz(func(t *testing.T, state []byte, name, kind, version string) {
		var b strings.Builder
		writeTools(&b, []InstalledTool{{Name: name, Kind: kind, Version: version}}, state)
		result := b.String()

		if !utf8.ValidString(result) {
			t.Fatalf("writeTools produced invalid UTF-8: %q", result)
		}
		if !strings.HasPrefix(result, "## Installed tools\n\n") {
			t.Fatalf("writeTools output does not start with the header: %q", result)
		}
		for line := range strings.SplitSeq(strings.TrimPrefix(result, "## Installed tools\n\n"), "\n") {
			if line != "" && !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "Installed packages with no recorded command") {
				t.Fatalf("writeTools wrote a line that is no entry: %q in %q", line, result)
			}
			if strings.HasPrefix(line, "- ") && strings.Contains(line, "`") {
				t.Fatalf("writeTools let a backtick into an entry: %q", line)
			}
		}
	})
}
