// Package steering generates the steering kiro-cli reads, in two outputs. environment.md
// holds container-wide facts and is read by every session on this HOME. ChatDocs renders the
// chat-only guidance a chat bridge's session door carries. Both run synchronously on the
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

// Generator produces steering files for kiro-cli.
type Generator struct {
	mcpSnapshot   func() MCPSnapshot
	forgeSnapshot func() ForgeSnapshot
	workDir       string
	configDir     string
	mu            sync.Mutex
}

// New returns a Generator that writes steering files for the given workDir and configDir.
func New(workDir, configDir string) *Generator {
	return &Generator{workDir: workDir, configDir: configDir}
}

// SetMCPSnapshot wires a snapshot callback (unset omits the MCP section). The callback runs
// OUTSIDE the generator's mutex, so it may take agent locks.
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

// Generate renders environment.md and writes it atomically, holding g.mu across the write so
// calls serialise, and skips a byte-identical write (MCP event storms regenerate often).
func (g *Generator) Generate(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Read the callback under g.mu, call it outside (see SetMCPSnapshot).
	snapshotFn := g.mcpSnapshot
	forgeFn := g.forgeSnapshot
	var mcp MCPSnapshot
	var forges ForgeSnapshot
	if snapshotFn != nil || forgeFn != nil {
		g.mu.Unlock()
		if snapshotFn != nil {
			mcp = snapshotFn()
		}
		if forgeFn != nil {
			forges = forgeFn()
		}
		g.mu.Lock()
	}

	content := g.render(ctx, mcp, snapshotFn != nil, forges, forgeFn != nil)
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

// render produces the whole document for the given snapshots. hasMCP and
// hasForges say whether a snapshot callback was wired at all, which is what
// decides whether the MCP and forge sections appear.
func (g *Generator) render(ctx context.Context, mcp MCPSnapshot, hasMCP bool, forges ForgeSnapshot, hasForges bool) []byte {
	var b strings.Builder
	writeIntro(&b, g.workDir)

	// tools-state.json is what is INSTALLED (tools.json is intent).
	state := filepath.Join(g.configDir, "tools-state.json")
	if data, err := readCappedFile(state, toolsManifestCap); err == nil {
		writeTools(&b, data)
	}
	writeRuntime(&b, g.configDir)
	writeToolsEngine(&b, g.configDir, g.workDir)
	if hasMCP {
		writeMCP(&b, mcp)
	}
	if hasForges {
		writeForges(&b, forges)
	}
	writeWorkspace(ctx, &b, g.workDir)
	writeMemory(ctx, &b, g.workDir, g.configDir)
	writeLimitations(&b)
	writeCapabilities(&b, g.configDir)
	return []byte(b.String())
}

// chatSteeringName is the client steering document's name on the session door.
const chatSteeringName = "marotte"

// ChatDocs renders the chat-only steering a chat bridge sends on the session door, writing
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

// renderChat produces the chat-only document.
func (g *Generator) renderChat(forges ForgeSnapshot) string {
	var b strings.Builder
	b.WriteString("# Marotte chat\n\n")
	b.WriteString("This session is a chat in marotte, the browser app the user is looking at. ")
	b.WriteString("These sections describe what the user sees; the environment steering doc ")
	b.WriteString("covers the container.\n\n")
	writeGitPanel(&b, g.workDir, len(forges.Providers) > 0)
	writeUIGuide(&b)
	writeAttachments(&b, marotte.DefaultUploadDir, g.workDir)
	writeChatCapabilities(&b)
	return b.String()
}

// readCappedFile reads at most limit bytes from path, for untrusted workspace input; errors
// are log-and-omit. It opens with atomicfile.OpenRegular: a FIFO would hang Generate, which
// runs synchronously before every bridge spawn, and a symlinked README would copy whatever it
// names (an MCP credential, measured) into authoritative agent context. Truncation is kept:
// only the head matters.
func readCappedFile(path string, limit int64) ([]byte, error) {
	f, _, err := atomicfile.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// CustomPath returns the path to the custom.md steering file in the kiro home directory.
func (g *Generator) CustomPath() string {
	return workspace.KiroSteeringPath("custom.md")
}

// toolState mirrors the tools engine's per-tool machine state subset
// this generator consumes (the toolbelt engine's ToolStatus).
type toolState struct {
	InstalledVersion string   `json:"installed_version"`
	Bins             []string `json:"bins,omitempty"`
	PMBins           []string `json:"pm_bins,omitempty"`
}

func writeTools(b *strings.Builder, data []byte) {
	var state struct {
		Tools map[string]toolState `json:"tools"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Error("steering: parse tools-state.json", "error", err)
		return
	}
	type tool struct{ name, version string }
	var all []tool
	for name, st := range state.Tools {
		if st.InstalledVersion == "" {
			continue
		}
		bins := slices.Concat(st.Bins, st.PMBins)
		if len(bins) == 0 {
			bins = []string{name}
		}
		// Defused: names and versions come from the catalog, hand edits and --version output.
		for _, bin := range bins {
			all = append(all, tool{defuse(bin), defuse(st.InstalledVersion)})
		}
	}
	if len(all) == 0 {
		return
	}
	slices.SortFunc(all, func(a, b tool) int { return strings.Compare(a.name, b.name) })
	all = slices.CompactFunc(all, func(a, b tool) bool { return a.name == b.name })
	b.WriteString("## Installed tools\n\n")
	for _, t := range all {
		fmt.Fprintf(b, "- %s %s\n", t.name, t.version)
	}
	b.WriteString("\n")
}

// writeMCP emits the "Connected integrations" section listing every
// currently-connected MCP server.
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
	b.WriteString("Their tools appear in your toolset with the server name ")
	b.WriteString("embedded in the tool name.\n\n")
	for _, s := range servers {
		fmt.Fprintf(b, "- **%s**\n", defuse(s.Name))
	}
	b.WriteString("\n")
}

// writeForges renders the connected forge providers. Git over HTTPS through marotte's own
// credential helper is the one authenticated path, so the section offers no forge CLI and
// must never claim no auth is needed.
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
