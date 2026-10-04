package forges

import "testing"

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
		if got := c.kind.Valid(); got != c.valid {
			t.Errorf("%s.Valid() = %v", c.kind, got)
		}
		if got := c.kind.DefaultHost(); got != c.host {
			t.Errorf("%s.DefaultHost() = %q", c.kind, got)
		}
	}
}

// TestSplitID locks in the ID parser used by HTTP routing.
func TestSplitID(t *testing.T) {
	cases := []struct {
		id   string
		kind Kind
		host string
	}{
		{"github:github.com", KindGitHub, "github.com"},
		{"gitlab:self.example", KindGitLab, "self.example"},
		{"codeberg:codeberg.org", KindCodeberg, "codeberg.org"},
		{"malformed", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		k, h := splitID(c.id)
		if k != c.kind || h != c.host {
			t.Errorf("splitID(%q) = %q, %q; want %q, %q", c.id, k, h, c.kind, c.host)
		}
	}
}

// TestMakeID_HostDefaulting verifies MakeID fills in the kind's default
// host for an empty host and uses an explicit host verbatim.
func TestMakeID_HostDefaulting(t *testing.T) {
	if got := MakeID(KindGitHub, ""); got != "github:github.com" {
		t.Errorf("MakeID(KindGitHub, \"\") = %q, want %q", got, "github:github.com")
	}
	if got := MakeID(KindGitHub, "self.example"); got != "github:self.example" {
		t.Errorf("MakeID(KindGitHub, %q) = %q, want %q", "self.example", got, "github:self.example")
	}
}

// stubPath points PATH at an empty directory, so no forge CLI is reachable from
// the test. t.Setenv also blocks t.Parallel, which a test changing the
// process-wide PATH must not use anyway.
func stubPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}
