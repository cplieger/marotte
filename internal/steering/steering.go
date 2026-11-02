// Package steering generates the steering kiro-cli reads, in two outputs. environment.md
// holds container-wide facts and is read by every session on this HOME. ChatDocs renders the
// chat-only guidance a chat session's door carries. Both run synchronously on the
// session-start path, so they stay network-free: slow data arrives through snapshot callbacks.
package steering

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/workspace"
)

// Read caps bound untrusted workspace input (agent-cloned repos) so a crafted file cannot
// OOM the container.
const (
	firstLineReadCap = 4 << 10 // README first non-heading line fits easily in 4 KiB
	toolsManifestCap = 1 << 20 // any realistic tools.json stays well under 1 MiB
	// steeringCompareCap bounds the read of the existing environment.md for the unchanged check;
	// anything larger is not a file this wrote, compares unequal, and is replaced.
	steeringCompareCap = 4 << 20

	inclusionAlways    = "always"
	inclusionFileMatch = "fileMatch"
	inclusionManual    = "manual"
	inclusionAuto      = "auto"
)

// MCPSnapshot is the subset of the MCP runtime registry the generator uses.
type MCPSnapshot struct {
	Servers []marotte.MCPSnapshotServer
}

// ForgeSnapshot describes connected forge providers for the steering file.
type ForgeSnapshot struct {
	Providers []ForgeProvider
}

// ForgeProvider is one connected forge with its repos.
type ForgeProvider struct {
	Kind  string   // "github", "gitlab", etc.
	Host  string   // "github.com", "gitlab.company.com"
	User  string   // authenticated username
	Email string   // authenticated email (best-effort, may be "")
	Repos []string // top repo names (capped for brevity)
}

// InstalledTool is one tools-engine row the engine's live probe reports installed.
type InstalledTool struct {
	Name    string // the row's name, which tools-state.json records its commands under
	Kind    string // the source kind: "aqua", "apt", "npm" and so on
	Version string // the installed version, "" when none is recorded
}

// Generator produces steering files for kiro-cli.
type Generator struct {
	mcpSnapshot   func() MCPSnapshot
	forgeSnapshot func() ForgeSnapshot
	toolsSnapshot func() ([]InstalledTool, bool)
	workDir       string
	configDir     string
	toolsDir      string
	mu            sync.Mutex // guards the snapshot callbacks only
	// genMu is held from the snapshots to the write, so an older generation never overwrites a newer one.
	genMu sync.Mutex
}

// New returns a Generator. Each directory must pass CheckPath; toolsDir is the tools engine's root,
// which need not sit under configDir, or "" when the engine has no usable one.
func New(workDir, configDir, toolsDir string) *Generator {
	return &Generator{workDir: workDir, configDir: configDir, toolsDir: toolsDir}
}

// SetMCPSnapshot wires a snapshot callback (unset omits the MCP section).
func (g *Generator) SetMCPSnapshot(fn func() MCPSnapshot) {
	g.mu.Lock()
	g.mcpSnapshot = fn
	g.mu.Unlock()
}

// SetForgeSnapshot wires a callback that returns connected forge info.
// Called once after construction. If unset, the forge section is omitted.
func (g *Generator) SetForgeSnapshot(fn func() ForgeSnapshot) {
	g.mu.Lock()
	g.forgeSnapshot = fn
	g.mu.Unlock()
}

// SetToolsSnapshot wires a callback that returns the installed tools, with ok false when the
// tools engine is down or its inventory failed. Unset or not ok omits the Installed tools
// section.
func (g *Generator) SetToolsSnapshot(fn func() (tools []InstalledTool, ok bool)) {
	g.mu.Lock()
	g.toolsSnapshot = fn
	g.mu.Unlock()
}

// Generate renders environment.md and writes it atomically, and skips a byte-identical write (MCP
// event storms regenerate often). Calls serialise: each one's snapshots, render and write finish
// before the next call takes its snapshots. The snapshot callbacks run under that serialisation
// alone, so one must never wait on a goroutine that is calling Generate. A cancelled ctx stops a
// call before its snapshots and again before its write, so a shutdown logs no failed write.
func (g *Generator) Generate(ctx context.Context) {
	g.genMu.Lock()
	defer g.genMu.Unlock()
	if ctx.Err() != nil {
		return
	}

	g.mu.Lock()
	snapshotFn := g.mcpSnapshot
	forgeFn := g.forgeSnapshot
	toolsFn := g.toolsSnapshot
	g.mu.Unlock()
	var mcp MCPSnapshot
	var forges ForgeSnapshot
	var tools []InstalledTool
	var toolsOK bool
	if snapshotFn != nil {
		mcp = snapshotFn()
	}
	if forgeFn != nil {
		forges = forgeFn()
	}
	if toolsFn != nil {
		tools, toolsOK = toolsFn()
	}
	if ctx.Err() != nil {
		return
	}

	content := g.render(ctx, mcp, snapshotFn != nil, forges, forgeFn != nil, tools, toolsOK)
	steeringFile := workspace.KiroSteeringPath("environment.md")

	// readCappedFile, not os.ReadFile: a FIFO or symlink may stand here (invariant 6). Any error
	// falls through to the write, which replaces a bogus file.
	if existing, readErr := readCappedFile(steeringFile, steeringCompareCap); readErr == nil && bytes.Equal(existing, content) {
		slog.Debug("steering: content unchanged, skipping write", "path", steeringFile)
		return
	}

	// 0600 for a file listing the workspace layout and MCP server names; WithMkdirMode creates the
	// parent narrowly (MkdirAll would widen it to 0755).
	if _, wErr := atomicfile.WriteFile(ctx, steeringFile, content,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); wErr != nil {
		slog.Error("steering: write", "path", steeringFile, "error", wErr)
		return
	}
	slog.Info("steering: wrote", "path", steeringFile, "bytes", len(content))
}

// hasMCP and hasForges say whether a snapshot callback was wired at all, which decides whether the
// MCP and forge sections appear; toolsOK says the tools snapshot answered.
func (g *Generator) render(ctx context.Context, mcp MCPSnapshot, hasMCP bool, forges ForgeSnapshot, hasForges bool,
	tools []InstalledTool, toolsOK bool,
) []byte {
	var b strings.Builder
	writeIntro(&b, g.workDir)

	if toolsOK {
		// The inventory carries no bins: tools-state.json records them. An unreadable file
		// leaves every tool without a recorded command.
		data, _ := readCappedFile(filepath.Join(g.configDir, "tools-state.json"), toolsManifestCap)
		writeTools(&b, tools, data)
	}
	writeRuntime(&b, g.configDir, g.toolsDir)
	writeToolsEngine(&b, g.configDir, g.toolsDir, g.workDir)
	if hasMCP {
		writeMCP(&b, mcp)
	}
	if hasForges {
		writeForges(&b, forges)
	}
	writeWorkspace(ctx, &b, g.workDir)
	granted, _, _ := settings.AgentDirs{Workspace: g.workDir, Config: g.configDir}.Granted(ctx)
	writeAdditionalDirs(&b, granted)
	writeMemory(ctx, &b, g.workDir, g.configDir)
	writeLimitations(&b)
	writeCapabilities(&b, g.configDir)
	return []byte(b.String())
}

const chatSteeringName = "marotte"

// ChatDocs renders the chat-only steering a chat session sends on its door, writing
// nothing to disk (a file would reach every session on this HOME).
func (g *Generator) ChatDocs(context.Context) []marotte.ClientSteeringDoc {
	g.mu.Lock()
	forgeFn := g.forgeSnapshot
	g.mu.Unlock()
	var forges ForgeSnapshot
	if forgeFn != nil {
		forges = forgeFn()
	}
	return []marotte.ClientSteeringDoc{{
		Name:      chatSteeringName,
		Inclusion: inclusionAlways,
		Content:   g.renderChat(forges),
	}}
}

func (g *Generator) renderChat(forges ForgeSnapshot) string {
	var b strings.Builder
	b.WriteString("# Marotte chat\n\n")
	b.WriteString("This session is a chat in marotte, the browser app the user is looking at. ")
	b.WriteString("These sections describe what the user sees; the environment steering doc ")
	b.WriteString("covers the container.\n\n")
	writeGitPanel(&b, g.workDir, len(forges.Providers) > 0)
	writeUIGuide(&b, g.workDir, g.configDir)
	writeAttachments(&b, marotte.DefaultUploadDir, g.workDir, g.configDir)
	writeChatCapabilities(&b)
	return b.String()
}

// Errors are log-and-omit. It opens with atomicfile.OpenRegular: a FIFO would hang Generate, which
// runs synchronously before every session door, and a symlinked README would copy whatever it names
// (an MCP credential, measured) into authoritative agent context. Truncation is kept: only the head
// matters.
func readCappedFile(path string, limit int64) ([]byte, error) {
	f, _, err := atomicfile.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// CustomPath returns the path to the custom.md steering file in the kiro home directory.
func (*Generator) CustomPath() string {
	return workspace.KiroSteeringPath("custom.md")
}

// toolState is the subset of a tools-state.json row (the toolbelt engine's ToolStatus) this
// generator reads: the commands a tool owns.
type toolState struct {
	Bins   []string `json:"bins,omitempty"`
	PMBins []string `json:"pm_bins,omitempty"`
}

// writeTools lists installed's commands from the bins state records. A tool with none is listed
// as a package, never under its name as a command: an apt package's commands need not match its
// name (ripgrep ships rg), and an npm package may ship none.
func writeTools(b *strings.Builder, installed []InstalledTool, state []byte) {
	var recorded struct {
		Tools map[string]toolState `json:"tools"`
	}
	if len(state) > 0 {
		if err := json.Unmarshal(state, &recorded); err != nil {
			slog.Error("steering: parse tools-state.json", "error", err)
		}
	}
	var commands, packages []toolEntry
	for _, t := range installed {
		// Defused: names and versions come from the catalog, hand edits and --version output.
		version := defuse(t.Version)
		st := recorded.Tools[t.Name]
		bins := slices.Concat(st.Bins, st.PMBins)
		if len(bins) == 0 {
			packages = append(packages, toolEntry{defuse(t.Name), version, defuse(t.Kind)})
			continue
		}
		for _, bin := range bins {
			commands = append(commands, toolEntry{defuse(bin), version, ""})
		}
	}
	if len(commands)+len(packages) == 0 {
		return
	}
	b.WriteString("## Installed tools\n\n")
	if len(commands) > 0 {
		writeToolEntries(b, commands)
	}
	if len(packages) > 0 {
		b.WriteString("Installed packages with no recorded command (`dpkg -L <package>` lists an apt package's files):\n\n")
		writeToolEntries(b, packages)
	}
}

type toolEntry struct{ name, version, kind string }

// writeToolEntries writes one line per name, sorted; a name listed twice is one PATH entry.
func writeToolEntries(b *strings.Builder, list []toolEntry) {
	slices.SortFunc(list, func(x, y toolEntry) int { return strings.Compare(x.name, y.name) })
	list = slices.CompactFunc(list, func(x, y toolEntry) bool { return x.name == y.name })
	for _, e := range list {
		b.WriteString("- " + e.name)
		if e.version != "" {
			b.WriteString(" " + e.version)
		}
		if e.kind != "" {
			b.WriteString(" (" + e.kind + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func writeMCP(b *strings.Builder, snap MCPSnapshot) {
	if len(snap.Servers) == 0 {
		return
	}
	servers := slices.Clone(snap.Servers)
	slices.SortFunc(servers, func(a, b marotte.MCPSnapshotServer) int {
		return strings.Compare(a.Name, b.Name)
	})

	b.WriteString("## Connected integrations\n\n")
	b.WriteString("External systems the user has connected via MCP. ")
	b.WriteString("Their tools are deferred: none is in your toolset until you ")
	b.WriteString("load it by id with tool_load, then call it with tool_call. ")
	b.WriteString("A workflow step whose agent profile does not set includeMcpJson ")
	b.WriteString("sees none of them, and reaches a Power's servers only through kiro_powers.\n\n")
	for _, s := range servers {
		fmt.Fprintf(b, "- **%s**\n", defuse(s.Name))
	}
	b.WriteString("\n")
}

// Git over HTTPS through marotte's own credential helper is the one authenticated path, so the
// section offers no forge CLI and must never claim no auth is needed.
func writeForges(w io.Writer, snap ForgeSnapshot) {
	if len(snap.Providers) == 0 {
		return
	}
	fmt.Fprintf(w, "## Connected forges\n\n")
	fmt.Fprintf(w, "Git over HTTPS (clone, push, pull, fetch) is authenticated for each connected forge by marotte's own git credential helper, which connecting the account in the Sources tab registers in `$HOME/.gitconfig`. Use plain `git` with an `https://` remote. SSH remotes are not covered: an `ssh://` or `git@host:` remote authenticates with whatever SSH key the user set up, if any.\n\n")
	fmt.Fprintf(w, "The helper answers with the connected account's own credential, so a push or fetch the forge refuses for a missing permission is that credential's limit. The user changes it by reconnecting the account in Sources with a token or sign-in that has the permission.\n\n")
	fmt.Fprintf(w, "marotte installs and signs in no forge CLI and exports no forge token to agent sessions. A forge CLI under Installed tools was installed by the user and carries only its own login, if any, which can belong to a different account.\n\n")
	for i := range snap.Providers {
		writeForgeProvider(w, &snap.Providers[i])
	}
}

// writeForgeProvider renders one connected forge: auth line, clone hint, capped repo list.
func writeForgeProvider(w io.Writer, p *ForgeProvider) {
	// Every field is a remote forge's report, so it is defused.
	user := cmp.Or(defuse(p.User), "(authenticated)")
	fmt.Fprintf(w, "### %s (%s)\n\n", defuse(p.Kind), defuse(p.Host))
	if p.Email != "" {
		fmt.Fprintf(w, "- Authenticated as: %s <%s>\n", user, defuse(p.Email))
	} else {
		fmt.Fprintf(w, "- Authenticated as: %s\n", user)
	}
	fmt.Fprintf(w, "- Clone via: `git clone https://%s/<owner>/<repo>.git`\n", defuse(p.Host))
	if len(p.Repos) > 0 {
		fmt.Fprintf(w, "- Accessible repositories:\n")
		n := min(len(p.Repos), 20)
		for _, r := range p.Repos[:n] {
			fmt.Fprintf(w, "  - %s\n", defuse(r))
		}
		if len(p.Repos) > 20 {
			fmt.Fprintf(w, "  - … and %d more\n", len(p.Repos)-20)
		}
	}
	fmt.Fprintln(w)
}
