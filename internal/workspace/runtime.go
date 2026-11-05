package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// KASLookup is the environment kiro-cli resolves its unpacked KAS runtime (node and the agent
// server) from.
type KASLookup struct {
	NodeOverride string // KIRO_KAS_NODE_PATH
	RuntimeDir   string // KIRO_RUNTIME_DIR
	DataHome     string // XDG_DATA_HOME
	Home         string // HOME
}

// KASLookupFromEnv reads the lookup from the process environment.
func KASLookupFromEnv() KASLookup {
	return KASLookup{
		NodeOverride: os.Getenv("KIRO_KAS_NODE_PATH"),
		RuntimeDir:   os.Getenv("KIRO_RUNTIME_DIR"),
		DataHome:     os.Getenv("XDG_DATA_HOME"),
		Home:         os.Getenv("HOME"),
	}
}

// KASRuntime is one kiro-cli version's unpacked KAS runtime. A path is "" when it does not exist.
type KASRuntime struct {
	Node string
	//deadset:ignore DS1301 -- read by the kascap census tests, which must resolve the bundle as kiro-cli does
	Server string
}

// kasServerRel is the agent server's path inside a runtime's kas/ tree.
var kasServerRel = filepath.Join("node_modules", "@kiro", "agent", "dist", "server", "acp-server.js")

// runtimeEntryRe is a runtime-dir entry's name, <version>-<16 hex>.
var runtimeEntryRe = regexp.MustCompile(`^(.+)-[0-9a-f]{16}$`)

// Find returns where kiro-cli runs KAS from for version, resolved as kiro-cli 3.0.0 resolves it
// (crates/chat-cli/src/embedded_tui/runtime_dir.rs): KIRO_KAS_NODE_PATH as given for the node;
// else, when KIRO_RUNTIME_DIR is absolute, its complete <version>-<id> entry; else the data dir
// ($XDG_DATA_HOME, else $HOME/.local/share, then kiro-cli/), which kiro-cli also falls back to
// when it cannot write the runtime dir. An empty version accepts only a sole candidate. Call it
// per use: kiro-cli names the entry at its first start, and that start deletes the data-dir node.
func (l KASLookup) Find(version string) KASRuntime {
	rt, ok := l.runtimeEntry(version)
	if !ok {
		rt = l.dataDirRuntime(version)
	}
	if l.NodeOverride != "" {
		rt.Node = l.NodeOverride
	}
	return rt
}

// runtimeEntry is version's complete entry under the runtime dir, false when there is none.
func (l KASLookup) runtimeEntry(version string) (KASRuntime, bool) {
	if !filepath.IsAbs(l.RuntimeDir) {
		return KASRuntime{}, false
	}
	dir, ok := soleEntry(l.RuntimeDir, func(name string) bool {
		m := runtimeEntryRe.FindStringSubmatch(name)
		return len(m) == 2 && (version == "" || m[1] == version) &&
			isFile(filepath.Join(l.RuntimeDir, name, ".complete"))
	}, version == "")
	if !ok {
		return KASRuntime{}, false
	}
	return KASRuntime{
		Node:   existing(filepath.Join(dir, "node")),
		Server: existing(filepath.Join(dir, "kas", kasServerRel)),
	}, true
}

// dataDirRuntime is version's runtime in kiro-cli's data dir, where the node is shared by every
// version and each version's agent server sits in kas/<version>-<hash>/.
func (l KASLookup) dataDirRuntime(version string) KASRuntime {
	data := l.DataHome
	if data == "" {
		if l.Home == "" {
			return KASRuntime{}
		}
		data = filepath.Join(l.Home, ".local", "share")
	}
	data = filepath.Join(data, "kiro-cli")
	kas := filepath.Join(data, "kas")
	dir, _ := soleEntry(kas, func(name string) bool {
		return (version == "" || strings.HasPrefix(name, version+"-")) &&
			isFile(filepath.Join(kas, name, kasServerRel))
	}, version == "")
	rt := KASRuntime{Node: existing(filepath.Join(data, "node"))}
	if dir != "" {
		rt.Server = filepath.Join(dir, kasServerRel)
	}
	return rt
}

// soleEntry is the first, by name, of root's directories that match accepts; with onlyOne, the
// match only when it is the sole one.
func soleEntry(root string, accept func(name string) bool, onlyOne bool) (string, bool) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && accept(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 || (onlyOne && len(names) > 1) {
		return "", false
	}
	return filepath.Join(root, slices.Min(names)), true
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func existing(p string) string {
	if isFile(p) {
		return p
	}
	return ""
}
