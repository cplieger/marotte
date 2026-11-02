package steering

import "testing"

func TestCheckPath(t *testing.T) {
	for _, tc := range []struct {
		name, dir string
		ok        bool
	}{
		{"plain", "/config/tools", true},
		{"spaces and quotes", `/srv/my tools/it's "here"`, true},
		{"shell and Markdown punctuation", "/srv/$(x)/#*_[a](b)<c>", true},
		{"letters beyond ASCII", "/srv/é/日本/😀", true},
		{"line feed", "/srv/a\nb", false},
		{"trailing line feed", "/srv/a\n", false},
		{"carriage return", "/srv/a\rb", false},
		{"tab", "/srv/a\tb", false},
		{"escape", "/srv/a\x1bb", false},
		{"next line", "/srv/a\u0085b", false},
		{"backtick", "/srv/a`b", false},
		{"line separator", "/srv/a\u2028b", false},
		{"paragraph separator", "/srv/a\u2029b", false},
		{"bidi override", "/srv/a\u202eb", false},
		{"zero-width space", "/srv/a\u200bb", false},
		{"soft hyphen", "/srv/a\u00adb", false},
		{"byte order mark", "/srv/a\ufeffb", false},
		{"interlinear annotation anchor", "/srv/a\ufff9b", false},
		{"tag letter", "/srv/a\U000E0041b", false},
		{"unassigned tag-block rune", "/srv/a\U000E0000b", false},
		{"invalid UTF-8", "/srv/a\xffb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckPath(tc.dir); (err == nil) != tc.ok {
				t.Errorf("CheckPath(%q) = %v, want accepted %v", tc.dir, err, tc.ok)
			}
		})
	}
}
