package spec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Root is one .kiro tree in scope: the workspace root's own, or a first-level
// repository's.
type Root struct {
	// Dir is the absolute path of the .kiro directory.
	Dir string
	// Rel is the workspace-relative path of that directory: ".kiro" or
	// "<repo>/.kiro".
	Rel string
}

var specDirRe = regexp.MustCompile(`^(?:([^/.][^/]*)/)?\.kiro/specs/([^/]+)(?:/|$)`)

// Roots enumerates the .kiro trees under workDir: the workspace's own first,
// then one per non-dot first-level subdirectory that holds a .kiro directory,
// in directory order.
func Roots(workDir string) []Root {
	var roots []Root
	if isDir(filepath.Join(workDir, ".kiro")) {
		roots = append(roots, Root{Dir: filepath.Join(workDir, ".kiro"), Rel: ".kiro"})
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return roots
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(workDir, e.Name(), ".kiro")
		if isDir(dir) {
			roots = append(roots, Root{Dir: dir, Rel: e.Name() + "/.kiro"})
		}
	}
	return roots
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// Address resolves a workspace-relative spec directory against roots: dir must
// equal <root.Rel>/specs/<name> for one root, with name a single non-empty
// segment that is not "." or "..". Anything else answers ok false.
//
// It lives here rather than at either consumer because two of them now resolve
// the same string — the GET and the approve command — and a second copy could
// disagree about which directories are addressable, which for the command would
// mean recording an approval against a spec the GET refuses to serve.
func Address(roots []Root, dir string) (Root, string, bool) {
	for _, root := range roots {
		name, found := strings.CutPrefix(dir, root.Rel+"/specs/")
		if !found || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			continue
		}
		return root, name, true
	}
	return Root{}, "", false
}

// DirOf names the spec directory a workspace-relative, slash-separated path is
// in or is: ".kiro/specs/<name>" or "<repo>/.kiro/specs/<name>". The
// directory itself counts, so a delete of the directory marks it too. It is
// purely lexical and touches no filesystem.
func DirOf(rel string) (dir string, ok bool) {
	m := specDirRe.FindStringSubmatch(rel)
	if len(m) == 0 || m[2] == "." || m[2] == ".." {
		return "", false
	}
	prefix := ""
	if m[1] != "" {
		prefix = m[1] + "/"
	}
	return prefix + ".kiro/specs/" + m[2], true
}
