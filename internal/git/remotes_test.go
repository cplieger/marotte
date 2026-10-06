package git

import "testing"

func TestParseRemoteSlug(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		host string
		slug string
	}{
		{
			name: "SCPStyle", raw: "git@github.com:cplieger/marotte.git",
			host: "github.com", slug: "cplieger/marotte",
		},
		{
			name: "SCPStyleNoSuffix", raw: "git@github.com:cplieger/marotte",
			host: "github.com", slug: "cplieger/marotte",
		},
		{
			name: "HTTPS", raw: "https://github.com/cplieger/marotte.git",
			host: "github.com", slug: "cplieger/marotte",
		},
		{
			name: "HTTPSNoSuffix", raw: "https://github.com/cplieger/marotte",
			host: "github.com", slug: "cplieger/marotte",
		},
		{
			name: "SSHURL", raw: "ssh://git@gitlab.com/group/project.git",
			host: "gitlab.com", slug: "group/project",
		},
		{
			name: "GitLabSubgroupKeptWhole", raw: "https://gitlab.com/group/sub/deeper/project.git",
			host: "gitlab.com", slug: "group/sub/deeper/project",
		},
		{
			name: "TrailingSlash", raw: "https://github.com/cplieger/marotte/",
			host: "github.com", slug: "cplieger/marotte",
		},
		{
			name: "LeadingAndTrailingSpace", raw: "  git@github.com:a/b.git\n",
			host: "github.com", slug: "a/b",
		},
		{
			name: "SelfHostedGitea", raw: "https://git.example.test/team/thing.git",
			host: "git.example.test", slug: "team/thing",
		},
		{
			name: "PortIsNotPartOfTheHost", raw: "https://git.example.test:8443/team/thing.git",
			host: "git.example.test", slug: "team/thing",
		},

		{name: "Empty", raw: "", host: "", slug: ""},
		{name: "LocalPath", raw: "/srv/git/thing.git", host: "", slug: ""},
		{name: "RelativePath", raw: "../sibling", host: "", slug: ""},
		{name: "SingleSegment", raw: "https://github.com/onlyowner", host: "", slug: ""},
		{name: "SingleSegmentSCP", raw: "git@github.com:onlyowner.git", host: "", slug: ""},
		{name: "DotDotSegment", raw: "https://github.com/a/../b", host: "", slug: ""},
		{name: "RemoteHelper", raw: "ext::sh -c whoami", host: "", slug: ""},
		{name: "ControlCharInHost", raw: "https://git\x01hub.com/a/b", host: "", slug: ""},

		// url.Parse DECODES the path, so percent-encoded bytes reach cleanSlug as the real byte.
		{name: "EncodedNULInPath", raw: "https://github.com/a/b%00c", host: "", slug: ""},
		{name: "EncodedBELInPath", raw: "https://github.com/a/%07b", host: "", slug: ""},
		{name: "EncodedEscapeSequence", raw: "https://github.com/a/%1b]0;pwned%07b", host: "", slug: ""},
		{name: "EncodedDELInPath", raw: "https://github.com/a/b%7f", host: "", slug: ""},
		{name: "EncodedSpaceInPath", raw: "https://github.com/a/b%20c", host: "", slug: ""},
		{name: "EncodedBackslashInPath", raw: "https://github.com/a%5cb/c", host: "", slug: ""},
		{name: "LiteralBackslashSCP", raw: "git@github.com:a/b\\c.git", host: "", slug: ""},
		{name: "LiteralNULSCP", raw: "git@github.com:a/b\x00c.git", host: "", slug: ""},
		{
			name: "QueryIsNotPartOfThePath", raw: "https://github.com/a/b?x=1",
			host: "github.com", slug: "a/b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, slug := ParseRemoteSlug(tc.raw)
			if host != tc.host || slug != tc.slug {
				t.Errorf("ParseRemoteSlug(%q) = (%q, %q), want (%q, %q)",
					tc.raw, host, slug, tc.host, tc.slug)
			}
		})
	}
}

// TestRemoteWebBase is what a connection's web base is compared with, so an ssh
// remote must answer nothing and an http one must keep its port: a plaintext
// loopback instance is addressed by both.
func TestRemoteWebBase(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "HTTPS", raw: "https://github.com/cplieger/marotte.git", want: "https://github.com"},
		{name: "SchemeAndHostLowerCased", raw: "HTTPS://GitHub.COM/a/b", want: "https://github.com"},
		{name: "PortKept", raw: "http://127.0.0.1:3000/alice/app.git", want: "http://127.0.0.1:3000"},
		{name: "UserinfoDropped", raw: "https://bob:secret@gitlab.com/group/sub/project.git", want: "https://gitlab.com"},
		{name: "SpaceTrimmed", raw: "  https://git.example.test/team/thing.git\n", want: "https://git.example.test"},
		{name: "SCPStyle", raw: "git@github.com:cplieger/marotte.git", want: ""},
		{name: "SSHURL", raw: "ssh://git@gitlab.com/group/project.git", want: ""},
		{name: "GitProtocol", raw: "git://git.example.test/team/thing.git", want: ""},
		{name: "LocalPath", raw: "/srv/git/thing.git", want: ""},
		{name: "RemoteHelper", raw: "ext::sh -c whoami", want: ""},
		{name: "ControlCharInHost", raw: "https://git\x01hub.com/a/b", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteWebBase(tc.raw); got != tc.want {
				t.Errorf("remoteWebBase(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// FuzzParseRemoteSlug asserts that whatever a remote URL contains, an accepted slug is a plain multi-segment
// path with no traversal or forbidden byte.
func FuzzParseRemoteSlug(f *testing.F) {
	for _, seed := range []string{
		"git@github.com:cplieger/marotte.git",
		"https://gitlab.com/group/sub/project.git",
		"ssh://git@host/a/b",
		"ext::sh -c whoami",
		"https://github.com/a/../b",
		"/srv/git/thing.git",
		"",
		"https://github.com/a/b%00c",
		"https://github.com/a/%07b",
		"https://github.com/a/%1b]0;pwned%07b",
		"https://github.com/a/b%7f",
		"https://github.com/a%5cb/c",
		"https://github.com/a/b%20c",
		"git@github.com:a/b\\c.git",
		"git@github.com:a/b\x00c.git",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		host, slug := ParseRemoteSlug(raw)
		if host == "" || slug == "" {
			if host != "" || slug != "" {
				t.Fatalf("ParseRemoteSlug(%q) returned a half answer (%q, %q)", raw, host, slug)
			}
			return
		}
		if slug[0] == '/' || slug[len(slug)-1] == '/' {
			t.Fatalf("slug %q from %q has a boundary slash", slug, raw)
		}
		segs := 0
		for _, seg := range splitSlug(slug) {
			segs++
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("slug %q from %q holds segment %q", slug, raw, seg)
			}
		}
		if segs < 2 {
			t.Fatalf("slug %q from %q is not a repository address", slug, raw)
		}
		for _, r := range slug {
			switch {
			case r < 0x20:
				t.Fatalf("slug %q from %q holds C0 control %#U", slug, raw, r)
			case r == 0x20:
				t.Fatalf("slug %q from %q holds a space", slug, raw)
			case r == 0x7F:
				t.Fatalf("slug %q from %q holds DEL", slug, raw)
			case r == '\\':
				t.Fatalf("slug %q from %q holds a backslash", slug, raw)
			case r == '?' || r == '#':
				t.Fatalf("slug %q from %q holds URL delimiter %q", slug, raw, r)
			}
		}
	})
}

// splitSlug is the fuzz target's own splitter, kept local so the invariant is
// asserted against the output rather than re-derived with the production helper.
func splitSlug(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
