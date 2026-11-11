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
	tools         ToolsSnapshot
	kiroRepos     []kiroRepoFixture
	mcp           int
	forgeAccounts int // 0 means one
	forgeRepos    int // per account
	plainRepos    int
	dirs          int
	granted       int
	memory        bool
	readmes       bool // plain repos get a README paragraph and a branch to prove neither is rendered
}

type kiroRepoFixture struct {
	name     string
	steering []string // front matter bodies, one per doc
	skills   []string
	hooks    int
	agents   int
}

// rendered is one fixture's environment.md and the directories it was rendered for.
type rendered struct {
	out, workDir, configDir string
}

const (
	readmeSentinel = "READMESENTINELPARAGRAPH"
	branchSentinel = "sentinel-branch"
)

// build lays the fixture out on disk and renders environment.md for it.
func (f *renderFixture) build(t *testing.T) rendered {
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
		writeRepo(t, workDir, k.name, "https://github.com/owner/"+k.name+".git", false)
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
		for i := range k.agents {
			mustWrite(t, filepath.Join(workDir, k.name, ".kiro", "agents", fmt.Sprintf("a%02d.json", i)), `{"name":"a"}`)
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
	var forges ForgeSnapshot
	for a := range max(f.forgeAccounts, 1) {
		p := ForgeProvider{Kind: "gitea", Host: fmt.Sprintf("git%02d.example.org", a), User: "someone"}
		for i := range f.forgeRepos {
			p.Repos = append(p.Repos, fmt.Sprintf("someone/project%03d", i))
		}
		forges.Providers = append(forges.Providers, p)
	}
	g := New(Config{
		WorkDir: workDir, ConfigDir: configDir, ToolsDir: filepath.Join(configDir, "tools"), ServerAddr: "127.0.0.1:9847",
	})
	out := string(g.render(t.Context(), mcp, f.mcp > 0, forges, true, f.tools, true))
	return rendered{out: out, workDir: workDir, configDir: configDir}
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

func aptRows(n int, prefix string) []InstalledTool {
	rows := engineRows(n, prefix)
	for i := range rows {
		rows[i].Apt = true
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

// baseSystem is what the image bakes in, present on every host.
const baseSystem = "bash curl git jq openssl rg scp sftp ssh ssh-add ssh-keygen ssh-keyscan tar unzip xz"

const debianSlimHead = "The image is Debian slim: its base userland (coreutils, util-linux, findutils, grep, sed, awk, " +
	"diff, gzip, which, perl, dpkg and apt-get) is present and not listed here."

const debianSlimTail = " Check any other command with `command -v`, and install through the tools engine below."

func smallFixture() *renderFixture {
	return &renderFixture{
		tools:      ToolsSnapshot{System: strings.Fields(baseSystem)},
		forgeRepos: 12,
		plainRepos: 3,
		readmes:    true,
	}
}

// typicalFixture installs file and an ssh client from Debian, a long-token description and a
// long file-match pattern among its routing entries.
func typicalFixture() *renderFixture {
	tools := engineRows(15, "tool")
	tools = append(tools,
		InstalledTool{Version: "7:7.1", Commands: []string{"ffmpeg", "ffprobe"}, Apt: true},
		InstalledTool{Version: "1:5.46", Commands: []string{"file"}, Apt: true},
		InstalledTool{Version: "1:10.0", Commands: []string{"scp", "sftp", "ssh"}, Apt: true},
		InstalledTool{Version: "2.0", Commands: []string{"imgconv"}})
	tools = append(tools, libraryRows(6)...)
	return &renderFixture{
		tools:      ToolsSnapshot{Tools: tools, System: strings.Fields(baseSystem)},
		mcp:        3,
		forgeRepos: 30,
		plainRepos: 10,
		kiroRepos: []kiroRepoFixture{
			{name: "kiroone", steering: []string{
				"inclusion: always\ndescription: The house rules.",
				"inclusion: fileMatch\nfileMatchPattern: \"src/**\"\ndescription: Source rules.",
				"inclusion: manual\ndescription: " + strings.Repeat("x", 1000),
				"inclusion: fileMatch\nfileMatchPattern: \"" + strings.Repeat("a/", 150) + "\"\ndescription: Long pattern.",
			}},
			{name: "kirotwo", skills: []string{"description: " + words(60)}},
		},
	}
}

// largeFixture sits over every cap at scale 1, so a larger scale changes only overflow counts.
// Its tenth .kiro repo is over discovery's per-repo caps, with its always-loaded doc and its
// file-match skill sorting after 25 others; each other one's always-loaded doc sorts after its
// manual and file-match docs. Each forge account holds one page of 100 names, as a forge reports.
func largeFixture(scale int) *renderFixture {
	tools := engineRows(200*scale, "cmd")
	tools = append(tools, aptRows(10*scale, "aux")...)
	tools = append(tools, libraryRows(400*scale)...)
	var kiro []kiroRepoFixture
	for i := range 72 * scale {
		kiro = append(kiro, kiroRepoFixture{
			name: fmt.Sprintf("kiro%03d", i),
			steering: []string{
				"inclusion: manual\ndescription: Manual doc.",
				"inclusion: fileMatch\nfileMatchPattern: \"*.go\"\ndescription: Go doc.",
				"inclusion: always\ndescription: Always doc.",
			},
			skills: []string{"description: First skill.", "description: Second skill."},
			hooks:  1,
		})
	}
	heavy := &kiro[9]
	heavy.steering, heavy.skills = nil, nil
	for range 25 * scale {
		heavy.steering = append(heavy.steering, "inclusion: manual\ndescription: Heavy doc.")
		heavy.skills = append(heavy.skills, "description: Heavy skill.")
	}
	heavy.steering = append(heavy.steering, "inclusion: always\ndescription: Heavy house rules.")
	heavy.skills = append(heavy.skills, "inclusion: fileMatch\nfileMatchPattern: \"*.sql\"\ndescription: Heavy file-match skill.")
	heavy.hooks, heavy.agents = 12*scale, 12*scale
	return &renderFixture{
		tools:         ToolsSnapshot{Tools: tools, System: strings.Fields(baseSystem)},
		mcp:           40 * scale,
		forgeAccounts: 8 * scale,
		forgeRepos:    100,
		plainRepos:    100 * scale,
		kiroRepos:     kiro,
		dirs:          40 * scale,
		granted:       30 * scale,
		memory:        true,
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
		check   func(t *testing.T, r rendered)
		name    string
	}{{
		name:    "small",
		fixture: smallFixture(),
		check: func(t *testing.T, r rendered) {
			out := r.out
			tools := sectionOf(out, "## Installed tools")
			if !strings.Contains(tools, "Baked into the image: bash, curl, git, jq, openssl, rg, scp, sftp, ssh, ssh-add, ssh-keygen, ssh-keyscan, tar, unzip, xz\n") {
				t.Errorf("Installed tools lacks the baked-in line:\n%s", tools)
			}
			if want := debianSlimHead + " The image does not ship file, bc, xxd, netstat or ss." + debianSlimTail; !strings.Contains(tools, want) {
				t.Errorf("Installed tools lacks the Debian slim line naming every missing utility:\n%s", tools)
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
		check: func(t *testing.T, r rendered) {
			out := r.out
			tools := sectionOf(out, "## Installed tools")
			commands := lineWithPrefix(tools, "Commands: ")
			for _, want := range []string{"ffmpeg 7:7.1", "ffprobe 7:7.1", "file 1:5.46", "imgconv 2.0", "ssh 1:10.0", "tool000 1.0.0", "tool014 1.0.14"} {
				if !strings.Contains(commands, want) {
					t.Errorf("Commands line lacks %q: %q", want, commands)
				}
			}
			if got := len(strings.Split(strings.TrimPrefix(commands, "Commands: "), ", ")); got != 22 {
				t.Errorf("Commands line names %d commands, want 22 (15 engine, ffmpeg's 2, file, the ssh client's 3, imgconv): %q", got, commands)
			}
			if got := lineWithPrefix(tools, "Baked into the image: "); got != "Baked into the image: bash, curl, git, jq, openssl, rg, ssh-add, ssh-keygen, ssh-keyscan, tar, unzip, xz" {
				t.Errorf("Baked line = %q, want the names Commands already lists left out", got)
			}
			if want := debianSlimHead + " The image does not ship bc, xxd, netstat or ss." + debianSlimTail; !strings.Contains(tools, want) {
				t.Errorf("the Debian slim line calls the installed file missing:\n%s", tools)
			}
			if !strings.Contains(tools, "\n6 more installed packages ship no command (mostly libraries): "+
				"`dpkg -l` or `curl -s 127.0.0.1:9847/api/tools` lists them.\n") {
				t.Errorf("Installed tools lacks the no-command count line:\n%s", tools)
			}
			if !strings.Contains(out, "  - **Skills** (`kirotwo/.kiro/skills/`; read a SKILL.md when its description matches the task):\n"+
				"    - `kirotwo/.kiro/skills/skill00/SKILL.md` — "+words(20)+" …\n") {
				t.Errorf("the skill is not under **Skills** with a 20-word description:\n%s", sectionOf(out, "### Git repositories"))
			}
			if !strings.Contains(out, "    - `kiroone/.kiro/steering/doc02.md` — "+strings.Repeat("x", 300)+" …\n") {
				t.Errorf("a one-token description is not cut at 300 bytes:\n%s", sectionOf(out, "### Git repositories"))
			}
			if !strings.Contains(out, "    - `kiroone/.kiro/steering/doc03.md` (matches the `fileMatchPattern` in its front matter) — Long pattern.\n") {
				t.Errorf("a long file-match pattern is not replaced by a pointer:\n%s", sectionOf(out, "### Git repositories"))
			}
			if strings.Contains(out, "Always-loaded skills") || strings.Contains(out, "Manual skills") {
				t.Errorf("a skill is labelled by an inclusion it does not have")
			}
			if got, want := sectionOf(out, "### Per-repo .kiro protocol"), strings.TrimPrefix(strings.ReplaceAll(wantProtocol, "@WORK@", r.workDir), "### Per-repo .kiro protocol\n"); got != want {
				t.Errorf("protocol =\n%s\nwant\n%s", got, want)
			}
			for _, repo := range []string{"kiroone", "kirotwo"} {
				if !strings.Contains(out, "\n- `"+repo+"/`\n") {
					t.Errorf("the .kiro repo %s has no line of its own", repo)
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
		check:   checkLarge,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.fixture.build(t)
			if !strings.Contains(r.out, "This file is auto-generated by marotte") || !strings.Contains(r.out, "edits are overwritten") {
				t.Errorf("the auto-generated header is gone")
			}
			if tools := sectionOf(r.out, "## Installed tools"); !strings.Contains(tools, debianSlimHead) || !strings.Contains(tools, debianSlimTail) {
				t.Errorf("Installed tools lacks the Debian slim line")
			}
			tc.check(t, r)
		})
	}
}

// checkLarge derives each count from largeFixture(1) and the caps' rules.
func checkLarge(t *testing.T, r rendered) {
	out, workDir := r.out, r.workDir
	commands := lineWithPrefix(out, "Commands: ")
	head, tail, ok := strings.Cut(strings.TrimPrefix(commands, "Commands: "), ", … and ")
	if !ok || len(strings.Split(head, ", ")) != 120 || strings.Contains(head, "aux") {
		t.Errorf("Commands line does not name exactly 120 engine commands then an overflow: %q", commands)
	}
	if want := "90 more (`command -v <name>` checks one; `curl -s 127.0.0.1:9847/api/tools` lists every row's commands)"; tail != want {
		t.Errorf("Commands overflow = %q, want %q", tail, want)
	}
	if !strings.Contains(out, "\n400 more installed packages ship no command") {
		t.Errorf("the no-command count is not 400")
	}
	other := lineWithPrefix(out, "- Other repositories: ")
	if got := len(codeSpan.FindAllString(other, -1)); got != 61 || !strings.HasSuffix(other, ", … and 40 more (`ls "+workDir+"`)") {
		t.Errorf("Other repositories line does not name 60 then `… and 40 more`: %q", other)
	}
	checkLargeRouting(t, out, workDir)
	mcp := sectionOf(out, "## Connected integrations")
	if got := strings.Count(mcp, "\n- **server"); got != 30 || !strings.Contains(mcp, "\n- … and 10 more (the load_resource catalog lists them)\n") {
		t.Errorf("Connected integrations names %d servers, want 30 then `… and 10 more`", got)
	}
	dirs := sectionOf(out, "### Directories")
	if got := strings.Count(dirs, "\n- `dir"); got != 30 || !strings.Contains(dirs, "\n- … and 10 more (`ls "+workDir+"`)\n") {
		t.Errorf("Directories lists %d, want 30 then `… and 10 more`:\n%s", got, dirs)
	}
	extra := sectionOf(out, "## Additional directories")
	if got := strings.Count(extra, "\n- `/"); got != 20 ||
		!strings.Contains(extra, "\n- … and 10 more (`additional_directories` in `"+r.configDir+"/config.json`)\n") {
		t.Errorf("Additional directories lists %d, want 20 then `… and 10 more` naming the setting:\n%s", got, extra)
	}
	forge := sectionOf(out, "## Connected forges")
	if got := strings.Count(forge, "\n### gitea ("); got != 5 ||
		!strings.Contains(forge, "\n… and 3 more connected accounts, authenticated the same way (the Sources tab lists them).\n") {
		t.Errorf("forge lists %d accounts, want 5 then `… and 3 more`:\n%s", got, forge)
	}
	if got := strings.Count(forge, "\n  - someone/"); got != 100 ||
		strings.Count(forge, "\n  - … and at least 80 more (the Sources tab lists them)\n") != 5 {
		t.Errorf("forge lists %d repos, want 20 then `… and at least 80 more` for each of 5 accounts", got)
	}
	memory := sectionOf(out, "## Memory")
	if got := strings.Count(memory, "`: `repo:"); got != 30 ||
		!strings.Contains(memory, "\n- … and 142 more (each repository's tag is `repo:<provider>/<owner>/<name>` from its "+
			"origin: the provider is github, gitlab, bitbucket or azure when the host name contains that word, "+
			"the host name otherwise; on git.amazon.com or a subdomain of it the tag is "+
			"`repo:gitfarm/gitfarm/<path after pkg/, else its last segment>`)\n") {
		t.Errorf("Memory lists %d tags, want 30 then `… and 142 more`:\n%s", got, memory)
	}
	if !strings.Contains(memory, "\n- `kiro000/`: `repo:github/owner/kiro000`\n") {
		t.Errorf("a github.com origin is not tagged by its provider word:\n%s", memory)
	}
}

// checkLargeRouting: the first ten .kiro repos get blocks; the budget goes to the ten always-loaded
// docs, then nine file-match docs and kiro009's file-match skill, then 20 on-demand entries in repo
// order (three each for kiro000-kiro005, two for kiro006); the remaining 62 repos fold onto one line.
func checkLargeRouting(t *testing.T, out, workDir string) {
	if got := len(routingEntry.FindAllString(out, -1)); got != 40 {
		t.Errorf("routing lists %d steering and skill entries, want 40", got)
	}
	for i := range 9 {
		r := fmt.Sprintf("kiro%03d", i)
		if !strings.Contains(out, "  - **Always-loaded steering** (read these as soon as you start working in `"+r+"/`):\n"+
			"    - `"+r+"/.kiro/steering/doc02.md` — Always doc.\n") {
			t.Errorf("%s lost its always-loaded doc to the routing budget", r)
		}
	}
	if !strings.Contains(out, "  - **Always-loaded steering** (read these as soon as you start working in `kiro009/`):\n"+
		"    - `kiro009/.kiro/steering/doc25.md` — Heavy house rules.\n") ||
		!strings.Contains(out, "  - **File-match skills** (`kiro009/.kiro/skills/`):\n"+
			"    - `kiro009/.kiro/skills/skill25/SKILL.md` (matches `*.sql`) — Heavy file-match skill.\n") {
		t.Errorf("kiro009 lost an entry that sorts after its 20th to discovery's per-repo cap:\n%s", sectionOf(out, "### Git repositories"))
	}
	if strings.Contains(out, "Heavy doc.") || strings.Contains(out, "Heavy skill.") {
		t.Errorf("kiro009's on-demand entries outranked another repo's within the budget")
	}
	for r, cut := range map[string]int{"kiro006": 1, "kiro007": 3, "kiro008": 3, "kiro009": 50} {
		if !strings.Contains(out, fmt.Sprintf("  - … %d more .kiro entries: list `%s/.kiro/steering/` and `%s/.kiro/skills/`\n", cut, r, r)) {
			t.Errorf("%s lacks its `… %d more .kiro entries` line", r, cut)
		}
	}
	if strings.Contains(out, "more .kiro entries: list `kiro005/") {
		t.Errorf("kiro005, inside the budget, got an overflow line")
	}
	if !strings.Contains(out, " `a09` … and 2 more\n") || !strings.Contains(out, "    - … more hooks: list `kiro009/.kiro/hooks/`\n") {
		t.Errorf("kiro009's agents and hooks past discovery's caps are not counted:\n%s", sectionOf(out, "### Git repositories"))
	}
	folded := make([]string, 0, 60)
	for i := 10; i < 70; i++ {
		folded = append(folded, fmt.Sprintf("`kiro%03d/`", i))
	}
	want := "- More repositories with .kiro content: " + strings.Join(folded, ", ") +
		", … and 2 more (`ls -ad " + workDir + "/*/.kiro " + workDir + "/.[!.]*/.kiro`). Before working in one, list its " +
		"`.kiro/steering/`, `.kiro/skills/`, `.kiro/hooks/` and `.kiro/agents/`: read every steering doc whose `inclusion` " +
		"is not `fileMatch`, `manual` or `auto`, check the `fileMatchPattern` in its steering docs and `skills/*/SKILL.md` " +
		"before opening or editing a matching file, and apply the protocol below to the rest."
	if got := lineWithPrefix(out, "- More repositories with .kiro content: "); got != want {
		t.Errorf("folded .kiro repos line =\n%s\nwant\n%s", got, want)
	}
}

var digits = regexp.MustCompile(`[0-9]+`)

// Past every cap, a larger setup changes only the overflow counts: with every number masked, a
// list that kept growing would still show.
func TestRender_LargeSetupIsBounded(t *testing.T) {
	large := digits.ReplaceAllString(largeFixture(1).build(t).out, "N")
	doubled := digits.ReplaceAllString(largeFixture(2).build(t).out, "N")
	if large == doubled {
		return
	}
	a, b := strings.Split(large, "\n"), strings.Split(doubled, "\n")
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			t.Fatalf("doubling every driver changed line %d:\n%s\nto\n%s", i+1, a[i], b[i])
		}
	}
	t.Errorf("doubling every driver changed the file from %d to %d lines", len(a), len(b))
}

func TestRender_ToolsSnapshotNotOKListsNothingAsPresent(t *testing.T) {
	workDir, configDir := t.TempDir(), t.TempDir()
	g := New(Config{WorkDir: workDir, ConfigDir: configDir, ToolsDir: filepath.Join(configDir, "tools")})
	snap := ToolsSnapshot{Tools: engineRows(2, "tool"), System: []string{"git"}}
	out := string(g.render(t.Context(), MCPSnapshot{}, false, ForgeSnapshot{}, false, snap, false))
	want := "\nThe image is Debian slim: its base userland (coreutils, util-linux, findutils, grep, sed, awk, diff, gzip, " +
		"which, perl, dpkg and apt-get) is present and not listed here. The tools engine did not answer, so the " +
		"commands installed or baked in are not listed: check with `command -v`, and install through the tools " +
		"engine below.\n\n"
	if got := sectionOf(out, "## Installed tools"); got != want {
		t.Errorf("Installed tools with the engine down =\n%q\nwant\n%q", got, want)
	}
}

// Every line that tells an agent where marotte's server answers names the configured address, so
// a LISTEN_ADDR on another host or port leaves no line pointing at the default.
func TestRender_EveryServerLineNamesTheServerAddress(t *testing.T) {
	workDir, configDir := t.TempDir(), t.TempDir()
	g := New(Config{
		WorkDir: workDir, ConfigDir: configDir, ToolsDir: filepath.Join(configDir, "tools"), ServerAddr: "192.0.2.10:9000",
	})
	tools := append(engineRows(maxCommands+1, "tool"), InstalledTool{Version: "2.0", Apt: true})
	out := string(g.render(t.Context(), MCPSnapshot{}, false, ForgeSnapshot{}, false, ToolsSnapshot{Tools: tools}, true))
	for _, want := range []string{
		"marotte's own HTTP server answers on `192.0.2.10:9000` inside the container",
		"`curl -s '192.0.2.10:9000/api/tools/search?q=<name>'`",
		"`curl -s -X POST 192.0.2.10:9000/api/tools ",
		"`GET 192.0.2.10:9000/api/tools/jobs` lists jobs",
		"`dpkg -l` or `curl -s 192.0.2.10:9000/api/tools` lists them",
		"`curl -s 192.0.2.10:9000/api/tools` lists every row's commands",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("environment.md does not say %q", want)
		}
	}
	if strings.Contains(out, ":9847") {
		t.Errorf("environment.md still names the default port with ServerAddr 192.0.2.10:9000:\n%s", out)
	}
}
