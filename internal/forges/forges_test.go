package forges

import (
	"strings"
	"testing"

	"github.com/cplieger/keyenc"
)

// TestKindHelpers locks in the Kind metadata.
func TestKindHelpers(t *testing.T) {
	cases := []struct {
		kind  Kind
		host  string
		valid bool
	}{
		{KindGitHub, "github.com", true},
		{KindGitLab, "gitlab.com", true},
		{KindCodeberg, "codeberg.org", true},
		{KindGitea, "", true},
		{Kind("nope"), "", false},
	}
	for _, c := range cases {
		if got := c.kind.valid(); got != c.valid {
			t.Errorf("%s.Valid() = %v", c.kind, got)
		}
		if got := c.kind.defaultHost(); got != c.host {
			t.Errorf("%s.DefaultHost() = %q", c.kind, got)
		}
	}
}

// parseIDSeeds are the ids parseID reads back and the ones it refuses, shared
// with FuzzParseID as its seeds.
var parseIDSeeds = struct {
	parse  map[string][2]string
	refuse map[string]string
}{
	parse: map[string][2]string{
		"a public instance":  {"github:github.com", "github.com"},
		"a host with a port": {keyenc.Join("gitea", "git.example.com:3000"), "git.example.com:3000"},
		"a bracketed IPv6":   {keyenc.Join("gitea", "[::1]:3000"), "[::1]:3000"},
	},
	refuse: map[string]string{
		"an unknown kind":     "bitbucket:bitbucket.org",
		"one field":           "github",
		"three fields":        keyenc.Join("gitea", "git.example.com", "3000"),
		"an empty host":       keyenc.Join("gitea", ""),
		"a host with a slash": keyenc.Join("gitea", "x.test/p"),
		"a backslash":         keyenc.Join("gitea", `x\y.test`),
		"a 260-byte host":     keyenc.Join("gitea", strings.Repeat("a", 254)+":65535"),
		"a hashed id":         keyenc.Join("gitea", strings.Repeat("a", keyenc.MaxComponentBytes+1)),
		"an unescaped colon":  "gitea:git.example.com:3000",
	},
}

// TestParseID_ReadsBackOnlyWhatMakeIDMints pins the seeds by name: a parsed id
// is one MakeID mints from its parts, and every other spelling is refused.
func TestParseID_ReadsBackOnlyWhatMakeIDMints(t *testing.T) {
	for name, tc := range parseIDSeeds.parse {
		t.Run(name, func(t *testing.T) {
			kind, host, ok := parseID(tc[0])
			if !ok || host != tc[1] || MakeID(kind, host) != tc[0] {
				t.Errorf("parseID(%q) = %q, %q, %v; want the host %q", tc[0], kind, host, ok, tc[1])
			}
		})
	}
	for name, id := range parseIDSeeds.refuse {
		t.Run(name, func(t *testing.T) {
			if kind, host, ok := parseID(id); ok {
				t.Errorf("parseID(%q) = %q, %q, true; want it refused", id, kind, host)
			}
		})
	}
}

// TestMakeID_HostDefaulting verifies MakeID fills in the kind's default host for
// an empty host, and that an id with no port keeps today's bytes.
func TestMakeID_HostDefaulting(t *testing.T) {
	if got := MakeID(KindGitHub, ""); got != "github:github.com" {
		t.Errorf("MakeID(KindGitHub, \"\") = %q, want %q", got, "github:github.com")
	}
	if got := MakeID(KindGitHub, "self.example"); got != "github:self.example" {
		t.Errorf("MakeID(KindGitHub, %q) = %q, want %q", "self.example", got, "github:self.example")
	}
	if got, want := MakeID(KindGitea, "git.test:3000"), `gitea:git.test\:3000`; got != want {
		t.Errorf("MakeID(KindGitea, %q) = %q, want the port's colon escaped %q", "git.test:3000", got, want)
	}
}

// stubPath points PATH at an empty directory, so no forge CLI is reachable from
// the test. t.Setenv also blocks t.Parallel, which a test changing the
// process-wide PATH must not use anyway.
func stubPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}
