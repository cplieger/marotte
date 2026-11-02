package forges

import (
	"slices"
	"testing"

	"github.com/cplieger/keyenc"
)

// FuzzMakeID asserts MakeID answers an id for any input and, for a valid kind
// and host, one keyenc.Split reads back into exactly that kind and host, the
// kind's default standing in for an empty host.
func FuzzMakeID(f *testing.F) {
	f.Add("github", "github.com")
	f.Add("gitlab", "")
	f.Add("gitea", "codeberg.org")
	f.Add("gitea", "git.example.com:3000")
	f.Add("", "")
	f.Add("unknown", "host.example.com")

	f.Fuzz(func(t *testing.T, kind, host string) {
		id := MakeID(Kind(kind), host)
		if id == "" {
			t.Fatal("MakeID returned empty string")
		}
		want := host
		if want == "" {
			want = Kind(kind).defaultHost()
		}
		if !Kind(kind).valid() || !validHost(want) {
			return
		}
		parts, err := keyenc.Split(id)
		if err != nil || !slices.Equal(parts, []string{kind, want}) {
			t.Fatalf("keyenc.Split(MakeID(%q, %q)) = %q, %v; want [%q %q]", kind, host, parts, err, kind, want)
		}
	})
}

// FuzzParseID asserts every id parseID accepts from untrusted input is one
// MakeID mints from its parts, on a host the sign-in boundary admits, and never
// a hashed id.
func FuzzParseID(f *testing.F) {
	for _, tc := range parseIDSeeds.parse {
		f.Add(tc[0])
	}
	for _, id := range parseIDSeeds.refuse {
		f.Add(id)
	}

	f.Fuzz(func(t *testing.T, id string) {
		kind, host, ok := parseID(id)
		if !ok {
			return
		}
		if MakeID(kind, host) != id || !validHost(host) || keyenc.IsHashed(id) {
			t.Fatalf("parseID(%q) = %q, %q: not an id MakeID mints on an admitted host", id, kind, host)
		}
	})
}

// FuzzSplitFirst verifies the head/tail split round-trips: when a
// separator is found, head+"/"+tail reconstructs the input; otherwise
// head is the whole input and tail is empty.
func FuzzSplitFirst(f *testing.F) {
	f.Add("api/forges/github:github.com")
	f.Add("noslash")
	f.Add("")
	f.Add("/leading")
	f.Add("a/b/c")

	f.Fuzz(func(t *testing.T, s string) {
		head, tail, found := splitFirst(s)
		if found {
			reconstructed := head + "/" + tail
			if reconstructed != s {
				t.Fatalf("splitFirst(%q): head=%q tail=%q doesn't reconstruct", s, head, tail)
			}
		} else {
			if head != s {
				t.Fatalf("splitFirst(%q): no slash but head=%q != input", s, head)
			}
			if tail != "" {
				t.Fatalf("splitFirst(%q): no slash but tail=%q", s, tail)
			}
		}
	})
}
