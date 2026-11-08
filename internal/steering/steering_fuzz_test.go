package steering

import (
	"testing"
)

func FuzzParseSteeringFrontmatter(f *testing.F) {
	f.Add([]byte("---\ninclusion: always\n---\n# Hello"))
	f.Add([]byte("---\ninclusion: fileMatch\nfileMatchPattern: \"*.go\"\n---\nbody"))
	f.Add([]byte("---\ninclusion: manual\ndescription: \"test\"\n---\n"))
	f.Add([]byte("no frontmatter"))
	f.Add([]byte(""))
	f.Add([]byte("---\n---\n"))
	f.Add([]byte("---\ninclusion: bogus\n---\n"))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		doc := parseSteeringFrontmatter(data)

		switch doc.Inclusion {
		case "always", "fileMatch", "manual":
			// ok
		default:
			t.Errorf("parseSteeringFrontmatter: unexpected inclusion %q", doc.Inclusion)
		}

		if len(data) == 0 && doc.Inclusion != "always" {
			t.Errorf("parseSteeringFrontmatter(nil): got inclusion %q, want always", doc.Inclusion)
		}
	})
}
