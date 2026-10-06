package filebrowse

import (
	"path/filepath"
	"strings"
)

// The deny list is the second layer under paths.go's mount allow-list: these
// entries live inside the granted config mount, and an os.Root cannot refuse a
// sub-path. Its prefix tests are not pathinside calls, because Blocks excludes a
// listed directory itself and protectedDir asks in both directions.

// DefaultConfigDir is the config root a zero Sensitive is built over.
const DefaultConfigDir = "/config"

// sensitivePath describes a single blocked path entry with explicit
// match semantics: IsDir=true means "directory prefix" (blocks all
// contents), IsDir=false means "exact file match". Family also matches
// every name extending the file's with a dot, which is where its store
// renames an unparseable copy aside.
type sensitivePath struct {
	Path   string
	IsDir  bool
	Family bool
}

// sensitiveEntries are the blocked paths relative to the config root: agent
// config, generated docs, state files and the credential stores. kiro/ is the
// legacy KIRO_HOME the entrypoint migrates; it stays blocked for stragglers.
var sensitiveEntries = []sensitivePath{
	{Path: "kiro", IsDir: true},
	// Container HOME: AWS SSO token + OAuth secret, git SSH keys, the CLIs'
	// stored logins, ~/.gitconfig, kiro-cli's ~/.kiro state and its ~/.local
	// install tree.
	{Path: "home", IsDir: true},
	{Path: "chats", IsDir: true},
	{Path: "push-subs.json"},
	{Path: "vapid-keys.json"},
	// MCP server config — env / header / OAuth secrets stored cleartext.
	{Path: "mcp.json", Family: true},
	// The OAuth credentials KAS asks marotte to hold for it.
	{Path: "mcp-secrets.json", Family: true},
	// The forge credential store and the connection records beside it (a
	// client private key and a proxy URL with credentials can live there).
	{Path: "forge-store", IsDir: true},
	{Path: "forge-connections.json", Family: true},
}

// Sensitive is the deny list rooted at one config directory. The file
// browser and the `.kiro` docs scanner share one value, so the two cannot
// disagree about what is off limits. The zero value is the list rooted at
// DefaultConfigDir.
type Sensitive struct {
	list []sensitivePath
}

// NewSensitive builds the deny list under configDir. When configDir is a
// symlink the list carries both its spelling and its resolved target, since
// the docs scanner compares resolved paths only. An empty configDir is
// DefaultConfigDir.
func NewSensitive(configDir string) Sensitive {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	root, err := filepath.Abs(configDir)
	if err != nil {
		root = filepath.Clean(configDir)
	}
	list := rootedAt(root)
	if resolved, rErr := filepath.EvalSymlinks(root); rErr == nil && resolved != root {
		list = append(list, rootedAt(resolved)...)
	}
	return Sensitive{list: list}
}

func rootedAt(root string) []sensitivePath {
	out := make([]sensitivePath, 0, len(sensitiveEntries))
	for _, e := range sensitiveEntries {
		p := filepath.Join(root, e.Path)
		if e.IsDir {
			p += "/"
		}
		out = append(out, sensitivePath{Path: p, IsDir: e.IsDir, Family: e.Family})
	}
	return out
}

func (s Sensitive) entries() []sensitivePath {
	if s.list == nil {
		return rootedAt(DefaultConfigDir)
	}
	return s.list
}

// Blocks reports whether the resolved absolute path is user-blocked.
// Directory entries match their contents but not the directory itself;
// protectedDir answers for an operation that would affect the container.
// The caller passes an already-resolved absolute path (symlinks followed).
func (s Sensitive) Blocks(resolved string) bool {
	for _, sp := range s.entries() {
		if sp.IsDir {
			if strings.HasPrefix(resolved, sp.Path) {
				return true
			}
		} else if resolved == sp.Path || (sp.Family && strings.HasPrefix(resolved, sp.Path+".")) {
			return true
		}
	}
	return false
}

// protectedDir reports whether deleting `resolved` would wipe a directory listed in, or enclosing a
// path listed in, the deny list: the container check Blocks omits (Blocks passes `<root>/chats`
// itself). Callers pass a canonicalised path.
func (s Sensitive) protectedDir(resolved string) bool {
	res := strings.TrimRight(resolved, "/") + "/"
	for _, sp := range s.entries() {
		if sp.IsDir {
			if strings.HasPrefix(sp.Path, res) || strings.HasPrefix(res, sp.Path) {
				return true
			}
			continue
		}
		// Sensitive file entry: block any directory that encloses it.
		if strings.HasPrefix(sp.Path, res) {
			return true
		}
	}
	return false
}
