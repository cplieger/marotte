package steering

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRepoRemote_MatchesKASTags(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		{"git@github.com:cplieger/marotte.git", "github/cplieger/marotte"},
		{"https://github.com/cplieger/marotte", "github/cplieger/marotte"},
		{"ssh://git@github.com:22/cplieger/marotte.git", "github/cplieger/marotte"},
		{"https://gitlab.com/group/sub/proj.git", "gitlab/group/sub/proj"},
		{"https://bitbucket.org/team/repo/extra", "bitbucket/team/repo"},
		{"https://dev.azure.com/org/project/_git/repo", "azure/org/project/repo"},
		{"ssh://git.amazon.com/pkg/MyPackage", "gitfarm/gitfarm/MyPackage"},
		{"https://git.example.org:8443/owner/name.git", "git.example.org/owner/name"},
		{"https://git.example.org/solo", "git.example.org/git.example.org/solo"},
		{"/srv/repos/local.git", ""},
		{"https://user:secret@git.example.org/o/r.git", "user:secret@git.example.org/o/r"},
		{"", ""},
	}
	for _, tc := range cases {
		provider, owner, name, ok := parseRepoRemote(tc.url)
		got := ""
		if ok {
			got = provider + "/" + owner + "/" + name
		}
		if got != tc.want {
			t.Errorf("parseRepoRemote(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// writeRepo makes a repo whose origin is url; linked makes `.git` a worktree
// pointer file whose common dir holds the config, the way KAS follows it.
func writeRepo(t *testing.T, workDir, name, url string, linked bool) {
	t.Helper()
	repo := filepath.Join(workDir, name)
	gitDir := filepath.Join(repo, ".git")
	if linked {
		common := filepath.Join(t.TempDir(), "main.git")
		wt := filepath.Join(common, "worktrees", name)
		mustWrite(t, filepath.Join(wt, "commondir"), "../..\n")
		mustWrite(t, gitDir, "gitdir: "+wt+"\n")
		gitDir = common
	}
	mustWrite(t, filepath.Join(gitDir, "config"), "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = "+url+"\n")
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A credential in a generic host's userinfo, or markup, never reaches the doc.
func TestMemoryRepoTag_RefusesAnUnsafeTag(t *testing.T) {
	workDir := t.TempDir()
	writeRepo(t, workDir, "leak", "https://user:secret@git.example.org/o/r.git", false)
	writeRepo(t, workDir, "ok", "git@github.com:o/r.git", false)
	if got := memoryRepoTag(filepath.Join(workDir, "leak")); got != "" {
		t.Errorf("memoryRepoTag(userinfo remote) = %q, want refused", got)
	}
	if got := memoryRepoTag(filepath.Join(workDir, "ok")); got != "repo:github/o/r" {
		t.Errorf("memoryRepoTag(github remote) = %q, want repo:github/o/r", got)
	}
}

func TestWriteMemory_NamesEachRepoTagUnlessOff(t *testing.T) {
	workDir := t.TempDir()
	writeRepo(t, workDir, "marotte", "git@github.com:cplieger/marotte.git", false)
	writeRepo(t, workDir, "wt", "https://gitlab.com/g/wt.git", true)

	cfg := t.TempDir()
	var b strings.Builder
	writeMemory(t.Context(), &b, workDir, cfg)
	out := b.String()
	for _, want := range []string{
		"## Memory",
		"`memory list` with `filter` set to its tag",
		"`memory add` with a `path` inside that repository",
		"- `marotte/`: `repo:github/cplieger/marotte`",
		"- `wt/`: `repo:gitlab/g/wt`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Memory section is missing %q:\n%s", want, out)
		}
	}

	mustWrite(t, filepath.Join(cfg, "config.json"), `{"memory_mode":"off"}`)
	b.Reset()
	writeMemory(t.Context(), &b, workDir, cfg)
	if b.Len() != 0 {
		t.Errorf("Memory set to Off still wrote a section:\n%s", b.String())
	}
}

func TestRender_IncludesTheMemorySection(t *testing.T) {
	workDir := t.TempDir()
	writeRepo(t, workDir, "marotte", "git@github.com:cplieger/marotte.git", false)
	out := string(New(workDir, t.TempDir()).render(t.Context(), MCPSnapshot{}, false, ForgeSnapshot{}, false))
	if !strings.Contains(out, "## Memory\n") || !strings.Contains(out, "`repo:github/cplieger/marotte`") {
		t.Errorf("environment.md lacks the Memory section under the default mode")
	}
}
