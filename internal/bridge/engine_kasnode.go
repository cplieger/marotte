package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/kasnode"
	"github.com/cplieger/marotte/internal/workspace"
)

// selfExecutable is the launcher KIRO_KAS_NODE_PATH names: marotte's own binary, which main re-executes as node
// (kasnode.Exec). A var for tests.
var selfExecutable = os.Executable

// nodeVersionBudget bounds `node --version`, which answers in well under a second.
const nodeVersionBudget = 10 * time.Second

// cliNodeHashes caches kasnode.EmbeddedNodeHash per kiro-cli path: an installed version's binary never changes
// in place, and the scan reads about 220 MB.
var cliNodeHashes = struct {
	m map[string]string
	sync.Mutex
}{m: map[string]string{}}

// withKASNodeLauncher adds the high throughput launcher to env when the setting is on and every check passes, and
// returns the node flags the launch adds (nil without the launcher). A failed check starts kiro-cli's stock engine
// with one WARN naming why; the next start checks again.
func (e *Engine) withKASNodeLauncher(env []string) (out, nodeArgs []string) {
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, kasnode.LaunchVar+"=") })
	if !e.highThroughput {
		return env, nil
	}
	launcher, node, reason := e.kasNodeLaunch(env)
	if reason != "" {
		slog.Warn("engine: starting without the high throughput optimizations",
			"generation", e.generation, "reason", reason)
		return env, nil
	}
	return append(env, kasnode.NodePathVar+"="+launcher, kasnode.LaunchVar+"="+node), slices.Clone(kasnode.Flags)
}

// kasNodeLaunch returns the launcher and the node it runs, or why the launcher must not be used. The node must be
// the one kiro-cli extracted for the active kiro-cli (the hash embedded in the binary agrees with the file, and so
// does node.sha256 in the data dir, where it is the extraction marker) and report kasnode.ValidatedNodeVersion: in
// the data dir kiro-cli extracts nothing while KIRO_KAS_NODE_PATH is set, so a bump that ships a new node would
// otherwise keep running the old one. The node is found as kiro-cli finds it (workspace.KASLookup): the sole
// complete entry under an absolute KIRO_RUNTIME_DIR, else the data dir. A start prunes the runtime dir to the
// running version, so after a bump the sole entry is the old version's until that start, whose hash then fails.
func (e *Engine) kasNodeLaunch(env []string) (launcher, node, reason string) {
	if v, ok := envValue(env, kasnode.NodePathVar); ok {
		return "", "", fmt.Sprintf("%s is already set to %q in marotte's environment", kasnode.NodePathVar, v)
	}
	lookup := kasLookup(env)
	dataNode := kasnode.EmbeddedNodePath(lookup.DataHome, lookup.Home)
	if dataNode == "" && !filepath.IsAbs(lookup.RuntimeDir) {
		return "", "", "neither KIRO_RUNTIME_DIR, HOME nor XDG_DATA_HOME is set, so kiro-cli's node cannot be located"
	}
	want, err := cliNodeHash(e.cliPath)
	if err != nil {
		return "", "", fmt.Sprintf("cannot read the node hash in %s: %v", e.cliPath, err)
	}
	node = lookup.Find("").Node
	if node == "" {
		return "", "", "kiro-cli has not extracted its node yet; the next engine start checks again"
	}
	// Only the data-dir node has a node.sha256; a runtime-dir entry is checked by its file hash alone.
	recorded, hashes := want, ""
	if node == dataNode {
		b, readErr := os.ReadFile(node + ".sha256")
		if readErr != nil {
			return "", "", fmt.Sprintf("kiro-cli has not extracted its node yet (%v); the next engine start checks again", readErr)
		}
		recorded = strings.TrimSpace(string(b))
		hashes = ", node.sha256 " + recorded
	}
	got, err := kasnode.FileSHA256(node)
	if err != nil {
		return "", "", fmt.Sprintf("cannot hash %s: %v", node, err)
	}
	if got != want || recorded != want {
		return "", "", fmt.Sprintf("%s is not the node this kiro-cli bundles (file %s%s, kiro-cli %s); "+
			"kiro-cli extracts its own at this start and the next start checks again",
			node, got, hashes, want)
	}
	version, err := nodeVersion(e.lifecycleCtx, node)
	if err != nil {
		return "", "", err.Error()
	}
	if version != kasnode.ValidatedNodeVersion {
		return "", "", fmt.Sprintf("kiro-cli's node is %s, but the optimizations were validated on %s only",
			version, kasnode.ValidatedNodeVersion)
	}
	launcher, err = selfExecutable()
	if err != nil {
		return "", "", fmt.Sprintf("cannot locate marotte's own binary: %v", err)
	}
	return launcher, node, ""
}

func cliNodeHash(cliPath string) (string, error) {
	cliNodeHashes.Lock()
	h, ok := cliNodeHashes.m[cliPath]
	cliNodeHashes.Unlock()
	if ok {
		return h, nil
	}
	f, err := os.Open(cliPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h, err = kasnode.EmbeddedNodeHash(f)
	if err != nil {
		return "", err
	}
	cliNodeHashes.Lock()
	cliNodeHashes.m[cliPath] = h
	cliNodeHashes.Unlock()
	return h, nil
}

// nodeVersion runs node --version with no environment, so no NODE_OPTIONS can change or stop it.
func nodeVersion(ctx context.Context, node string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeVersionBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--version")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s --version failed: %w", node, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// kasLookup is the lookup kiro-cli resolves its node from in env, the environment it is started with.
func kasLookup(env []string) workspace.KASLookup {
	var l workspace.KASLookup
	l.RuntimeDir, _ = envValue(env, "KIRO_RUNTIME_DIR")
	l.DataHome, _ = envValue(env, "XDG_DATA_HOME")
	l.Home, _ = envValue(env, "HOME")
	return l
}

// envValue is env's last value for name, as os/exec resolves a repeated key.
func envValue(env []string, name string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			value, found = v, true
		}
	}
	return value, found
}
