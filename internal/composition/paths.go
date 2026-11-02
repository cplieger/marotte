package composition

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/envx/v2"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/marotte/internal/workspace"
)

// PathsCommand is the subcommand that prints the boot's folders for entrypoint.sh to eval.
const PathsCommand = "paths"

// imageToolsDir is the tools dir the image's ENV names in PATH, GOPATH and GOBIN (Dockerfile).
const imageToolsDir = "/config/tools"

// imageSystemPath is the image's PATH after its imageToolsDir entries (Dockerfile).
var imageSystemPath = []string{
	"/config/home/.local/bin", "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
}

var errToolsDirUnusable = errors.New("the tools engine is off because its folder cannot be used")

type bootPaths struct {
	configDir string
	tools     toolsRoot
}

// toolsRoot is the one verdict on the tools dir. Every consumer reads it through use, so a rejected
// dir is off everywhere: no engine, no kiro-cli install, no instance lock or mode change, no PATH,
// GOPATH or GOBIN entry, and no mention in steering. The zero value is no tools dir, as outside the
// container.
type toolsRoot struct {
	// err is why dir is rejected, wrapping errToolsDirUnusable and worded for the Tools tab.
	err error
	dir string
	// spelled is dir as configured, before absDir cleaned it: PATH, GOPATH and GOBIN may carry a
	// rejected dir in either form.
	spelled string
}

func (r toolsRoot) use() (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.dir, nil
}

// resolveBootPaths derives both dirs: an empty variable is its default, a relative one is absolute
// against the cwd, and symlinks stay unresolved.
func resolveBootPaths(src envx.Source) bootPaths {
	configVar := cmp.Or(src.String("KIRO_CONFIG_DIR"), "/config")
	toolsVar, tools := "MAROTTE_TOOLS_DIR", src.String("MAROTTE_TOOLS_DIR")
	if tools == "" {
		toolsVar, tools = "KIRO_CONFIG_DIR", configVar+"/tools"
	}
	return bootPaths{configDir: absDir("KIRO_CONFIG_DIR", configVar), tools: checkToolsRoot(toolsVar, tools)}
}

// dirProblem reports why dir, set by envVar, cannot be one of the server's folders, or nil.
func dirProblem(envVar, dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s=%q: not an absolute path", envVar, dir)
	}
	if err := steering.CheckPath(dir); err != nil {
		return fmt.Errorf("%s=%q: %w", envVar, dir, err)
	}
	return nil
}

// checkToolsRoot judges spelled, set by envVar and made absolute, by dirProblem's rule plus the
// PATH and GOPATH list separator. The reason names envVar but never the dir, since the Tools tab
// shows it.
func checkToolsRoot(envVar, spelled string) toolsRoot {
	dir := absDir(envVar, spelled)
	var why string
	switch {
	case !filepath.IsAbs(dir):
		why = "is not absolute"
	case strings.ContainsRune(dir, filepath.ListSeparator):
		why = fmt.Sprintf("holds %q, which PATH and GOPATH cannot carry", filepath.ListSeparator)
	default:
		if err := steering.CheckPath(dir); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		return toolsRoot{dir: dir, spelled: spelled}
	}
	return toolsRoot{dir: dir, spelled: spelled, err: fmt.Errorf("%w: %s names a folder whose path %s; set it to another folder and restart",
		errToolsDirUnusable, envVar, why)}
}

type envValue struct {
	value string
	set   bool
}

// toolsEnv returns the PATH, GOPATH and GOBIN for root. For a usable dir the image's imageToolsDir
// entries become the dir's, a PATH lacking the dir's bin and go/bin gets them in the image's order,
// and a GOPATH or GOBIN the operator set to another value is kept. For a rejected or absent dir the
// image's entries and the rejected dir's own, in its configured and its absolute spelling, are
// removed from PATH, from every root of a GOPATH list and from GOBIN, and every other entry and
// value stays.
func toolsEnv(root toolsRoot, lookup func(string) (string, bool)) (path string, gopath, gobin envValue) {
	toolsDir, err := root.use()
	pathValue, _ := lookup("PATH")
	entries := make([]string, 0, 8)
	for _, e := range filepath.SplitList(pathValue) {
		if e == imageToolsDir+"/bin" || e == imageToolsDir+"/go/bin" {
			if toolsDir == "" {
				continue
			}
			e = toolsDir + strings.TrimPrefix(e, imageToolsDir)
		}
		entries = append(entries, e)
	}
	if err != nil {
		for _, dir := range []string{root.dir, root.spelled} {
			entries = dropRun(entries, filepath.SplitList(dir+"/bin"))
			entries = dropRun(entries, filepath.SplitList(dir+"/go/bin"))
		}
		return strings.Join(entries, string(filepath.ListSeparator)),
			goPathWithout(lookup, imageToolsDir+"/go", root.dir+"/go", root.spelled+"/go"),
			derivedEnv(lookup, "GOBIN", "", imageToolsDir+"/bin", root.dir+"/bin", root.spelled+"/bin")
	}
	if toolsDir == "" {
		return strings.Join(entries, string(filepath.ListSeparator)),
			goPathWithout(lookup, imageToolsDir+"/go"),
			derivedEnv(lookup, "GOBIN", "", imageToolsDir+"/bin")
	}
	bin, goBin := toolsDir+"/bin", toolsDir+"/go/bin"
	if !slices.Contains(entries, bin) {
		entries = slices.Insert(entries, 0, bin)
	}
	if !slices.Contains(entries, goBin) {
		entries = slices.Insert(entries, slices.Index(entries, bin)+1, goBin)
	}
	return strings.Join(entries, string(filepath.ListSeparator)),
		derivedEnv(lookup, "GOPATH", toolsDir+"/go", imageToolsDir+"/go"),
		derivedEnv(lookup, "GOBIN", bin, imageToolsDir+"/bin")
}

// dropRun is removeRuns for PATH, keeping a run the image's own system PATH holds, since
// /usr/sbin:/usr names /usr/sbin and /usr/bin as its bin.
func dropRun(entries, run []string) []string {
	for i := range imageSystemPath {
		if runAt(imageSystemPath, run, i) {
			return entries
		}
	}
	return removeRuns(entries, run)
}

// goPathWithout returns GOPATH with every root in ours removed, unset when no root remains.
func goPathWithout(lookup func(string) (string, bool), ours ...string) envValue {
	v, _ := lookup("GOPATH")
	roots := filepath.SplitList(v)
	for _, root := range ours {
		roots = removeRuns(roots, filepath.SplitList(root))
	}
	kept := strings.Join(roots, string(filepath.ListSeparator))
	return envValue{value: kept, set: kept != ""}
}

// removeRuns removes every run of consecutive entries equal to run: a dir holding the list
// separator sits in a list as several entries.
func removeRuns(entries, run []string) []string {
	kept := make([]string, 0, len(entries))
	for i := 0; i < len(entries); {
		if runAt(entries, run, i) {
			i += len(run)
			continue
		}
		kept = append(kept, entries[i])
		i++
	}
	return kept
}

func runAt(entries, run []string, i int) bool {
	return len(entries)-i >= len(run) && slices.Equal(entries[i:i+len(run)], run)
}

// derivedEnv keeps name's value unless it is unset, empty or one of ours, which become want, or
// absent when want is "".
func derivedEnv(lookup func(string) (string, bool), name, want string, ours ...string) envValue {
	if v, _ := lookup(name); v != "" && !slices.Contains(ours, v) {
		return envValue{value: v, set: true}
	}
	return envValue{value: want, set: want != ""}
}

// RunPaths prints, for bash or sh to eval, CONFIG_DIR, TOOLS, KIRO_HOME, PATH, GOPATH and GOBIN as
// the server derives them, so the boot acts on the server's folders. TOOLS is empty when the server
// rejects the tools dir. It returns 1, printing why, when the server would refuse the config dir.
func RunPaths(stdout, stderr io.Writer) int {
	return runPaths(stdout, stderr, os.LookupEnv)
}

func runPaths(stdout, stderr io.Writer, lookup func(string) (string, bool)) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	getenv := func(k string) string { v, _ := lookup(k); return v }
	paths := resolveBootPaths(envx.Source{Get: getenv})
	if err := dirProblem("KIRO_CONFIG_DIR", paths.configDir); err != nil {
		log.Error("marotte cannot use its config folder", "error", logsafe.Field(err.Error()))
		return 1
	}
	kiroHome, ignored := workspace.ResolveKiroHome(getenv("KIRO_HOME"), getenv("HOME"))
	if ignored != "" {
		log.Warn("ignoring KIRO_HOME, as kiro-cli does", "value", logsafe.Field(getenv("KIRO_HOME")),
			"reason", ignored, "using", kiroHome)
	}
	path, gopath, gobin := toolsEnv(paths.tools, lookup)
	toolsDir, _ := paths.tools.use()
	var b strings.Builder
	b.WriteString("CONFIG_DIR=" + shellQuote(paths.configDir) + "\n")
	b.WriteString("TOOLS=" + shellQuote(toolsDir) + "\n")
	b.WriteString("export KIRO_HOME=" + shellQuote(kiroHome) + "\n")
	b.WriteString("export PATH=" + shellQuote(path) + "\n")
	for _, v := range []struct {
		name string
		val  envValue
	}{{"GOPATH", gopath}, {"GOBIN", gobin}} {
		if v.val.set {
			b.WriteString("export " + v.name + "=" + shellQuote(v.val.value) + "\n")
		} else {
			b.WriteString("unset " + v.name + "\n")
		}
	}
	if _, err := io.WriteString(stdout, b.String()); err != nil {
		log.Error("could not print the boot folders", "error", err)
		return 1
	}
	return 0
}

// shellQuote single-quotes s for bash and sh, so eval reads back every byte.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
