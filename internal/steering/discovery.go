package steering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/marotte/internal/sanitize"
)

// Doc is one classified per-repo steering markdown file: basename plus front-matter fields.
type Doc struct {
	Filename    string // basename, e.g. "notes.md"
	Inclusion   string // "always" | "fileMatch" | "manual" | "auto"; defaults to "always"
	FileMatch   string // glob pattern when Inclusion == "fileMatch"; empty otherwise
	Description string // human-readable description from the description field
}

// writeRepoSteering renders the per-repo steering inventory grouped by inclusion trigger,
// indented under the repo bullet.
func writeRepoSteering(b *strings.Builder, repo string, docs []Doc) {
	always := make([]Doc, 0, len(docs))
	matched := make([]Doc, 0, len(docs))
	manual := make([]Doc, 0, len(docs))
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

// writeSteeringEntry renders one steering doc bullet under a group header.
func writeSteeringEntry(b *strings.Builder, repo string, d Doc) {
	fmt.Fprintf(b, "    - `%s/.kiro/steering/%s`", repo, d.Filename)
	if d.FileMatch != "" {
		fmt.Fprintf(b, " (matches `%s`)", d.FileMatch)
	}
	if d.Description != "" {
		fmt.Fprintf(b, " — %s", d.Description)
	}
	b.WriteString("\n")
}

// writeRepoSteeringInstructions tells the agent to read per-repo steering: the agent boots
// at /workspace, so kiro-cli auto-loads steering only at that level.
func writeRepoSteeringInstructions(b *strings.Builder, repos []string, workDir string) {
	// Only when some repo carries .kiro/ content.
	hasAny := false
	for _, r := range repos {
		rd := filepath.Join(workDir, r)
		if len(findRepoDocs(rd)) > 0 || len(findRepoSkills(rd)) > 0 ||
			len(findRepoAgents(rd)) > 0 || len(findRepoHooks(rd)) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return
	}
	b.WriteString("### Per-repo .kiro protocol\n\n")
	b.WriteString("The per-repo `.kiro/` directories above are NOT auto-loaded ")
	b.WriteString("by kiro-cli (its auto-include only fires for the cwd it booted in, ")
	b.WriteString("which is `/workspace`). Treat the inventory above as a routing table:\n\n")
	b.WriteString("- **When you start working in a repo** (any prompt that mentions it, ")
	b.WriteString("or as soon as you cd / read / edit a file inside it): immediately ")
	b.WriteString("read every \"Always-loaded steering\" and \"Always-loaded skills\" file listed for that repo.\n")
	b.WriteString("- **When you open or edit a file** that matches a \"File-match\" ")
	b.WriteString("pattern (steering or skill): read the matching doc first, then ")
	b.WriteString("proceed with the change.\n")
	b.WriteString("- **\"Manual\" steering/skills** are reference material — read on demand if a ")
	b.WriteString("relevant question arises or the user references them by name.\n")
	b.WriteString("- **Custom agents**: don't read; just be aware they exist. If the user ")
	b.WriteString("asks to switch agents, you know what's available.\n")
	b.WriteString("- **Hooks**: don't read; kiro-cli runs them automatically on their ")
	b.WriteString("trigger events. If you're about to perform an action where a hook ")
	b.WriteString("triggers, proactively mention it. When the user describes a workflow ")
	b.WriteString("pattern that would benefit from a hook, suggest creating one.\n")
	b.WriteString("- One read per session is enough; subsequent prompts within the same ")
	b.WriteString("turn don't need to re-read what you already loaded.\n")
	b.WriteString("- **If a doc is added or edited during this session**: the ")
	b.WriteString("inventory above is a snapshot from when this session started and ")
	b.WriteString("won't auto-refresh. Re-read the file via read_file directly if the ")
	b.WriteString("user mentions it. A fresh chat will pick it up automatically.\n\n")
}

// writeRepoSkills renders the per-repo skills inventory grouped by
// inclusion trigger, same as steering.
func writeRepoSkills(b *strings.Builder, repo string, docs []Doc) {
	always := make([]Doc, 0, len(docs))
	matched := make([]Doc, 0, len(docs))
	manual := make([]Doc, 0, len(docs))
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
		fmt.Fprintf(b, "  - **Always-loaded skills** (`%s/.kiro/skills/`):\n", repo)
		for _, d := range always {
			writeSkillEntry(b, repo, d)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(b, "  - **File-match skills** (`%s/.kiro/skills/`):\n", repo)
		for _, d := range matched {
			writeSkillEntry(b, repo, d)
		}
	}
	if len(manual) > 0 {
		fmt.Fprintf(b, "  - **Manual skills** (invoke verbally by name):\n")
		for _, d := range manual {
			writeSkillEntry(b, repo, d)
		}
	}
}

func writeSkillEntry(b *strings.Builder, repo string, d Doc) {
	fmt.Fprintf(b, "    - `%s/.kiro/skills/%s`", repo, d.Filename)
	if d.FileMatch != "" {
		fmt.Fprintf(b, " (matches `%s`)", d.FileMatch)
	}
	if d.Description != "" {
		fmt.Fprintf(b, " — %s", d.Description)
	}
	b.WriteString("\n")
}

// findRepoDocs returns a repo's `.kiro/steering/` markdown files classified by inclusion
// mode (default "always"), via findMdDocsInDir's caps.
func findRepoDocs(repoDir string) []Doc {
	return findMdDocsInDir(filepath.Join(repoDir, ".kiro", "steering"))
}

// parseSteeringFrontmatter adapts the shared front-matter parser (Parse) onto Doc; do not
// reintroduce a local parse here. The free-text fields are defused HERE, in the one consumer
// that writes them into agent-authoritative markdown (internal/server's JSON needs no defuse).
func parseSteeringFrontmatter(data []byte) Doc {
	fm := Parse(data)
	return Doc{
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

// ParseInclusion returns the validated inclusion mode from a steering file's front-matter
// ("always" when absent or unknown), BOM- and CRLF-tolerant. Exported for internal/server.
func ParseInclusion(data []byte) string {
	return parseSteeringFrontmatter(data).Inclusion
}

// findRepoSkills scans `.kiro/skills/` for skill DIRECTORIES (holding SKILL.md, as
// internal/server's scanSkills does), classified by SKILL.md's inclusion mode. Capped.
func findRepoSkills(repoDir string) []Doc {
	dir := filepath.Join(repoDir, ".kiro", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Doc, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// A missing SKILL.md yields empty data, so the default "always".
		data, _ := readCappedFile(filepath.Join(dir, e.Name(), "SKILL.md"), FrontMatterReadCap)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = defuse(e.Name()) + "/SKILL.md"
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// AgentEntry is a custom agent config found in `.kiro/agents/`.
type AgentEntry struct {
	Filename string
	Name     string // from JSON "name" field
}

// findRepoAgents scans `.kiro/agents/` for agent configs, collapsing each
// `.json`/`.md` pair to ONE agent through DedupeAgentFiles. Capped at 10
// distinct agents.
func findRepoAgents(repoDir string) []AgentEntry {
	dir := filepath.Join(repoDir, ".kiro", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	agents := DedupeAgentFiles(entries)
	out := make([]AgentEntry, 0, min(len(agents), 10))
	for _, a := range agents {
		if len(out) >= 10 {
			break
		}
		out = append(out, AgentEntry{Filename: defuse(a.File), Name: defuse(a.Base)})
	}
	return out
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
// its own entry. Capped at 10 entries total.
func findRepoHooks(repoDir string) []HookEntry {
	dir := filepath.Join(repoDir, ".kiro", "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
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
			h.Filename = defuse(e.Name())
			out = append(out, h)
			if len(out) >= 10 {
				return out
			}
		}
	}
	return out
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
// the format Kiro's createHook tool and internal/command/hooks.go write. Malformed JSON or
// an empty hooks array yields nil.
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
		preview = defuse(preview)
		if len(preview) > 80 {
			preview = truncateUTF8(preview, 77) + "..."
		}
		out = append(out, HookEntry{
			Name:    defuse(h.Name),
			Trigger: defuse(h.Trigger),
			Command: preview,
		})
	}
	return out
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

// findMdDocsInDir classifies each `.md` in a flat directory by its front-matter. Reads are
// capped at 64 KiB and the result at 20 entries.
func findMdDocsInDir(dir string) []Doc {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Doc, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, _ := readCappedFile(path, FrontMatterReadCap)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = defuse(e.Name())
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}
