package steering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// renderFixture is one user's setup, sized by the drivers environment.md grows with.
type renderFixture struct {
	tools      ToolsSnapshot
	mcp        int
	forgeRepos int
	plainRepos int
	kiroRepos  []kiroRepoFixture
	dirs       int
	granted    int
	memory     bool
	readmes    bool // plain repos get a README paragraph and a branch to prove neither is rendered
}

type kiroRepoFixture struct {
	name     string
	steering []string // front matter bodies, one per doc
	skills   []string
	hooks    int
}

const (
	readmeSentinel = "READMESENTINELPARAGRAPH"
	branchSentinel = "sentinel-branch"
)

// build lays the fixture out on disk and renders environment.md for it.
func (f *renderFixture) build(t *testing.T) (out, workDir string) {
	t.Helper()
	workDir, configDir := t.TempDir(), t.TempDir()
	for i := range f.plainRepos {
		name := fmt.Sprintf("repo%03d", i)
		writeRepo(t, workDir, name, "https://git.example.org/owner/"+name+".git", false)
		if f.readmes {
			mustWrite(t, filepath.Join(workDir, name, "README.md"), "# Title\n\n"+readmeSentinel+" describes it.\n")
			mustWrite(t, filepath.Join(workDir, name, ".git", "HEAD"), "ref: refs/heads/"+branchSentinel+"\n")
		}
	}
	for _, k := range f.kiroRepos {
		writeRepo(t, workDir, k.name, "https://git.example.org/owner/"+k.name+".git", false)
		for i, fm := range k.steering {
			mustWrite(t, filepath.Join(workDir, k.name, ".kiro", "steering", fmt.Sprintf("doc%02d.md", i)), "---\n"+fm+"\n---\n\n# D\n")
		}
		for i, fm := range k.skills {
			mustWrite(t, filepath.Join(workDir, k.name, ".kiro", "skills", fmt.Sprintf("skill%02d", i), "SKILL.md"), "---\n"+fm+"\n---\n\n# S\n")
		}
		for i := range k.hooks {
			mustWrite(t, filepath.Join(workDir, k.name, ".kiro", "hooks", fmt.Sprintf("h%02d.json", i)),
				`{"version":"v1","hooks":[{"name":"n","trigger":"PostFileSave","action":{"type":"command","command":"make check"}}]}`)
		}
	}
	for i := range f.dirs {
		if err := os.MkdirAll(filepath.Join(workDir, fmt.Sprintf("dir%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := map[string]any{}
	if f.memory {
		cfg[settings.KeyMemoryMode] = "learn"
	}
	if f.granted > 0 {
		dirs := make([]string, f.granted)
		for i := range dirs {
			dirs[i] = grantableDir(t)
		}
		cfg[settings.KeyAdditionalDirectories] = dirs
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(configDir, "config.json"), string(data))

	var mcp MCPSnapshot
	for i := range f.mcp {
		mcp.Servers = append(mcp.Servers, marotte.MCPSnapshotServer{Name: fmt.Sprintf("server%02d", i)})
	}
	forges := ForgeSnapshot{Providers: []ForgeProvider{{Kind: "github", Host: "github.com", User: "someone"}}}
	for i := range f.forgeRepos {
		forges.Providers[0].Repos = append(forges.Providers[0].Repos, fmt.Sprintf("someone/project%03d", i))
	}
	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	return string(g.render(t.Context(), mcp, f.mcp > 0, forges, true, f.tools, true)), workDir
}

// sectionOf returns the body of the heading line through the next heading of the same level or above.
func sectionOf(out, heading string) string {
	_, rest, ok := strings.Cut(out, heading+"\n")
	if !ok {
		return ""
	}
	level := strings.Repeat("#", strings.Index(heading, " "))
	for i := 0; i < len(rest); {
		j := strings.Index(rest[i:], "\n#")
		if j < 0 {
			return rest
		}
		line := rest[i+j+1:]
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		if hashes <= len(level) && strings.HasPrefix(line[hashes:], " ") {
			return rest[:i+j+1]
		}
		i += j + 2
	}
	return rest
}

// lineWithPrefix returns the first line of s starting with prefix.
func lineWithPrefix(s, prefix string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

var (
	codeSpan     = regexp.MustCompile("`[^`]+`")
	routingEntry = regexp.MustCompile("(?m)^    - `[^`]+/\\.kiro/(steering|skills)/[^`]+`")
)

func engineRows(n int, prefix string) []InstalledTool {
	rows := make([]InstalledTool, n)
	for i := range rows {
		name := fmt.Sprintf("%s%03d", prefix, i)
		rows[i] = InstalledTool{Version: fmt.Sprintf("1.0.%d", i), Commands: []string{name}}
	}
	return rows
}

func libraryRows(n int) []InstalledTool {
	rows := make([]InstalledTool, n)
	for i := range rows {
		rows[i] = InstalledTool{Version: "1.0"}
	}
	return rows
}

func words(n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = fmt.Sprintf("w%02d", i+1)
	}
	return strings.Join(w, " ")
}

const baseSystem = "bash curl git jq ssh tar unzip xz"

const debianSlimLine = "The image is Debian slim: anything not listed is likely absent (common utilities such as " +
	"file, bc, xxd, netstat and ss are not installed). Check with `command -v`, and install through the tools engine below."

func smallFixture() *renderFixture {
	return &renderFixture{
		tools:      ToolsSnapshot{System: strings.Fields(baseSystem)},
		forgeRepos: 12,
		plainRepos: 3,
		readmes:    true,
	}
}

func typicalFixture() *renderFixture {
	tools := engineRows(15, "tool")
	tools = append(tools,
		InstalledTool{Version: "7:7.1", Commands: []string{"ffmpeg", "ffprobe"}},
		InstalledTool{Version: "2.0", Commands: []string{"imgconv"}})
	tools = append(tools, libraryRows(6)...)
	return &renderFixture{
		tools:      ToolsSnapshot{Tools: tools, System: strings.Fields(baseSystem)},
		mcp:        3,
		forgeRepos: 30,
		plainRepos: 10,
		kiroRepos: []kiroRepoFixture{
			{name: "kiroone", steering: []string{"inclusion: always\ndescription: The house rules.",
				"inclusion: fileMatch\nfileMatchPattern: \"src/**\"\ndescription: Source rules."}},
			{name: "kirotwo", skills: []string{"description: " + words(60)}},
		},
	}
}

// largeFixture sits over every cap. It carries twelve .kiro repos, more than the per-workspace
// limit on repos with their own routing block, so doubling it adds no line (see the bounded case).
func largeFixture(scale int) *renderFixture {
	tools := engineRows(200*scale, "cmd")
	tools = append(tools, libraryRows(400*scale)...)
	var kiro []kiroRepoFixture
	for i := range 12 * scale {
		kiro = append(kiro, kiroRepoFixture{
			name: fmt.Sprintf("kiro%02d", i),
			steering: []string{
				"inclusion: always\ndescription: Always doc.", "inclusion: manual\ndescription: Manual doc.",
				"inclusion: fileMatch\nfileMatchPattern: \"*.go\"\ndescription: Go doc.",
			},
			skills: []string{"description: First skill.", "description: Second skill."},
			hooks:  1,
		})
	}
	return &renderFixture{
		tools:      ToolsSnapshot{Tools: tools, System: strings.Fields(baseSystem)},
		mcp:        40 * scale,
		forgeRepos: 500 * scale,
		plainRepos: 100 * scale,
		kiroRepos:  kiro,
		dirs:       40 * scale,
		granted:    30 * scale,
		memory:     true,
	}
}

const wantProtocol = "### Per-repo .kiro protocol\n\n" +
	"kiro-cli loads `.kiro/` only from `@WORK@`, so the repos' `.kiro/` content listed above is not loaded. Use the list as a routing table:\n\n" +
	"- When you start working in a repo (a prompt names it, or you read, edit or cd into it), read its \"Always-loaded steering\" files.\n" +
	"- Before opening or editing a file that matches a \"File-match\" pattern, read that doc or skill.\n" +
	"- Read a \"Manual\" steering doc or a skill when the task or the user calls for it.\n" +
	"- Custom agents are listed for awareness: this session cannot load or switch to them.\n" +
	"- Hooks do not run for nested repos: when you perform a hook's trigger action, run its command yourself (for an agent hook, act on its prompt).\n" +
	"- One read per session is enough. The list is a snapshot from session start: read a doc added or edited since then directly with read_file.\n\n"

func TestRender_Fixtures(t *testing.T) {
	cases := []struct {
		fixture *renderFixture
		check   func(t *testing.T, out, workDir string)
		name    string
	}{{
		name:    "small",
		fixture: smallFixture(),
		check: func(t *testing.T, out, _ string) {
			tools := sectionOf(out, "## Installed tools")
			if !strings.Contains(tools, "Baked into the image: bash, curl, git, jq, ssh, tar, unzip, xz\n") {
				t.Errorf("Installed tools lacks the baked-in line:\n%s", tools)
			}
			if !strings.Contains(tools, debianSlimLine) {
				t.Errorf("Installed tools lacks the Debian slim line:\n%s", tools)
			}
			if strings.Contains(tools, "Commands:") {
				t.Errorf("Installed tools has a Commands line with no command installed:\n%s", tools)
			}
			if strings.Contains(out, "Per-repo .kiro protocol") {
				t.Errorf("a workspace with no .kiro content carries the protocol")
			}
			if got := lineWithPrefix(out, "- Other repositories: "); got != "- Other repositories: `repo000/`, `repo001/`, `repo002/`" {
				t.Errorf("Other repositories line = %q", got)
			}
			for _, banned := range []string{readmeSentinel, branchSentinel} {
				if strings.Contains(out, banned) {
					t.Errorf("environment.md carries %q, which one shell call answers", banned)
				}
			}
			if strings.Contains(sectionOf(out, "## Connected forges"), "… and") {
				t.Errorf("12 forge repos, under the cap, got an overflow line")
			}
		},
	}, {
		name:    "typical",
		fixture: typicalFixture(),
		check: func(t *testing.T, out, workDir string) {
			tools := sectionOf(out, "## Installed tools")
			commands := lineWithPrefix(tools, "Commands: ")
			for _, want := range []string{"ffmpeg 7:7.1", "ffprobe 7:7.1", "imgconv 2.0", "tool000 1.0.0", "tool014 1.0.14"} {
				if !strings.Contains(commands, want) {
					t.Errorf("Commands line lacks %q: %q", want, commands)
				}
			}
			if got := len(strings.Split(strings.TrimPrefix(commands, "Commands: "), ", ")); got != 18 {
				t.Errorf("Commands line names %d commands, want 18 (15 engine, ffmpeg's 2, imagekit's 1): %q", got, commands)
			}
			if !strings.Contains(tools, "\n6 more installed packages ship no command (mostly libraries): "+
				"`dpkg -l` or `curl -s localhost:9847/api/tools` lists them.\n") {
				t.Errorf("Installed tools lacks the no-command count line:\n%s", tools)
			}
			if !strings.Contains(out, "  - **Skills** (`kirotwo/.kiro/skills/`; read a SKILL.md when its description matches the task):\n"+
				"    - `kirotwo/.kiro/skills/skill00/SKILL.md` — "+words(20)+" …\n") {
				t.Errorf("the skill is not under **Skills** with a 20-word description:\n%s", sectionOf(out, "### Git repositories"))
			}
			if strings.Contains(out, "Always-loaded skills") || strings.Contains(out, "Manual skills") {
				t.Errorf("a skill is labelled by an inclusion it does not have")
			}
			if got, want := sectionOf(out, "### Per-repo .kiro protocol"), strings.TrimPrefix(strings.ReplaceAll(wantProtocol, "@WORK@", workDir), "### Per-repo .kiro protocol\n"); got != want {
				t.Errorf("protocol =\n%s\nwant\n%s", got, want)
			}
			for _, r := range []string{"kiroone", "kirotwo"} {
				if !strings.Contains(out, "\n- `"+r+"/`\n") {
					t.Errorf("the .kiro repo %s has no line of its own", r)
				}
			}
			if got := len(codeSpan.FindAllString(lineWithPrefix(out, "- Other repositories: "), -1)); got != 10 {
				t.Errorf("Other repositories names %d repos, want 10", got)
			}
			if got := strings.Count(sectionOf(out, "## Connected integrations"), "\n- **server"); got != 3 {
				t.Errorf("Connected integrations names %d servers, want 3", got)
			}
		},
	}, {
		name:    "large",
		fixture: largeFixture(1),
		check: func(t *testing.T, out, workDir string) {
			commands := lineWithPrefix(out, "Commands: ")
			head, tail, ok := strings.Cut(strings.TrimPrefix(commands, "Commands: "), ", … and ")
			if !ok || len(strings.Split(head, ", ")) != 120 {
				t.Errorf("Commands line does not name exactly 120 then an overflow: %q", commands)
			}
			if !strings.HasPrefix(tail, "80 more (`ls ") || !strings.HasSuffix(tail, "/tools/bin`, `command -v <name>`)") {
				t.Errorf("Commands overflow = %q, want `80 more (`ls <toolsDir>/bin`, `command -v <name>`)`", tail)
			}
			if !strings.Contains(out, "\n400 more installed packages ship no command") {
				t.Errorf("the no-command count is not 400")
			}
			other := lineWithPrefix(out, "- Other repositories: ")
			if got := len(codeSpan.FindAllString(other, -1)); got != 61 || !strings.HasSuffix(other, ", … and 40 more (`ls "+workDir+"`)") {
				t.Errorf("Other repositories line does not name 60 then `… and 40 more`: %q", other)
			}
			if got := len(routingEntry.FindAllString(out, -1)); got != 40 {
				t.Errorf("routing lists %d steering and skill entries, want 40", got)
			}
			for i := 8; i < 10; i++ {
				r := fmt.Sprintf("kiro%02d", i)
				if !strings.Contains(out, "- `"+r+"/`\n  - … 5 more .kiro entries: list `"+r+"/.kiro/steering/` and `"+r+"/.kiro/skills/`\n") {
					t.Errorf("%s, past the entry cap, lacks its overflow line", r)
				}
			}
			if !strings.Contains(out, "- More repositories with .kiro content: `kiro10/`, `kiro11/`") {
				t.Errorf("the .kiro repos past the repo cap are not folded into one line:\n%s", sectionOf(out, "### Git repositories"))
			}
			mcp := sectionOf(out, "## Connected integrations")
			if got := strings.Count(mcp, "\n- **server"); got != 30 || !strings.Contains(mcp, "\n- … and 10 more (the load_resource catalog lists them)\n") {
				t.Errorf("Connected integrations names %d servers, want 30 then `… and 10 more`", got)
			}
			dirs := sectionOf(out, "### Directories")
			if got := strings.Count(dirs, "\n- `dir"); got != 30 || !strings.Contains(dirs, "\n- … and 10 more (`ls "+workDir+"`)\n") {
				t.Errorf("Directories lists %d, want 30 then `… and 10 more`:\n%s", got, dirs)
			}
			extra := sectionOf(out, "## Additional directories")
			if got := strings.Count(extra, "\n- `/"); got != 20 || !strings.Contains(extra, "\n- … and 10 more\n") {
				t.Errorf("Additional directories lists %d, want 20 then `… and 10 more`:\n%s", got, extra)
			}
			forge := sectionOf(out, "## Connected forges")
			if got := strings.Count(forge, "\n  - someone/"); got != 20 || !strings.Contains(forge, "\n  - … and 480 more\n") {
				t.Errorf("forge lists %d repos, want 20 then `… and 480 more`", got)
			}
			memory := sectionOf(out, "## Memory")
			if got := strings.Count(memory, "`: `repo:"); got != 30 ||
				!strings.Contains(memory, "\n- … and 82 more (each repository's tag is `repo:<host>/<owner>/<name>` from its origin)\n") {
				t.Errorf("Memory lists %d tags, want 30 then `… and 82 more`:\n%s", got, memory)
			}
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, workDir := tc.fixture.build(t)
			if !strings.Contains(out, "This file is auto-generated by marotte") || !strings.Contains(out, "edits are overwritten") {
				t.Errorf("the auto-generated header is gone")
			}
			if !strings.Contains(sectionOf(out, "## Installed tools"), debianSlimLine) {
				t.Errorf("Installed tools lacks the Debian slim line")
			}
			tc.check(t, out, workDir)
		})
	}
}

// Past every cap, a larger setup changes only the overflow counts.
func TestRender_LargeSetupIsBounded(t *testing.T) {
	large, _ := largeFixture(1).build(t)
	doubled, _ := largeFixture(2).build(t)
	if a, b := strings.Count(large, "\n"), strings.Count(doubled, "\n"); a != b {
		t.Errorf("doubling every driver changed the file from %d to %d lines", a, b)
	}
}

func TestRender_ToolsSnapshotNotOKKeepsOnlyTheAbsentLine(t *testing.T) {
	workDir, configDir := t.TempDir(), t.TempDir()
	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	snap := ToolsSnapshot{Tools: engineRows(2, "tool"), System: []string{"git"}}
	out := string(g.render(t.Context(), MCPSnapshot{}, false, ForgeSnapshot{}, false, snap, false))
	if got := sectionOf(out, "## Installed tools"); got != "\n"+debianSlimLine+"\n\n" {
		t.Errorf("Installed tools with the engine down =\n%q\nwant only the Debian slim line", got)
	}
}
