package steering

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// fakeDirEntry is a listing entry the test fully controls, including names no filesystem
// would accept.
type fakeDirEntry struct {
	name  string
	isDir bool
}

func (e fakeDirEntry) Name() string { return e.name }
func (e fakeDirEntry) IsDir() bool  { return e.isDir }
func (e fakeDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{e}, nil }

type fakeFileInfo struct{ e fakeDirEntry }

func (i fakeFileInfo) Name() string      { return i.e.name }
func (fakeFileInfo) Size() int64         { return 0 }
func (i fakeFileInfo) Mode() fs.FileMode { return i.e.Type() }
func (fakeFileInfo) ModTime() time.Time  { return time.Time{} }
func (i fakeFileInfo) IsDir() bool       { return i.e.isDir }
func (fakeFileInfo) Sys() any            { return nil }

func entries(names ...string) []fs.DirEntry {
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, fakeDirEntry{name: n})
	}
	return out
}

// agentDoorFixture is the listing every door test uses (its twin is in
// internal/server/kiro_agent_doors_test.go). Sorted, as fs.ReadDir and os.ReadDir sort.
var agentDoorFixture = []string{
	".hidden.md",
	".md",
	"README.txt",
	"deploy.json",
	"notes.md",
	"reviewer.json",
	"reviewer.md",
}

// TestDedupeAgentFiles is the ONE statement of what an agents/ listing means.
func TestDedupeAgentFiles(t *testing.T) {
	tests := map[string]struct {
		in   []fs.DirEntry
		want []AgentFile
		why  string
	}{
		"a pair collapses to the markdown": {
			in:   entries("reviewer.json", "reviewer.md"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "the .md is what carries the front-matter, so it is the spelling that can describe itself",
		},
		"the markdown wins whichever order it is listed in": {
			in:   entries("reviewer.md", "reviewer.json"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "directory order is not a preference; a listing that happens to name the .md first must resolve the same",
		},
		"a json-only agent is listed under its json": {
			in:   entries("deploy.json"),
			want: []AgentFile{{Base: "deploy", File: "deploy.json"}},
			why:  "dropping it would hide an agent that exists",
		},
		"a markdown-only agent is listed": {
			in:   entries("notes.md"),
			want: []AgentFile{{Base: "notes", File: "notes.md"}},
		},
		"a third spelling of the same base does not displace the markdown": {
			in:   entries("reviewer.md", "reviewer.json", "reviewer.md"),
			want: []AgentFile{{Base: "reviewer", File: "reviewer.md"}},
			why:  "the only upgrade is json -> md, so nothing can overwrite a chosen .md",
		},
		"a directory is not an agent": {
			in:   []fs.DirEntry{fakeDirEntry{name: "archive.md", isDir: true}},
			want: nil,
			why:  "a directory named like a doc would produce an entry no reader can open",
		},
		"a dot-prefixed file is not an agent": {
			in:   entries(".hidden.md", "._deploy.json"),
			want: nil,
			why:  "an AppleDouble or a hidden draft is not authored inventory, and its base name renders as junk",
		},
		"a bare extension is not an agent": {
			in:   entries(".md", ".json"),
			want: nil,
			why:  "its base name is empty; the dot rule already covers it, and an empty-named row is unopenable",
		},
		"an unrelated extension is not an agent": {
			in:   entries("README.txt", "config.yaml", "noext"),
			want: nil,
		},
		"a NUL in the name is refused": {
			in:   entries("deploy\x00.json"),
			want: nil,
			why:  "two of the three doors build a path the client is handed from this name",
		},
		"order is the listing's, not the map's": {
			in: entries("zeta.md", "alpha.json", "middle.md"),
			want: []AgentFile{
				{Base: "zeta", File: "zeta.md"},
				{Base: "alpha", File: "alpha.json"},
				{Base: "middle", File: "middle.md"},
			},
			why: "a cap applied at the call site takes a prefix, so the prefix has to be stable",
		},
		"the shared door fixture": {
			in: entries(agentDoorFixture...),
			want: []AgentFile{
				{Base: "deploy", File: "deploy.json"},
				{Base: "notes", File: "notes.md"},
				{Base: "reviewer", File: "reviewer.md"},
			},
			why: "every door test resolves this listing; this is the answer they are all checked against",
		},
		"an empty listing yields nothing": {
			in:   nil,
			want: nil,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := DedupeAgentFiles(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("DedupeAgentFiles = %+v (len %d), want %+v (len %d) — %s",
					got, len(got), tc.want, len(tc.want), tc.why)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %+v, want %+v — %s", i, got[i], tc.want[i], tc.why)
				}
			}
		})
	}
}

// TestFindRepoAgents_AgreesWithTheRule asserts the generator's door against DedupeAgentFiles.
func TestFindRepoAgents_AgreesWithTheRule(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".kiro", "agents")
	for _, name := range agentDoorFixture {
		if name == ".md" {
			// A file named ".md" is legal on disk and the rule refuses it.
			mustWriteFile(t, filepath.Join(dir, name), "")
			continue
		}
		mustWriteFile(t, filepath.Join(dir, name), "# "+name+"\n")
	}

	want := DedupeAgentFiles(entries(agentDoorFixture...))
	got := findRepoAgents(repo)

	if len(got) != len(want) {
		t.Fatalf("findRepoAgents = %+v (len %d), want the rule's %+v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Base {
			t.Errorf("findRepoAgents[%d].Name = %q, the rule says %q", i, got[i].Name, want[i].Base)
		}
	}
}
