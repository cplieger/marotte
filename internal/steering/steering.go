// Package steering generates the steering kiro-cli reads, in two outputs. environment.md
// holds container-wide facts and is read by every session on this HOME. ChatDocs renders the
// chat-only guidance a chat session's door carries. Both run synchronously on the
// session-start path, so they stay network-free: slow data arrives through snapshot callbacks.
package steering

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/workspace"
)

const (
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
	Repos []string // repo names the forge reported, possibly only its first page
}

// ToolsSnapshot is what the tools engine reports installed.
type ToolsSnapshot struct {
	Tools  []InstalledTool
	System []string // image-baked commands present on this host
}

// InstalledTool is one tools-engine row the engine's live probe reports installed.
type InstalledTool struct {
	Version  string   // the installed version, "" when none is recorded
	Commands []string // what the row puts on PATH; none for a library package
	Apt      bool     // a Debian package, whose commands can include its auxiliaries
}

// Caps on lists that grow with the user's setup. Each sits well above what one developer's
// typical setup reaches, so only a heavy setup sees a cut, and a cut list ends in a count.
const (
	maxCommands       = 120
	maxMCPServers     = 30
	maxPlainRepos     = 60
	maxKiroRepos      = 10
	maxRouting        = 40
	maxDescWords      = 20
	maxDescBytes      = 300
	maxFileMatchBytes = 200
	maxDirectories    = 30
	maxGrantedDirs    = 20
	maxMemoryTags     = 30
	maxForgeAccounts  = 5
	maxForgeRepos     = 20
)

// Generator produces steering files for kiro-cli.
type Generator struct {
	mcpSnapshot   func() MCPSnapshot
	forgeSnapshot func() ForgeSnapshot
	toolsSnapshot func() (ToolsSnapshot, bool)
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
// tools engine is down or its inventory failed. Unset or not ok lists none.
func (g *Generator) SetToolsSnapshot(fn func() (tools ToolsSnapshot, ok bool)) {
	g.mu.Lock()
	g.toolsSnapshot = fn
	g.mu.Unlock()
}

// Generate renders environment.md and writes it atomically, and skips a byte-identical write (MCP
// event storms regenerate often). Calls serialise: each one's snapshots, render and write finish
// before the next call takes its snapshots. The snapshot callbacks run under that serialisation
// alone, so one must never wait on a goroutine that is calling Generate. A cancelled ctx stops a
// call before its snapshots, its render and its write, returning the wrapped ctx.Err(). Generate
// logs no failure: it returns each one, and the caller judges it.
func (g *Generator) Generate(ctx context.Context) error {
	g.genMu.Lock()
	defer g.genMu.Unlock()
	if err := stopped(ctx); err != nil {
		return err
	}

	g.mu.Lock()
	snapshotFn := g.mcpSnapshot
	forgeFn := g.forgeSnapshot
	toolsFn := g.toolsSnapshot
	g.mu.Unlock()
	var mcp MCPSnapshot
	var forges ForgeSnapshot
	var tools ToolsSnapshot
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
	if err := stopped(ctx); err != nil {
		return err
	}

	content := g.render(ctx, mcp, snapshotFn != nil, forges, forgeFn != nil, tools, toolsOK)
	if err := stopped(ctx); err != nil {
		return err
	}
	steeringFile := workspace.KiroSteeringPath("environment.md")

	// readCappedFile, not os.ReadFile: a FIFO or symlink may stand here (invariant 6). Any error
	// falls through to the write, which replaces a bogus file.
	if existing, readErr := readCappedFile(steeringFile, steeringCompareCap); readErr == nil && bytes.Equal(existing, content) {
		slog.Debug("steering: content unchanged, skipping write", "path", steeringFile)
		return nil
	}

	// 0600 for a file listing the workspace layout and MCP server names; WithMkdirMode creates the
	// parent narrowly (MkdirAll would widen it to 0755).
	if _, err := filesafe.WriteFile(ctx, steeringFile, content,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("steering: write %s: %w", steeringFile, err)
	}
	slog.Info("steering: wrote", "path", steeringFile, "bytes", len(content))
	return nil
}

func stopped(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("steering: generate stopped: %w", err)
	}
	return nil
}

// hasMCP and hasForges say whether a snapshot callback was wired at all, which decides whether the
// MCP and forge sections appear; toolsOK says the tools snapshot answered.
func (g *Generator) render(ctx context.Context, mcp MCPSnapshot, hasMCP bool, forges ForgeSnapshot, hasForges bool,
	tools ToolsSnapshot, toolsOK bool,
) []byte {
	var b strings.Builder
	writeIntro(&b, g.workDir)
	writeTools(&b, tools, toolsOK)
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
	writeAdditionalDirs(&b, granted, g.configDir)
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

// Errors are log-and-omit. It opens with filesafe.OpenRegular: a FIFO would hang Generate, which
// runs synchronously before every session door, and a symlinked steering doc would copy whatever it names
// (an MCP credential, measured) into authoritative agent context. Truncation is kept: only the head
// matters.
func readCappedFile(path string, limit int64) ([]byte, error) {
	f, _, err := filesafe.OpenRegular(path)
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

// writeTools lists commands, not packages: an apt package's commands need not match its name
// (ripgrep ships rg). With ok false the engine did not answer: no snapshot command is listed, and
// the base-userland sentence still says that userland is present.
func writeTools(b *strings.Builder, snap ToolsSnapshot, ok bool) {
	b.WriteString("## Installed tools\n\n")
	if !ok {
		b.WriteString(debianBase + " The tools engine did not answer, so the commands installed or baked in are not " +
			"listed: check with `command -v`, and install through the tools engine below.\n\n")
		return
	}
	cmds, libraries := listCommands(snap.Tools)
	present := map[string]bool{}
	for _, c := range cmds {
		present[c.name] = true
	}
	if len(cmds) > 0 {
		writeCommands(b, cmds)
	}
	var baked []string
	for _, n := range defusedSorted(snap.System) {
		if !present[n] {
			baked = append(baked, n)
		}
		present[n] = true
	}
	if len(baked) > 0 {
		b.WriteString("Baked into the image: " + strings.Join(baked, ", ") + "\n\n")
	}
	writeAbsentLine(b, present)
	if libraries > 0 {
		fmt.Fprintf(b, "%d more installed packages ship no command (mostly libraries): ", libraries)
		b.WriteString("`dpkg -l` or `curl -s localhost:9847/api/tools` lists them.\n\n")
	}
}

// writeAbsentLine names the utilities the image does not ship, leaving out any that present holds.
func writeAbsentLine(b *strings.Builder, present map[string]bool) {
	missing := slices.DeleteFunc(slices.Clone(notShipped), func(n string) bool { return present[n] })
	b.WriteString(debianBase)
	switch n := len(missing); n {
	case 0:
	case 1:
		b.WriteString(" The image does not ship " + missing[0] + ".")
	default:
		b.WriteString(" The image does not ship " + strings.Join(missing[:n-1], ", ") + " or " + missing[n-1] + ".")
	}
	b.WriteString(" Check any other command with `command -v`, and install through the tools engine below.\n\n")
}

// debianBase names the Debian essential and required-priority userland a debian:*-slim image
// ships, which no inventory reports.
const debianBase = "The image is Debian slim: its base userland (coreutils, util-linux, findutils, grep, sed, awk, " +
	"diff, gzip, which, perl, dpkg and apt-get) is present and not listed here."

// notShipped are utilities agents reach for that the slim image lacks; one the user installed
// is not named.
var notShipped = []string{"file", "bc", "xxd", "netstat", "ss"}

type command struct{ name, version string }

// listCommands returns each defused command once, an engine row's before an apt row's: the
// engine's bin dir precedes /usr/bin on PATH, so its copy is the one that runs. It also counts
// the rows that ship none.
func listCommands(tools []InstalledTool) (cmds []command, libraries int) {
	seen := map[string]bool{}
	for _, apt := range []bool{false, true} {
		for _, t := range tools {
			if t.Apt != apt {
				continue
			}
			if len(t.Commands) == 0 {
				libraries++
			}
			cmds = appendNewCommands(cmds, seen, t)
		}
	}
	return cmds, libraries
}

// appendNewCommands appends t's commands that seen lacks. Defused: names and versions come from
// the catalog, dpkg and --version output.
func appendNewCommands(cmds []command, seen map[string]bool, t InstalledTool) []command {
	for _, name := range defusedSorted(t.Commands) {
		if !seen[name] {
			seen[name] = true
			cmds = append(cmds, command{name: name, version: defuse(t.Version)})
		}
	}
	return cmds
}

// writeCommands fills the cap in listCommands' order, engine commands first: the user picked each
// engine row, while an apt package also ships auxiliaries (gcc's gcov-dump).
func writeCommands(b *strings.Builder, cmds []command) {
	kept := slices.Clone(cmds[:min(len(cmds), maxCommands)])
	slices.SortFunc(kept, func(x, y command) int { return strings.Compare(x.name, y.name) })
	entries := make([]string, 0, len(kept))
	for _, c := range kept {
		entries = append(entries, strings.TrimSpace(c.name+" "+c.version))
	}
	b.WriteString("Commands: " + strings.Join(entries, ", "))
	if extra := len(cmds) - maxCommands; extra > 0 {
		fmt.Fprintf(b, ", … and %d more (`command -v <name>` checks one; `curl -s localhost:9847/api/tools` "+
			"lists every row's commands)", extra)
	}
	b.WriteString("\n\n")
}

// defusedSorted defuses each name, drops the empty ones and returns the rest sorted and deduped.
func defusedSorted(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if d := defuse(n); d != "" {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
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
	// The loader depends on the session's disclosure arm, which no frame carries and this
	// HOME-wide file cannot know, so both are named.
	b.WriteString("Their tools are deferred: none is in your toolset until you load it by id, ")
	b.WriteString("then call it with tool_call. The loader is load_resource (ids tool::<server>::<tool>) ")
	b.WriteString("or, in a session without it, tool_load. ")
	b.WriteString("A workflow step whose agent profile does not set includeMcpJson sees none of them, ")
	b.WriteString("and reaches a Power's servers only through the Power: its power:: ids in load_resource, or kiro_powers.\n\n")
	for _, s := range servers[:min(len(servers), maxMCPServers)] {
		fmt.Fprintf(b, "- **%s**\n", defuse(s.Name))
	}
	if extra := len(servers) - maxMCPServers; extra > 0 {
		fmt.Fprintf(b, "- … and %d more (the load_resource catalog lists them)\n", extra)
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
	for i := range snap.Providers[:min(len(snap.Providers), maxForgeAccounts)] {
		writeForgeProvider(w, &snap.Providers[i])
	}
	if extra := len(snap.Providers) - maxForgeAccounts; extra > 0 {
		fmt.Fprintf(w, "… and %d more connected accounts, authenticated the same way (the Sources tab lists them).\n\n", extra)
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
		for _, r := range p.Repos[:min(len(p.Repos), maxForgeRepos)] {
			fmt.Fprintf(w, "  - %s\n", defuse(r))
		}
		if extra := len(p.Repos) - maxForgeRepos; extra > 0 {
			fmt.Fprintf(w, "  - … and at least %d more (the Sources tab lists them)\n", extra)
		}
	}
	fmt.Fprintln(w)
}
