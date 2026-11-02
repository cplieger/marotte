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

var errToolsDirUnusable = errors.New("the tools engine is off because its folder cannot be used")

// bootPaths is the one derivation of the config and tools dirs, for the server and the entrypoint.
type bootPaths struct {
	// toolsErr, when set, keeps the tools engine down; it is worded for the Tools tab.
	toolsErr  error
	configDir string
	toolsDir  string
}

// resolveBootPaths derives both dirs: an empty variable is its default, a relative one is absolute
// against the cwd, and symlinks stay unresolved.
func resolveBootPaths(src envx.Source) bootPaths {
	configDir := absDir("KIRO_CONFIG_DIR", cmp.Or(src.String("KIRO_CONFIG_DIR"), "/config"))
	toolsVar, tools := "MAROTTE_TOOLS_DIR", src.String("MAROTTE_TOOLS_DIR")
	if tools == "" {
		toolsVar, tools = "KIRO_CONFIG_DIR", filepath.Join(configDir, "tools")
	}
	toolsDir := absDir(toolsVar, tools)
	return bootPaths{configDir: configDir, toolsDir: toolsDir, toolsErr: toolsDirProblem(toolsVar, toolsDir)}
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

// toolsDirProblem is dirProblem for the tools dir, which must also be a PATH and GOPATH entry.
// The reason names envVar but never dir, since the Tools tab shows it.
func toolsDirProblem(envVar, dir string) error {
	var why string
	switch {
	case !filepath.IsAbs(dir):
		why = "is not absolute"
	case holdsListSeparator(dir):
		why = fmt.Sprintf("holds %q, which PATH and GOPATH cannot carry", filepath.ListSeparator)
	default:
		if err := steering.CheckPath(dir); err != nil {
			why = err.Error()
		}
	}
	if why == "" {
		return nil
	}
	return fmt.Errorf("%w: %s names a folder whose path %s; set it to another folder and restart",
		errToolsDirUnusable, envVar, why)
}

// holdsListSeparator reports whether dir would split into two entries of a PATH or GOPATH.
func holdsListSeparator(dir string) bool {
	return strings.ContainsRune(dir, filepath.ListSeparator)
}

func engineToolsDir(dir string, problem error) string {
	if problem != nil {
		return ""
	}
	return dir
}

type envValue struct {
	value string
	set   bool
}

// toolsEnv returns the PATH, GOPATH and GOBIN for a tools engine rooted at toolsDir, or with none
// when toolsDir is "". The image's imageToolsDir entries become toolsDir's or are dropped, a PATH
// lacking toolsDir's bin and go/bin gets them in the image's order, and a GOPATH or GOBIN the
// operator set to another value is kept.
func toolsEnv(toolsDir string, lookup func(string) (string, bool)) (path string, gopath, gobin envValue) {
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
	var goPathWant, goBinWant string
	if toolsDir != "" {
		bin, goBin := toolsDir+"/bin", toolsDir+"/go/bin"
		if !slices.Contains(entries, bin) {
			entries = slices.Insert(entries, 0, bin)
		}
		if !slices.Contains(entries, goBin) {
			entries = slices.Insert(entries, slices.Index(entries, bin)+1, goBin)
		}
		goPathWant, goBinWant = toolsDir+"/go", bin
	}
	return strings.Join(entries, string(filepath.ListSeparator)),
		derivedEnv(lookup, "GOPATH", imageToolsDir+"/go", goPathWant),
		derivedEnv(lookup, "GOBIN", imageToolsDir+"/bin", goBinWant)
}

// derivedEnv keeps name's value unless it is unset, empty or the image's, which become want, or
// absent when want is "".
func derivedEnv(lookup func(string) (string, bool), name, image, want string) envValue {
	if v, _ := lookup(name); v != "" && v != image {
		return envValue{value: v, set: true}
	}
	return envValue{value: want, set: want != ""}
}

// RunPaths prints, for bash or sh to eval, CONFIG_DIR, TOOLS, KIRO_HOME, PATH, GOPATH and GOBIN as
// the server derives them, so the boot acts on the server's folders. It returns 1, printing why,
// when the server would refuse the config dir.
func RunPaths(stdout, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	paths := resolveBootPaths(envx.Source{})
	if err := dirProblem("KIRO_CONFIG_DIR", paths.configDir); err != nil {
		log.Error("marotte cannot use its config folder", "error", logsafe.Field(err.Error()))
		return 1
	}
	home := os.Getenv("HOME")
	kiroHome, ignored := workspace.ResolveKiroHome(os.Getenv("KIRO_HOME"), home)
	if ignored != "" {
		log.Warn("ignoring KIRO_HOME, as kiro-cli does", "value", logsafe.Field(os.Getenv("KIRO_HOME")),
			"reason", ignored, "using", kiroHome)
	}
	path, gopath, gobin := toolsEnv(engineToolsDir(paths.toolsDir, paths.toolsErr), os.LookupEnv)
	var b strings.Builder
	b.WriteString("CONFIG_DIR=" + shellQuote(paths.configDir) + "\n")
	b.WriteString("TOOLS=" + shellQuote(paths.toolsDir) + "\n")
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
