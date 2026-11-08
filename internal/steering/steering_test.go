package steering

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestWriteTools(t *testing.T) {
	snap := ToolsSnapshot{
		Tools: []InstalledTool{
			{Version: "v0.30.0", Commands: []string{"goimports"}},
			{Version: "1.26.2", Commands: []string{"go", "gofmt"}},
			{Version: "14.1.1-1", Commands: []string{"rg"}, Apt: true},
			{Version: "2.0", Apt: true},
		},
		System: []string{"git", "curl"},
	}
	var b strings.Builder
	writeTools(&b, snap, true)

	want := "## Installed tools\n\n" +
		"Commands: go 1.26.2, gofmt 1.26.2, goimports v0.30.0, rg 14.1.1-1\n\n" +
		"Baked into the image: curl, git\n\n" +
		"The image is Debian slim: its base userland (coreutils, grep, sed, awk, find, xargs, diff, gzip) is present " +
		"and not listed here. Anything else not listed is likely absent (file, bc, xxd, netstat and ss are not installed). " +
		"Check with `command -v`, and install through the tools engine below.\n\n" +
		"1 more installed packages ship no command (mostly libraries): `dpkg -l` or `curl -s localhost:9847/api/tools` lists them.\n\n"
	if got := b.String(); got != want {
		t.Errorf("writeTools =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteTools_AnUnrecordedVersionIsLeftOut(t *testing.T) {
	var b strings.Builder
	writeTools(&b, ToolsSnapshot{Tools: []InstalledTool{{Commands: []string{"fd"}}}}, true)
	if got := lineWithPrefix(b.String(), "Commands: "); got != "Commands: fd" {
		t.Errorf("Commands line = %q, want no placeholder for the empty version", got)
	}
}

func TestWriteTools_ACommandTwoRowsShipIsListedOnce(t *testing.T) {
	snap := ToolsSnapshot{Tools: []InstalledTool{
		{Version: "5.9.0", Commands: []string{"tsc", "tsserver"}},
		{Version: "1.0.0", Commands: []string{"tsc"}},
	}}
	var b strings.Builder
	writeTools(&b, snap, true)
	if got := strings.Count(b.String(), "tsc "); got != 1 {
		t.Errorf("tsc listed %d times, want once: PATH holds one:\n%s", got, b.String())
	}
}

// The engine's bin dir precedes /usr/bin on PATH, so an engine row's copy is the one that runs.
func TestWriteTools_AnEngineRowsVersionWinsOverAnAptRows(t *testing.T) {
	snap := ToolsSnapshot{Tools: []InstalledTool{
		{Version: "1.0-1", Commands: []string{"node"}, Apt: true},
		{Version: "24.0.0", Commands: []string{"node"}},
	}}
	var b strings.Builder
	writeTools(&b, snap, true)
	if got := lineWithPrefix(b.String(), "Commands: "); got != "Commands: node 24.0.0" {
		t.Errorf("Commands line = %q, want the engine row's version", got)
	}
}

func TestWriteTools_AnInstalledUtilityIsNotCalledMissing(t *testing.T) {
	cases := []struct {
		name    string
		snap    ToolsSnapshot
		missing string
	}{{
		name:    "one installed through the engine",
		snap:    ToolsSnapshot{Tools: []InstalledTool{{Version: "1:5.46", Commands: []string{"file"}, Apt: true}}},
		missing: " (bc, xxd, netstat and ss are not installed). ",
	}, {
		name:    "one baked into the image",
		snap:    ToolsSnapshot{System: []string{"bc", "xxd", "netstat", "ss"}},
		missing: " (file is not installed). ",
	}, {
		name: "all of them",
		snap: ToolsSnapshot{
			Tools:  []InstalledTool{{Commands: []string{"file", "bc"}}, {Commands: []string{"xxd"}, Apt: true}},
			System: []string{"netstat", "ss"},
		},
		missing: ". ",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			writeTools(&b, tc.snap, true)
			want := "Anything else not listed is likely absent" + tc.missing + "Check with `command -v`"
			if !strings.Contains(b.String(), want) {
				t.Errorf("writeTools lacks %q:\n%s", want, b.String())
			}
		})
	}
}

func TestWriteTools_ABakedNameAnInstalledRowShipsIsListedOnce(t *testing.T) {
	snap := ToolsSnapshot{
		Tools:  []InstalledTool{{Version: "1:10.0", Commands: []string{"scp", "ssh"}, Apt: true}},
		System: []string{"git", "ssh"},
	}
	var b strings.Builder
	writeTools(&b, snap, true)
	if got := lineWithPrefix(b.String(), "Baked into the image: "); got != "Baked into the image: git" {
		t.Errorf("Baked line = %q, want ssh only under Commands", got)
	}
}

// Past the cap, every engine command is kept before an apt row's: the user chose each engine
// row, while an apt package also ships its auxiliaries.
func TestWriteTools_TheCommandsCapKeepsEngineCommandsFirst(t *testing.T) {
	tools := []InstalledTool{{Version: "14", Commands: []string{"aux-a", "aux-b"}, Apt: true}}
	tools = append(tools, engineRows(maxCommands, "tool")...)
	var b strings.Builder
	writeTools(&b, ToolsSnapshot{Tools: tools}, true)
	commands := lineWithPrefix(b.String(), "Commands: ")
	if strings.Contains(commands, "aux-") {
		t.Errorf("an apt row's command took a slot from an engine command: %q", commands)
	}
	if !strings.HasPrefix(commands, "Commands: tool000 1.0.0, ") || !strings.HasSuffix(commands, ", … and 2 more "+
		"(`command -v <name>` checks one; `curl -s localhost:9847/api/tools` lists every row's commands)") {
		t.Errorf("Commands line does not keep the engine commands sorted then count the 2 apt ones: %q", commands)
	}
}

func TestWriteMCP_EmptyServersEmitsNothing(t *testing.T) {
	var b strings.Builder
	writeMCP(&b, MCPSnapshot{})
	if b.Len() != 0 {
		t.Errorf("writeMCP(empty) wrote %d bytes, want 0", b.Len())
	}
}

func TestWriteMCP_SortsServersAlphabetically(t *testing.T) {
	var b strings.Builder
	writeMCP(&b, MCPSnapshot{
		Servers: []marotte.MCPSnapshotServer{
			{Name: "zed"},
			{Name: "alpha"},
			{Name: "linear"},
		},
	})
	out := b.String()
	if !strings.Contains(out, "## Connected integrations") {
		t.Error("missing header")
	}
	alphaIdx := strings.Index(out, "**alpha**")
	linearIdx := strings.Index(out, "**linear**")
	zedIdx := strings.Index(out, "**zed**")
	if alphaIdx < 0 || linearIdx < 0 || zedIdx < 0 {
		t.Fatalf("missing bullets: alpha=%d linear=%d zed=%d",
			alphaIdx, linearIdx, zedIdx)
	}
	if alphaIdx >= linearIdx || linearIdx >= zedIdx {
		t.Errorf("order alpha=%d linear=%d zed=%d, want alpha < linear < zed",
			alphaIdx, linearIdx, zedIdx)
	}
}

// TestWriteMCP_SaysTheToolsAreDeferred pins that the agent is told to load an MCP tool by id: telling it the
// tools are already on its belt makes it report a connected integration as missing. Both loaders are named
// because the disclosure arm is per session.
func TestWriteMCP_SaysTheToolsAreDeferred(t *testing.T) {
	var b strings.Builder
	writeMCP(&b, MCPSnapshot{Servers: []marotte.MCPSnapshotServer{{Name: "github"}}})
	out := b.String()
	for _, want := range []string{
		"deferred", "load_resource", "tool::<server>::<tool>", "tool_load", "tool_call",
		"includeMcpJson", "power::", "kiro_powers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeMCP() omits %q; output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "appear in your toolset") {
		t.Errorf("writeMCP() still says MCP tools are in the toolset; output:\n%s", out)
	}
}

func TestWriteMCP_InputSnapshotNotMutated(t *testing.T) {
	servers := []marotte.MCPSnapshotServer{{Name: "zed"}, {Name: "alpha"}}
	snap := MCPSnapshot{Servers: servers}

	var b strings.Builder
	writeMCP(&b, snap)

	if servers[0].Name != "zed" || servers[1].Name != "alpha" {
		t.Errorf("input slice was re-sorted: %v", servers)
	}
}

// TestWriteForges_PerProviderFields verifies a populated provider renders
// its email and accessible-repositories block, while a bare provider (no
// email, no repos) omits both.
func TestWriteForges_PerProviderFields(t *testing.T) {
	t.Run("populated provider renders email and repo list", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind:  "github",
			Host:  "github.com",
			User:  "alice",
			Email: "alice@example.com",
			Repos: []string{"acme/widget"},
		}}})
		out := b.String()
		if !strings.Contains(out, "alice@example.com") {
			t.Errorf("missing authenticated email:\n%s", out)
		}
		if !strings.Contains(out, "Accessible repositories") || !strings.Contains(out, "acme/widget") {
			t.Errorf("missing accessible-repositories block:\n%s", out)
		}
	})

	t.Run("bare provider omits email and repo list", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "gitea",
			Host: "git.example.com",
			User: "bob",
			// Email empty, Repos nil.
		}}})
		out := b.String()
		if strings.Contains(out, "bob <") {
			t.Errorf("rendered an empty <email> for a provider without an email:\n%s", out)
		}
		if strings.Contains(out, "Accessible repositories") {
			t.Errorf("rendered accessible-repositories header with zero repos:\n%s", out)
		}
	})
}

// TestGenerate_WritesCompleteSteeringFile drives the full Generate
// flow: reads tools-state.json, inspects workDir, uses the wired MCP
// snapshot, and writes ~/.kiro/steering/environment.md.
func TestGenerate_WritesCompleteSteeringFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	configDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(workDir, "myrepo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.SetMCPSnapshot(func() MCPSnapshot {
		return MCPSnapshot{Servers: []marotte.MCPSnapshotServer{{Name: "github"}}}
	})
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) {
		return ToolsSnapshot{Tools: []InstalledTool{{Version: "v0.30.0", Commands: []string{"goimports"}}}}, true
	})

	g.Generate(t.Context())

	steeringPath := filepath.Join(home, ".kiro", "steering", "environment.md")
	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatalf("steering file not written: %v", err)
	}
	got := string(out)
	wants := []string{
		"# Environment",
		"## Installed tools",
		"Commands: goimports v0.30.0",
		"## Container runtime",
		"## Tools engine",
		"## Connected integrations",
		"**github**",
		"## Workspace",
		"myrepo",
		"## Limitations",
		"## Capabilities",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("steering file missing %q", w)
		}
	}
	// The intended section order.
	last := -1
	for _, h := range []string{
		"## Installed tools", "## Container runtime", "## Tools engine",
		"## Connected integrations", "## Workspace", "## Limitations", "## Capabilities",
	} {
		i := strings.Index(got, h)
		if i < 0 {
			t.Fatalf("heading %q is absent, so its order cannot be checked", h)
		}
		if i < last {
			t.Errorf("heading %q at %d is out of order (previous heading at %d)", h, i, last)
		}
		last = i
	}
	// Chat-user sections ride the session door; in the file they would reach steps and the TUI.
	for _, chatOnly := range []string{
		"## Git panel", "## UI guide", "## Images and attachments", "## Chat capabilities", "Rewind",
	} {
		if strings.Contains(got, chatOnly) {
			t.Errorf("steering file carries the chat-only %q", chatOnly)
		}
	}
	// Both sentences are false: the agent sees an attached image, and nothing
	// provides a per-file undo.
	for _, retired := range []string{
		"every other attached file arrives as a workspace path",
		"checkpointed server-side",
	} {
		if strings.Contains(got, retired) {
			t.Errorf("steering file carries the retired sentence %q", retired)
		}
	}

	info, statErr := os.Stat(steeringPath)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("steering file mode = %o, want 0o600", mode)
	}
}

// TestChatDocs_CarriesTheChatSections pins the one always-included client doc:
// a fileMatch or manual doc would never load, and a section dropped in the move
// would reach no session at all.
func TestChatDocs_CarriesTheChatSections(t *testing.T) {
	g := New("/ws", "/cfg", "/cfg/tools")
	g.SetForgeSnapshot(func() ForgeSnapshot {
		return ForgeSnapshot{Providers: []ForgeProvider{{Kind: "github", Host: "github.com", User: "alice"}}}
	})
	docs := g.ChatDocs(t.Context())
	if len(docs) != 1 {
		t.Fatalf("ChatDocs() returned %d docs, want 1", len(docs))
	}
	doc := docs[0]
	if doc.Name != "marotte" || doc.Inclusion != inclusionAlways {
		t.Errorf("ChatDocs()[0] = {Name: %q, Inclusion: %q}, want {marotte, always}", doc.Name, doc.Inclusion)
	}
	for _, want := range []string{
		"## Git panel", "## UI guide", "## Images and attachments", "## Chat capabilities",
		"the account the Sources tab holds",
	} {
		if !strings.Contains(doc.Content, want) {
			t.Errorf("ChatDocs content missing %q", want)
		}
	}
	for _, fileOnly := range []string{"## Container runtime", "## Tools engine", "## Limitations"} {
		if strings.Contains(doc.Content, fileOnly) {
			t.Errorf("ChatDocs content carries the container-wide %q", fileOnly)
		}
	}
}

// TestChatDocs_WritesNothing pins that the chat doc never touches the Kiro home:
// a file there is read by every session on this HOME, which is the audience the
// split exists to exclude.
func TestChatDocs_WritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	New(t.TempDir(), t.TempDir(), t.TempDir()).ChatDocs(t.Context())
	entries, err := os.ReadDir(filepath.Join(home, ".kiro", "steering"))
	if err == nil && len(entries) > 0 {
		t.Errorf("ChatDocs wrote %d steering file(s); want none", len(entries))
	}
}

// TestGenerate_NoMCPSnapshotOmitsSection pins the "Connected
// integrations" omission behaviour when the snapshot callback is
// absent (the generator has no reason to emit an empty section).
func TestGenerate_NoMCPSnapshotOmitsSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	// Deliberately do NOT call SetMCPSnapshot.
	g.Generate(t.Context())

	out, _ := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if strings.Contains(string(out), "Connected integrations") {
		t.Error("MCP section emitted when no snapshot was wired")
	}
}

// TestGenerate_EmptyMCPSnapshotOmitsSection: a wired snapshot that
// returns zero servers must also skip the section — emitting a
// header with nothing under it is noise.
func TestGenerate_EmptyMCPSnapshotOmitsSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.SetMCPSnapshot(func() MCPSnapshot { return MCPSnapshot{} })
	g.Generate(t.Context())

	out, _ := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if strings.Contains(string(out), "Connected integrations") {
		t.Error("MCP section emitted for empty snapshot")
	}
}

// TestGenerate_RendersForgeSection verifies a wired forge snapshot renders
// the "## Connected forges" section with the provider host and user.
func TestGenerate_RendersForgeSection(t *testing.T) {
	steeringPath := setupKiroHome(t)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.SetForgeSnapshot(func() ForgeSnapshot {
		return ForgeSnapshot{Providers: []ForgeProvider{
			{Kind: "github", Host: "github.com", User: "alice"},
		}}
	})
	g.Generate(t.Context())

	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatalf("steering file not written: %v", err)
	}
	got := string(out)
	for _, want := range []string{"## Connected forges", "github.com", "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("Generate with forge snapshot omitted %q\n--- output ---\n%s", want, got)
		}
	}
}

// TestChatDocs_GitPanelFollowsTheForgeSnapshot pins the git panel's closing paragraph: the
// connected-account one with a provider, the Sources pointer without.
func TestChatDocs_GitPanelFollowsTheForgeSnapshot(t *testing.T) {
	const connected, none = "the account the Sources tab holds", "No forge account is connected"
	for _, tc := range []struct {
		name      string
		providers []ForgeProvider
		want, not string
	}{
		{"one provider", []ForgeProvider{{Kind: "gitlab", Host: "gitlab.com", User: "alice"}}, connected, none},
		{"no provider", nil, none, connected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New(t.TempDir(), t.TempDir(), t.TempDir())
			g.SetForgeSnapshot(func() ForgeSnapshot { return ForgeSnapshot{Providers: tc.providers} })
			docs := g.ChatDocs(t.Context())
			if len(docs) != 1 {
				t.Fatalf("ChatDocs(%s) returned %d docs, want 1", tc.name, len(docs))
			}
			if got := docs[0].Content; !strings.Contains(got, tc.want) || strings.Contains(got, tc.not) {
				t.Errorf("ChatDocs(%s) git panel: want %q and not %q\n--- output ---\n%s", tc.name, tc.want, tc.not, got)
			}
		})
	}
}

// TestGenerate_IdempotentSkipsRewrite pins that identical inputs do not rewrite the file. A
// far-past mtime is stamped first so coarse mtime granularity cannot hide a rewrite.
func TestGenerate_IdempotentSkipsRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.Generate(t.Context())

	steeringPath := filepath.Join(home, ".kiro", "steering", "environment.md")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(steeringPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	g.Generate(t.Context())

	info, err := os.Stat(steeringPath)
	if err != nil {
		t.Fatalf("stat after second Generate: %v", err)
	}
	if cutoff := time.Now().Add(-30 * time.Minute); !info.ModTime().Before(cutoff) {
		t.Errorf("second Generate rewrote unchanged file: mtime %v is not before %v (skip-write guard broken)",
			info.ModTime(), cutoff)
	}
}

// TestGenerate_LogsWroteOnSuccess verifies a successful write logs the
// "steering: wrote" Info line.
func TestGenerate_LogsWroteOnSuccess(t *testing.T) {
	steeringPath := setupKiroHome(t)
	workDir := t.TempDir()
	configDir := t.TempDir()

	var buf bytes.Buffer
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.Generate(t.Context())

	if _, err := os.Stat(steeringPath); err != nil {
		t.Fatalf("Generate did not write file: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "steering: wrote") {
		t.Errorf("successful Generate did not log the success line; logs:\n%s", logs)
	}
}

// TestGenerate_ConcurrentCallsSerialise fans out Generate and SetMCPSnapshot calls; under -race
// it fails if the locking is weakened, and the file must be one coherent document.
func TestGenerate_ConcurrentCallsSerialise(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir, filepath.Join(configDir, "tools"))
	g.SetMCPSnapshot(func() MCPSnapshot {
		return MCPSnapshot{Servers: []marotte.MCPSnapshotServer{{Name: "github"}}}
	})
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) {
		return ToolsSnapshot{Tools: []InstalledTool{{Version: "v0.30.0", Commands: []string{"goimports"}}}}, true
	})

	const n = 16
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { g.Generate(t.Context()) })
		wg.Go(func() {
			g.SetMCPSnapshot(func() MCPSnapshot {
				return MCPSnapshot{Servers: []marotte.MCPSnapshotServer{{Name: "github"}}}
			})
		})
	}
	wg.Wait()

	got, err := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if err != nil {
		t.Fatalf("steering file missing after concurrent Generate: %v", err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "# Environment\n") {
		t.Errorf("steering file header corrupted under concurrency: %.80q", s)
	}
	if !strings.Contains(s, "## Capabilities") {
		t.Errorf("steering file truncated under concurrency; no Capabilities section:\n%s", s)
	}
}

func TestGenerate_AnOlderGenerationNeverOverwritesANewerOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := New(t.TempDir(), t.TempDir(), "")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return ToolsSnapshot{Tools: []InstalledTool{{Version: "1", Commands: []string{"old"}}}}, true
		}
		return ToolsSnapshot{Tools: []InstalledTool{{Version: "2", Commands: []string{"new"}}}}, true
	})

	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() {
		g.Generate(t.Context())
		close(firstDone)
	}()
	<-entered
	go func() {
		g.Generate(t.Context())
		close(secondDone)
	}()
	// The second generation either waits for the first (correct) or finishes while the first is
	// held in its snapshot; either way the first is released only after this window.
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-firstDone
	<-secondDone

	got, err := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Commands: new 2\n") || strings.Contains(string(got), "old 1") {
		t.Errorf("environment.md holds the older generation's tools, not the newer one's:\n%s", got)
	}
}

func TestGenerate_ACancelledContextRunsNoSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := New(t.TempDir(), t.TempDir(), "")
	var calls atomic.Int32
	g.SetMCPSnapshot(func() MCPSnapshot { calls.Add(1); return MCPSnapshot{} })
	g.SetForgeSnapshot(func() ForgeSnapshot { calls.Add(1); return ForgeSnapshot{} })
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) { calls.Add(1); return ToolsSnapshot{}, true })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := g.Generate(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled Generate returned %v, want its ctx's error", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a cancelled Generate ran %d snapshot callbacks, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "environment.md")); !os.IsNotExist(err) {
		t.Errorf("a cancelled Generate wrote environment.md (stat error %v)", err)
	}
}

func TestGenerate_CancelledDuringItsSnapshotsRendersNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var buf bytes.Buffer
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	configDir := t.TempDir()
	// A render would log this file's parse error, so the ERROR check sees a render that ran.
	mustWriteFile(t, filepath.Join(configDir, "tools-state.json"), "{")
	g := New(t.TempDir(), configDir, "")
	ctx, cancel := context.WithCancel(t.Context())
	g.SetToolsSnapshot(func() (ToolsSnapshot, bool) { cancel(); return ToolsSnapshot{}, true })

	err := g.Generate(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Generate cancelled during its snapshots returned %v, want its ctx's error", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "environment.md")); !os.IsNotExist(err) {
		t.Errorf("Generate wrote environment.md after its ctx was cancelled (stat error %v)", err)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("Generate rendered after its ctx was cancelled:\n%s", buf.String())
	}
}

// cancelAfterChecks is a context whose first checks pass, then reads as cancelled.
type cancelAfterChecks struct {
	context.Context
	passing atomic.Int32
	done    chan struct{}
	once    sync.Once
}

func newCancelAfterChecks(parent context.Context, passing int32) *cancelAfterChecks {
	c := &cancelAfterChecks{Context: parent, done: make(chan struct{})}
	c.passing.Store(passing)
	return c
}

func (c *cancelAfterChecks) Err() error {
	if c.passing.Add(-1) >= 0 {
		return nil
	}
	c.once.Do(func() { close(c.done) })
	return context.Canceled
}

func (c *cancelAfterChecks) Done() <-chan struct{} { return c.done }

// An unchanged file skips the write, so only the check after the render returns the ctx's error.
func TestGenerate_CancelledDuringItsRenderReturnsTheCtxError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := New(t.TempDir(), t.TempDir(), "")
	if err := g.Generate(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".kiro", "steering", "environment.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Generate checks its ctx twice before render, so the cancellation lands during render.
	err = g.Generate(newCancelAfterChecks(t.Context(), 2))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Generate cancelled during its render returned %v, want its ctx's error", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
		t.Errorf("Generate cancelled during its render rewrote environment.md:\n%s", after)
	}
}

func TestGenerate_ReturnsAFailedWriteWhileItsCtxIsLive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var buf bytes.Buffer
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	mustWriteFile(t, filepath.Join(home, ".kiro"), "not a folder")
	g := New(t.TempDir(), t.TempDir(), "")

	err := g.Generate(t.Context())

	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("Generate returned %v for a write under a file, want ENOTDIR", err)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("Generate logged the failure it returns:\n%s", buf.String())
	}
}

// cancelAfterTheWritesCheck reads as live through filesafe's first ctx check, then as cancelled:
// a shutdown landing between that check and a failing write.
type cancelAfterTheWritesCheck struct {
	context.Context
	checked atomic.Bool
}

func (c *cancelAfterTheWritesCheck) Err() error {
	if c.checked.Load() {
		return context.Canceled
	}
	pc := make([]uintptr, 1)
	if runtime.Callers(2, pc) == 1 {
		frame, _ := runtime.CallersFrames(pc).Next()
		c.checked.Store(strings.HasPrefix(frame.Function, "github.com/cplieger/go/filesafe."))
	}
	return nil
}

func TestGenerate_ReturnsAFailedWriteThatRacesACancel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".kiro"), "not a folder")
	g := New(t.TempDir(), t.TempDir(), "")
	ctx := &cancelAfterTheWritesCheck{Context: t.Context()}

	err := g.Generate(ctx)

	if !ctx.checked.Load() {
		t.Fatal("filesafe never checked the ctx, so no cancellation raced the write")
	}
	if !errors.Is(err, syscall.ENOTDIR) || errors.Is(err, context.Canceled) {
		t.Errorf("Generate whose write failed as its ctx was cancelled returned %v, want the write's ENOTDIR", err)
	}
}

func TestCustomPath_UsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := New("/some/work", "/some/config", "/some/config/tools")
	got := g.CustomPath()
	want := filepath.Join(home, ".kiro", "steering", "custom.md")
	if got != want {
		t.Errorf("CustomPath() = %q, want %q", got, want)
	}
}

func TestCustomPath_HomeUnsetFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("KIRO_HOME", "")
	os.Unsetenv("HOME")
	os.Unsetenv("KIRO_HOME")
	g := New("/some/work", "/some/config", "/some/config/tools")
	// With KIRO_HOME and HOME unset, KiroHome() falls back to ".kiro", as kiro-cli does.
	if got := g.CustomPath(); got != ".kiro/steering/custom.md" {
		t.Errorf("CustomPath() with no HOME = %q, want \".kiro/steering/custom.md\"", got)
	}
}

// TestWriteForges_RepoListTruncatesAtTwenty pins the `len(p.Repos) > 20` boundary: 20 repos
// list with no overflow line, 21 list 20 plus "… and 1 more".
func TestWriteForges_RepoListTruncatesAtTwenty(t *testing.T) {
	mkRepos := func(n int) []string {
		repos := make([]string, n)
		for i := range repos {
			repos[i] = fmt.Sprintf("acme/repo-%02d", i)
		}
		return repos
	}
	t.Run("exactly twenty repos: no overflow line", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "github", Host: "github.com", User: "alice", Repos: mkRepos(20),
		}}})
		out := b.String()
		if strings.Contains(out, "… and") {
			t.Errorf("20 repos rendered an overflow line, want none:\n%s", out)
		}
		if !strings.Contains(out, "acme/repo-19") {
			t.Errorf("20 repos: the 20th repo must still be listed:\n%s", out)
		}
	})
	t.Run("twenty-one repos: twenty listed plus one summarised", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "github", Host: "github.com", User: "alice", Repos: mkRepos(21),
		}}})
		out := b.String()
		if !strings.Contains(out, "… and 1 more") {
			t.Errorf("21 repos: want an \"… and 1 more\" overflow line:\n%s", out)
		}
		if strings.Contains(out, "acme/repo-20") {
			t.Errorf("21 repos: the 21st repo must be summarised, not listed inline:\n%s", out)
		}
	})
}

var connectedEverywhere = ForgeSnapshot{Providers: []ForgeProvider{
	{Kind: "github", Host: "github.com", User: "alice"},
	{Kind: "gitlab", Host: "gitlab.com", User: "alice"},
	{Kind: "gitea", Host: "git.example.com", User: "alice"},
	{Kind: "codeberg", Host: "codeberg.org", User: "alice"},
}}

// TestWriteForges_ClaimsNoForgeCLI pins the forge section to the one path
// Marotte authenticates for the agent: git over HTTPS through its own helper.
// No forge CLI is offered as a way in, for any connected kind.
func TestWriteForges_ClaimsNoForgeCLI(t *testing.T) {
	var b strings.Builder
	writeForges(&b, connectedEverywhere)
	out := b.String()
	if cli := forgeCLIName.FindString(out); cli != "" {
		t.Errorf("writeForges(every kind) names the forge CLI %q:\n%s", cli, out)
	}
	for _, want := range []string{
		"authenticated for each connected forge by marotte's own git credential helper",
		"marotte installs and signs in no forge CLI and exports no forge token to agent sessions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeForges(every kind) output missing %q:\n%s", want, out)
		}
	}
}

// TestWriteForges_StatesSSHIsNotCovered pins the boundary of the helper: an
// SSH remote never reaches it, so the agent must not expect one to push.
func TestWriteForges_StatesSSHIsNotCovered(t *testing.T) {
	var b strings.Builder
	writeForges(&b, connectedEverywhere)
	out := b.String()
	if !strings.Contains(out, "SSH remotes are not covered") {
		t.Errorf("writeForges(every kind) does not state that SSH remotes are not covered:\n%s", out)
	}
}

// TestWriteForges_ARefusalIsTheConnectedCredentialsLimit pins the remedy the
// agent is given for a refused push on every kind, and keeps out the
// unqualified sentence that once told it there was no boundary to worry about.
func TestWriteForges_ARefusalIsTheConnectedCredentialsLimit(t *testing.T) {
	const retired = "no auth login or token setup needed"
	for _, p := range connectedEverywhere.Providers {
		t.Run(p.Kind, func(t *testing.T) {
			var b strings.Builder
			writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{p}})
			out := b.String()
			if !strings.Contains(out, "reconnecting the account in Sources") {
				t.Errorf("writeForges(%s) does not name reconnecting in Sources as the remedy:\n%s", p.Kind, out)
			}
			if strings.Contains(out, retired) {
				t.Errorf("writeForges(%s) brought back %q:\n%s", p.Kind, retired, out)
			}
		})
	}
}
