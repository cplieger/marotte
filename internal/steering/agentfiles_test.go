package steering

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeDirEntry is a listing entry the test fully controls, including names no filesystem
// would accept.
type fakeDirEntry struct {
	name  string
	isDir bool
	typ   fs.FileMode
}

func (e fakeDirEntry) Name() string { return e.name }
func (e fakeDirEntry) IsDir() bool  { return e.isDir }
func (e fakeDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return e.typ
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

// linkTargets is the stat DedupeAgentFiles follows links with: linked.md resolves to a file,
// pair.md to a fifo, todir.md to a directory, and any other name dangles.
func linkTargets(name string) (fs.FileInfo, error) {
	switch name {
	case "linked.md":
		return fakeFileInfo{fakeDirEntry{name: name}}, nil
	case "pair.md":
		return fakeFileInfo{fakeDirEntry{name: name, typ: fs.ModeNamedPipe}}, nil
	case "todir.md":
		return fakeFileInfo{fakeDirEntry{name: name, isDir: true}}, nil
	default:
		return nil, fs.ErrNotExist
	}
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
		"a fifo, socket or device is not an agent": {
			in: []fs.DirEntry{
				fakeDirEntry{name: "pair.md", typ: fs.ModeNamedPipe},
				fakeDirEntry{name: "pair.json"},
				fakeDirEntry{name: "sock.md", typ: fs.ModeSocket},
				fakeDirEntry{name: "dev.json", typ: fs.ModeDevice | fs.ModeCharDevice},
			},
			want: []AgentFile{{Base: "pair", File: "pair.json"}},
			why:  "KAS reads an agent from a file, so a fifo .md must not shadow the readable .json",
		},
		"a link is an agent only when its target is a regular file": {
			in: []fs.DirEntry{
				fakeDirEntry{name: "pair.md", typ: fs.ModeSymlink},
				fakeDirEntry{name: "pair.json"},
				fakeDirEntry{name: "dangling.md", typ: fs.ModeSymlink},
				fakeDirEntry{name: "todir.md", typ: fs.ModeSymlink},
				fakeDirEntry{name: "linked.md", typ: fs.ModeSymlink},
			},
			want: []AgentFile{{Base: "pair", File: "pair.json"}, {Base: "linked", File: "linked.md"}},
			why:  "a link KAS cannot read as a file must not shadow the readable .json or stand as an agent",
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
			got := DedupeAgentFiles(tc.in, linkTargets)
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

	want := DedupeAgentFiles(entries(agentDoorFixture...), linkTargets)
	got, _ := findRepoAgents(repo)

	if len(got) != len(want) {
		t.Fatalf("findRepoAgents = %+v (len %d), want the rule's %+v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Base {
			t.Errorf("findRepoAgents[%d].Name = %q, the rule says %q", i, got[i].Name, want[i].Base)
		}
	}
}

// TestFindRepoAgents_LinkCountsOnlyWhenItsTargetIsARegularFile pins that environment.md's agent
// list and the presence check read an agents/ link as KAS does: an agent only when it resolves to
// a file.
func TestFindRepoAgents_LinkCountsOnlyWhenItsTargetIsARegularFile(t *testing.T) {
	cases := []struct {
		plant     func(t *testing.T, kiro string) string
		name      string
		wantAgent bool
	}{
		{name: "dangling", plant: func(t *testing.T, kiro string) string {
			return filepath.Join(kiro, "shared", "missing.md")
		}},
		{name: "to_a_directory", plant: func(t *testing.T, kiro string) string {
			dir := filepath.Join(kiro, "shared", "dir")
			mustWriteFile(t, filepath.Join(dir, "keep.txt"), "x\n")
			return dir
		}},
		{name: "to_a_fifo", plant: func(t *testing.T, kiro string) string {
			p := filepath.Join(kiro, "shared", "pipe")
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skipf("mkfifo unavailable: %v", err)
			}
			return p
		}},
		{name: "to_a_file", wantAgent: true, plant: func(t *testing.T, kiro string) string {
			p := filepath.Join(kiro, "shared", "real.md")
			mustWriteFile(t, p, "# real\n")
			return p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			kiro := filepath.Join(repo, ".kiro")
			if err := os.MkdirAll(filepath.Join(kiro, "agents"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.plant(t, kiro), filepath.Join(kiro, "agents", "linked.md")); err != nil {
				t.Skipf("symlink unsupported here: %v", err)
			}
			want := 0
			if tc.wantAgent {
				want = 1
			}
			if agents, total := findRepoAgents(repo); total != want {
				t.Errorf("findRepoAgents = %+v (total %d), want %d agents", agents, total, want)
			}
			if got := hasKiroContent(repo); got != tc.wantAgent {
				t.Errorf("hasKiroContent = %v, want %v", got, tc.wantAgent)
			}
		})
	}
}
