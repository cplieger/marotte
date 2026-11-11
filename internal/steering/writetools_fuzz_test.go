package steering

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWriteTools_keepsItsLineShape checks that no inventory field can forge a line or close a
// code span: whatever the names and versions, the section holds the same lines in the same order.
func FuzzWriteTools_keepsItsLineShape(f *testing.F) {
	f.Add("go", "1.26.2", "git")
	f.Add("a\nb", "1.0", "x\n## Capabilities")
	f.Add("a`b", "v\r1", "c`d")
	f.Add("", "", "")
	f.Add("\u2028", "1", "\u202e")

	f.Fuzz(func(t *testing.T, name, version, system string) {
		var b strings.Builder
		writeTools(&b, ToolsSnapshot{
			Tools:  []InstalledTool{{Version: version, Commands: []string{name}}, {Version: "1"}},
			System: []string{system},
		}, true, "127.0.0.1:9847")
		result := b.String()

		if !utf8.ValidString(result) {
			t.Fatalf("writeTools produced invalid UTF-8: %q", result)
		}
		var shape []string
		for line := range strings.SplitSeq(result, "\n") {
			if line == "" {
				continue
			}
			label, _, _ := strings.Cut(line, ":")
			shape = append(shape, label)
			if strings.HasPrefix(line, "Commands: ") || strings.HasPrefix(line, "Baked into the image: ") {
				if strings.Contains(line, "`") {
					t.Fatalf("writeTools let a backtick into a list: %q", line)
				}
			}
		}
		for _, l := range shape {
			switch l {
			case "## Installed tools", "Commands", "Baked into the image", "The image is Debian slim",
				"1 more installed packages ship no command (mostly libraries)":
			default:
				t.Fatalf("writeTools wrote a line that is none of its own: %q in %q", l, result)
			}
		}
	})
}
