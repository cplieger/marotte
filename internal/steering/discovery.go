package steering

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/sanitize"
)

// docEntry is one classified per-repo steering markdown file: basename plus front-matter fields.
type docEntry struct {
	Filename    string // basename, e.g. "notes.md"
	Inclusion   string // "always" | "fileMatch" | "manual" | "auto"; defaults to "always"
	FileMatch   string // glob pattern when Inclusion == "fileMatch"; empty otherwise
	Description string // human-readable description from the description field
}

func writeRepoSteering(b *strings.Builder, repo string, docs []docEntry) {
	always := make([]docEntry, 0, len(docs))
	matched := make([]docEntry, 0, len(docs))
	manual := make([]docEntry, 0, len(docs))
	for _, d := range docs {
		switch d.Inclusion {
		case inclusionFileMatch:
			matched = append(matched, d)
		case inclusionManual, inclusionAuto:
			// "auto" is on demand like "manual": KAS offers both as slash commands.
			manual = append(manual, d)
		default:
			always = append(always, d)
		}
	}
	if len(always) > 0 {
		fmt.Fprintf(b, "  - **Always-loaded steering** (read these as soon as you start working in `%s/`):\n", repo)
		for _, d := range always {
			writeSteeringEntry(b, repo, d)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(b, "  - **File-match steering** (read when touching matching paths in `%s/`):\n", repo)
		for _, d := range matched {
			writeSteeringEntry(b, repo, d)
		}
	}
	if len(manual) > 0 {
		fmt.Fprintf(b, "  - **Manual steering** (read on demand or when invoked via `#name`):\n")
		for _, d := range manual {
			writeSteeringEntry(b, repo, d)
		}
	}
}

func writeSteeringEntry(b *strings.Builder, repo string, d docEntry) {
	fmt.Fprintf(b, "    - `%s/.kiro/steering/%s`", repo, d.Filename)
	writeEntryTail(b, d)
}

// writeEntryTail writes an entry's pattern and description, each bounded: both are untrusted
// front matter sent on every model call.
func writeEntryTail(b *strings.Builder, d docEntry) {
	switch {
	case len(d.FileMatch) > maxFileMatchBytes:
		b.WriteString(" (matches the `fileMatchPattern` in its front matter)")
	case d.FileMatch != "":
		fmt.Fprintf(b, " (matches `%s`)", d.FileMatch)
	}
	if d.Description != "" {
		fmt.Fprintf(b, " — %s", capWords(d.Description, maxDescWords))
	}
	b.WriteString("\n")
}

// writeRepoSteeringInstructions tells the agent to read per-repo steering: the agent boots
// in workDir, so kiro-cli auto-loads steering only at that level.
func writeRepoSteeringInstructions(b *strings.Builder, workDir string) {
	b.WriteString("### Per-repo .kiro protocol\n\n")
	b.WriteString("kiro-cli loads `.kiro/` only from `" + workDir + "`, so the repos' `.kiro/` content listed above ")
	b.WriteString("is not loaded. Use the list as a routing table:\n\n")
	b.WriteString("- When you start working in a repo (a prompt names it, or you read, edit or cd into it), ")
	b.WriteString("read its \"Always-loaded steering\" files.\n")
	b.WriteString("- Before opening or editing a file that matches a \"File-match\" pattern, read that doc or skill.\n")
	b.WriteString("- Read a \"Manual\" steering doc or a skill when the task or the user calls for it.\n")
	// KAS loads agents and hooks only from a session root's .kiro/, and no nested repo is one.
	b.WriteString("- Custom agents are listed for awareness: this session cannot load or switch to them.\n")
	b.WriteString("- Hooks do not run for nested repos: when you perform a hook's trigger action, run its command ")
	b.WriteString("yourself (for an agent hook, act on its prompt).\n")
	b.WriteString("- One read per session is enough. The list is a snapshot from session start: read a doc added ")
	b.WriteString("or edited since then directly with read_file.\n\n")
}

// writeRepoSkills renders the per-repo skills inventory: file-match skills apart, since a path
// triggers them, and every other skill read when its description matches the task.
func writeRepoSkills(b *strings.Builder, repo string, docs []docEntry) {
	matched := make([]docEntry, 0, len(docs))
	other := make([]docEntry, 0, len(docs))
	for _, d := range docs {
		if d.Inclusion == inclusionFileMatch {
			matched = append(matched, d)
		} else {
			other = append(other, d)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(b, "  - **File-match skills** (`%s/.kiro/skills/`):\n", repo)
		for _, d := range matched {
			writeSkillEntry(b, repo, d)
		}
	}
	if len(other) > 0 {
		fmt.Fprintf(b, "  - **Skills** (`%s/.kiro/skills/`; read a SKILL.md when its description matches the task):\n", repo)
		for _, d := range other {
			writeSkillEntry(b, repo, d)
		}
	}
}

func writeSkillEntry(b *strings.Builder, repo string, d docEntry) {
	fmt.Fprintf(b, "    - `%s/.kiro/skills/%s`", repo, d.Filename)
	writeEntryTail(b, d)
}

// capWords keeps s's first n words and at most maxDescBytes of them, marking a cut with " …".
func capWords(s string, n int) string {
	cut := false
	if f := strings.Fields(s); len(f) > n {
		s, cut = strings.Join(f[:n], " "), true
	}
	if len(s) > maxDescBytes {
		s, cut = strings.TrimRight(truncateUTF8(s, maxDescBytes), " "), true
	}
	if cut {
		return s + " …"
	}
	return s
}

// findRepoDocs returns a repo's `.kiro/steering/` markdown files classified by inclusion
// mode (default "always"), within findMdDocsInDir's caps, and the directory's dirCount.
func findRepoDocs(repoDir string) (docs []docEntry, seen dirCount) {
	return findMdDocsInDir(filepath.Join(repoDir, ".kiro", "steering"))
}

// Parse is the one front-matter parser. The free-text fields are defused HERE, in the one consumer
// that writes them into agent-authoritative markdown (internal/server's JSON needs no defuse).
func parseSteeringFrontmatter(data []byte) docEntry {
	fm := Parse(data)
	return docEntry{
		Inclusion:   fm.Inclusion,
		FileMatch:   defuse(fm.FileMatch),
		Description: defuse(fm.Description),
	}
}

// frontmatterBody returns the YAML front-matter between the `---` fences and whether one was
// present, after stripping a UTF-8 BOM and normalizing CRLF.
func frontmatterBody(data []byte) (string, bool) {
	content := normalizeText(data)
	if !strings.HasPrefix(content, "---\n") {
		return "", false
	}
	end := strings.Index(content[4:], "\n---")
	if end <= 0 {
		return "", false
	}
	return content[4 : 4+end], true
}

// normalizeInclusion validates an inclusion value, folding unknown or empty to "always". FOUR
// values: KAS's schema declares "auto" too, an ON-DEMAND mode; folding it to "always" would
// claim the opposite about token cost.
func normalizeInclusion(v string) string {
	switch v {
	case inclusionFileMatch:
		return inclusionFileMatch
	case inclusionManual:
		return inclusionManual
	case inclusionAuto:
		return inclusionAuto
	default:
		return inclusionAlways
	}
}

// findRepoSkills scans `.kiro/skills/` for skills (isSkillDir; one with no SKILL.md renders with
// no description) and returns keepByRank's share of the first maxScannedEntries, with the
// directory's dirCount.
func findRepoSkills(repoDir string) (skills []docEntry, seen dirCount) {
	dir := filepath.Join(repoDir, ".kiro", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, dirCount{}
	}
	out := make([]docEntry, 0, min(len(entries), maxScannedEntries))
	total := 0
	for _, e := range entries {
		if !isSkillDir(dir, e) {
			continue
		}
		data, _ := readCappedFile(filepath.Join(dir, e.Name(), "SKILL.md"), FrontMatterReadCap)
		total++
		if len(out) >= maxScannedEntries {
			continue
		}
		doc := parseSteeringFrontmatter(data)
		doc.Filename = defuse(e.Name()) + "/SKILL.md"
		out = append(out, doc)
	}
	seen = countRanks(out, skillRank, total)
	return keepByRank(out, skillRank), seen
}

// agentEntry is a custom agent config found in `.kiro/agents/`.
type agentEntry struct {
	Name string // from JSON "name" field
}

// findRepoAgents scans `.kiro/agents/` for agent configs, collapsing each
// `.json`/`.md` pair to ONE agent through DedupeAgentFiles. It returns the first
// 10 distinct agents and how many there are.
func findRepoAgents(repoDir string) (agents []agentEntry, total int) {
	dir := filepath.Join(repoDir, ".kiro", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	all := DedupeAgentFiles(entries)
	out := make([]agentEntry, 0, min(len(all), 10))
	for _, a := range all[:min(len(all), 10)] {
		out = append(out, agentEntry{Name: defuse(a.Base)})
	}
	return out, len(all)
}

// HookEntry is one hook from a `.kiro/hooks/*.json` document.
type HookEntry struct {
	Filename string
	Name     string // hook name from the v1 envelope
	Trigger  string // PascalCase trigger: "SessionStart", "PreToolUse", "PostFileSave", …
	Command  string // truncated action preview (command, or prompt for agent hooks)
}

// findRepoHooks scans `.kiro/hooks/` for JSON hook documents. Each file
// is a v1 envelope carrying one or more hooks; every hook renders as
// its own entry. It returns the first 10, and more reports an 11th.
func findRepoHooks(repoDir string) (hooks []HookEntry, more bool) {
	dir := filepath.Join(repoDir, ".kiro", "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var out []HookEntry
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, _ := readCappedFile(path, 16<<10)
		for _, h := range parseHookDoc(data) {
			if len(out) >= 10 {
				return out, true
			}
			h.Filename = defuse(e.Name())
			out = append(out, h)
		}
	}
	return out, false
}

// ParseHooks parses a v1 hook document into its entries, every field defused. Exported for
// the REST docs scanner.
func ParseHooks(data []byte) []HookEntry {
	return parseHookDoc(data)
}

// parseHookDoc parses a v1 hook document:
//
//	{"version":"v1","hooks":[{name, trigger, matcher?,
//	  action:{type:"command"|"agent", command|prompt}, timeout?}]}
//
// the format Kiro's createHook tool writes. Malformed JSON or an empty hooks array yields nil.
func parseHookDoc(data []byte) []HookEntry {
	var doc struct {
		Hooks []struct {
			Name    string `json:"name"`
			Trigger string `json:"trigger"`
			Action  struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Prompt  string `json:"prompt"`
			} `json:"action"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make([]HookEntry, 0, len(doc.Hooks))
	for _, h := range doc.Hooks {
		preview := h.Action.Command
		if h.Action.Type == "agent" {
			preview = h.Action.Prompt
		}
		out = append(out, HookEntry{
			Name:    defuse(h.Name),
			Trigger: defuse(h.Trigger),
			Command: capPreview(defuse(preview)),
		})
	}
	return out
}

// capPreview keeps at most 80 bytes of s, marking a cut with "...".
func capPreview(s string) string {
	if len(s) > 80 {
		return truncateUTF8(s, 77) + "..."
	}
	return s
}

// defuse flattens control characters, strips hidden Unicode and swaps backticks for quotes:
// every workspace-derived string must pass it before landing in environment.md, which
// kiro-cli treats as authoritative. A raw newline could forge a line; a backtick close a span.
func defuse(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		case '`':
			return '\''
		}
		return r
	}, s)
	return sanitize.Unicode(s)
}

// findMdDocsInDir classifies a flat directory's `.md` files by their front matter, reading the
// first maxScannedEntries, and returns keepByRank's share with the directory's dirCount.
func findMdDocsInDir(dir string) (docs []docEntry, seen dirCount) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, dirCount{}
	}
	out := make([]docEntry, 0, min(len(entries), maxScannedEntries))
	total := 0
	for _, e := range entries {
		if !isSteeringDoc(e) {
			continue
		}
		total++
		if len(out) >= maxScannedEntries {
			continue
		}
		data, _ := readCappedFile(filepath.Join(dir, e.Name()), FrontMatterReadCap)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = defuse(e.Name())
		out = append(out, doc)
	}
	seen = countRanks(out, docRank, total)
	return keepByRank(out, docRank), seen
}

// Discovery reads front matter for up to maxScannedEntries of a directory's entries, each read
// capped at FrontMatterReadCap, and keeps maxRepoEntries of them, ranked, so an entry among the
// first maxScannedEntries by filename is kept before any lower-ranked one; past that window only
// the overflow count shows it.
const (
	maxScannedEntries = 100
	maxRepoEntries    = 20
)

// dirCount is how many entries a directory holds, and how many of each routing rank discovery
// read: the entries past maxScannedEntries have no rank.
type dirCount struct {
	byRank [routingRanks]int
	total  int
}

func countRanks(read []docEntry, rankOf func(docEntry) int, total int) dirCount {
	c := dirCount{total: total}
	for _, d := range read {
		c.byRank[rankOf(d)]++
	}
	return c
}

// keepByRank keeps the first maxRepoEntries of entries by routing rank, each rank in the
// directory's filename order.
func keepByRank(entries []docEntry, rankOf func(docEntry) int) []docEntry {
	slices.SortStableFunc(entries, func(a, b docEntry) int { return cmp.Compare(rankOf(a), rankOf(b)) })
	return entries[:min(len(entries), maxRepoEntries)]
}

func isSteeringDoc(e os.DirEntry) bool {
	return !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".md")
}

// isEmptyDir reports whether dir lists no entries; an unreadable one counts as empty.
func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil || len(entries) == 0
}

// isSkillDir reports whether skills/<e> is a skill: a non-dot directory, or a link to one inside
// `.kiro` (KAS follows a linked skill directory), that holds anything. KAS skips one with no
// SKILL.md, but it is listed as a broken skill to repair; an empty one has nothing to show.
func isSkillDir(skills string, e os.DirEntry) bool {
	if strings.HasPrefix(e.Name(), ".") {
		return false
	}
	p := filepath.Join(skills, e.Name())
	switch {
	case e.Type()&os.ModeSymlink != 0:
		if !linksToSkill(p, skills) {
			return false
		}
	case !e.IsDir():
		return false
	}
	return !isEmptyDir(p)
}

// linksToSkill reports whether link resolves beneath skills' `.kiro` and neither to skills nor to
// a directory holding it, which would list that tree as a skill. All three are resolved first,
// since `.kiro` or skills may itself be a link.
func linksToSkill(link, skills string) bool {
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false
	}
	resolvedKiro, err := filepath.EvalSymlinks(filepath.Dir(skills))
	if err != nil {
		return false
	}
	resolvedSkills, err := filepath.EvalSymlinks(skills)
	if err != nil {
		return false
	}
	return strings.HasPrefix(target, resolvedKiro+string(filepath.Separator)) &&
		!pathinside.Root(target).Contains(resolvedSkills)
}
