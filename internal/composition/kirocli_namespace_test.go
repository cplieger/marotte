package composition

// Two engines write into the same tools tree, and only the namespace split keeps them from deleting
// each other's installs: toolbelt owns bin/, opt/, npm/ and python/; pinstall owns
// kiro-cli-versions/.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/pinstall/v3"
)

// The pin these tests install, and the toolbelt tool name that collides with it.
const (
	nsVersion = "9.9.9"
	nsTool    = "kiro-cli"
)

// toolbeltBody is the content of every regular file the fake toolbelt footprint
// owns, so a survivor check can tell "still there" from "replaced by our own file
// at the same path" -- which is what a publish over a colliding root actually
// does.
const toolbeltBody = "toolbelt-owned\n"

// nsEnv is one planted volume: the tools dir, the app config pointing at it, and
// the witness path the foreign dispatcher records its own execution in.
type nsEnv struct {
	t       *testing.T
	tools   string
	witness string
}

func newNSEnv(t *testing.T) *nsEnv {
	t.Helper()
	tools := t.TempDir()
	return &nsEnv{t: t, tools: tools, witness: filepath.Join(tools, "foreign-was-run")}
}

// config is marotte's real install configuration for this volume.
func (e *nsEnv) config() *pinstall.Config {
	return kiroInstallConfig(&Config{
		KiroCLIVersion:     nsVersion,
		KiroCLISHA256:      strings.Repeat("a", 64),
		KiroCLISHA256ARM64: strings.Repeat("b", 64),
		ToolsDir:           e.tools,
	})
}

// manager builds a manager from that configuration, as startKiroCLI does.
func (e *nsEnv) manager() *pinstall.Manager {
	e.t.Helper()
	mgr, err := pinstall.New(e.config())
	if err != nil {
		e.t.Fatalf("pinstall.New from the app's own config: %v", err)
	}
	return mgr
}

// plantToolbeltKiroCLI plants what the toolbelt engine writes for a manifest entry named
// `kiro-cli`: a version tree at opt/<name>/<version>/ and a bin/<name> symlink into it per linked
// binary.
func (e *nsEnv) plantToolbeltKiroCLI(linked ...string) (survivors []string, tree string) {
	e.t.Helper()
	tree = filepath.Join(e.tools, "opt", nsTool, nsVersion)
	binDir := filepath.Join(e.tools, kiroLinkDir)
	for _, dir := range []string{tree, binDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			e.t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	survivors = append(survivors, e.symlink(filepath.Join(e.tools, "opt", "gopls", "1.0.0", "gopls"), filepath.Join(binDir, "gopls")))
	for _, name := range []string{nsTool, nsTool + "-chat", nsTool + "-term"} {
		target := filepath.Join(tree, name)
		if err := os.WriteFile(target, []byte(toolbeltBody), 0o600); err != nil {
			e.t.Fatalf("WriteFile(%s): %v", target, err)
		}
		survivors = append(survivors, target)
	}
	foreign := filepath.Join(tree, nsTool)
	e.writeScript(foreign, "printf 'was-run\\n' >>"+shellQuote(e.witness)+"\nprintf 'kiro-cli "+nsVersion+"\\n'\n")
	for _, name := range linked {
		survivors = append(survivors, e.symlink(filepath.Join(tree, name), filepath.Join(binDir, name)))
	}
	return survivors, tree
}

// plantOwnVersion writes a COMPLETE version directory under the app's own install
// root, standing in for an install a previous boot finished: the ordinary restart
// path, and the only way to drive this wiring with no download.
func (e *nsEnv) plantOwnVersion() string {
	e.t.Helper()
	dir := filepath.Join(e.tools, nsTool+"-versions", nsVersion)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		e.t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	// Answers --version with its directory's name and exits 0 for every settings assertion; both
	// dispatchers are planted because kiro-cli-chat is Required.
	script := "case \"$1\" in --version) printf 'kiro-cli " + nsVersion + "\\n' ;; esac\n"
	e.writeScript(filepath.Join(dir, nsTool), script)
	e.writeScript(filepath.Join(dir, nsTool+"-chat"), script)
	if err := os.WriteFile(filepath.Join(dir, ".complete"), []byte(nsVersion+"\n"), 0o600); err != nil {
		e.t.Fatalf("write sentinel: %v", err)
	}
	return dir
}

// plantLegacyResidue plants the genuine shell-era residue marotte's own installer
// left, so a sweep assertion cannot pass by doing nothing at all. marotte wrote no
// journal, no `.prev` backup and no install marker, so its residue is a promoted
// dispatcher in the shared bin dir plus an orphan staging tree.
func (e *nsEnv) plantLegacyResidue(dispatchers ...string) []string {
	e.t.Helper()
	planted := make([]string, 0, len(dispatchers)+1)
	for _, name := range dispatchers {
		p := filepath.Join(e.tools, kiroLinkDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			e.t.Fatalf("MkdirAll(%s): %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte("shell-era dispatcher\n"), 0o600); err != nil {
			e.t.Fatalf("WriteFile(%s): %v", p, err)
		}
		planted = append(planted, p)
	}
	stage := filepath.Join(e.tools, legacyStagePrefix+"abc123")
	if err := os.MkdirAll(stage, 0o750); err != nil {
		e.t.Fatalf("MkdirAll(%s): %v", stage, err)
	}
	return append(planted, stage)
}

func (e *nsEnv) writeScript(path, body string) {
	e.t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"exit 0\n"), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
		e.t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func (e *nsEnv) symlink(target, newname string) string {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		e.t.Fatalf("MkdirAll(%s): %v", filepath.Dir(target), err)
	}
	if err := os.Symlink(target, newname); err != nil {
		e.t.Fatalf("Symlink(%s -> %s): %v", newname, target, err)
	}
	return newname
}

// assertIntact checks that every planted path is not merely PRESENT but
// unchanged: a symlink still points where the engine pointed it, and a regular
// file still holds the engine's bytes. Presence alone is not enough -- a publish
// removes a colliding version directory and renames its own tree into the same
// place, so the paths reappear holding our files.
func (e *nsEnv) assertIntact(survivors []string) {
	e.t.Helper()
	optTree := filepath.Join(e.tools, "opt") + string(filepath.Separator)
	for _, p := range survivors {
		fi, err := os.Lstat(p)
		if err != nil {
			e.t.Errorf("%s is gone: the install reached into the toolbelt engine's namespace", p)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				e.t.Errorf("Readlink(%s): %v", p, err)
				continue
			}
			if !strings.HasPrefix(target, optTree) {
				e.t.Errorf("%s now points at %q, outside the engine's own opt tree: its symlink was republished under it", p, target)
			}
			continue
		}
		raw, err := os.ReadFile(p) // #nosec G304 -- a path this test planted.
		if err != nil {
			e.t.Errorf("read %s: %v", p, err)
			continue
		}
		if body := string(raw); body != toolbeltBody && !strings.HasPrefix(body, "#!/bin/sh") {
			e.t.Errorf("%s holds %q, neither of the shapes this test planted: it was removed and rewritten", p, body)
		}
	}
	if _, err := os.Lstat(e.witness); err == nil {
		e.t.Error("the manager EXECUTED the toolbelt-owned kiro-cli; another owner's files must never be a version candidate")
	}
}

// TestToolbeltKiroCLIFootprintSurvivesABoot asserts that with a toolbelt-owned `kiro-cli` on the volume, a full
// Ensure against marotte's configuration must leave it intact.
func TestToolbeltKiroCLIFootprintSurvivesABoot(t *testing.T) {
	env := newNSEnv(t)
	survivors, tree := env.plantToolbeltKiroCLI(nsTool+"-chat", nsTool+"-term")
	own := env.plantOwnVersion()
	mgr := env.manager()

	if err := mgr.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	env.assertIntact(survivors)
	if ready, why := mgr.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true: the app's own install must activate regardless", why)
	}
	if got := mgr.PathEntry(); got != own {
		t.Errorf("PathEntry() = %q, want the app's own version directory %q", got, own)
	}
	if got := mgr.Path(); got != filepath.Join(own, nsTool) {
		t.Errorf("Path() = %q, want the dispatcher inside the app's own version directory", got)
	}
	if strings.HasPrefix(mgr.Path(), tree+string(filepath.Separator)) {
		t.Fatalf("Path() = %q resolves INSIDE the toolbelt-owned tree", mgr.Path())
	}
	link := filepath.Join(env.tools, kiroLinkDir, nsTool)
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the `docker exec … kiro-cli` convenience symlink was not published: %v", err)
	}
	if target != filepath.Join(own, nsTool) {
		t.Errorf("%s points at %q, want the active version's own dispatcher", link, target)
	}
}

// TestInstallRootIsOutsideTheToolbeltNamespace pins the structural half: the install root is one
// component directly under the tools tree, outside every toolbelt directory.
func TestInstallRootIsOutsideTheToolbeltNamespace(t *testing.T) {
	env := newNSEnv(t)
	own := env.plantOwnVersion()
	mgr := env.manager()
	if err := mgr.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if mgr.PathEntry() != own {
		t.Fatalf("PathEntry() = %q, want %q", mgr.PathEntry(), own)
	}

	rel, err := filepath.Rel(env.tools, mgr.PathEntry())
	if err != nil {
		t.Fatalf("the active version directory is not under the tools dir at all: %v", err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 {
		t.Fatalf("the active version directory is %s below the tools dir (%q); a nested root can sit inside a tree the engine enumerates", rel, parts)
	}
	if owned := []string{"bin", "opt", "npm", "python"}; slices.Contains(owned, parts[0]) {
		t.Fatalf("the install root %q collides with the toolbelt engine's own %v trees", parts[0], owned)
	}
}

// TestLegacySweepSparesToolbeltSymlinks pins the sweep half: the legacy purge must not delete the
// engine's kiro-cli symlinks in bin/.
func TestLegacySweepSparesToolbeltSymlinks(t *testing.T) {
	env := newNSEnv(t)
	survivors, _ := env.plantToolbeltKiroCLI(nsTool+"-chat", nsTool+"-term")
	// Only the orphan staging tree: planting a regular file over the engine's symlink would write
	// through it.
	residue := env.plantLegacyResidue()
	own := env.plantOwnVersion()

	if err := env.manager().Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	env.assertIntact(survivors)
	for _, p := range residue {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("the sweep left the shell installer's own residue at %s", p)
		}
	}
	if _, err := os.Lstat(filepath.Join(env.tools, legacyPurgeMarker)); err != nil {
		t.Errorf("a refused foreign entry blocked %s, so every later boot re-runs the sweep over the co-owned bin dir: %v", legacyPurgeMarker, err)
	}
	if _, err := os.Lstat(own); err != nil {
		t.Errorf("the orphan-stage sweep removed the installation root %s: %v", own, err)
	}
}

// TestLegacySweepRunsOncePerVolume pins the once-only marker: the layout the sweep deletes cannot
// come back.
func TestLegacySweepRunsOncePerVolume(t *testing.T) {
	env := newNSEnv(t)
	env.plantOwnVersion()
	residue := env.plantLegacyResidue(nsTool + "-chat")

	if err := env.manager().Ensure(t.Context()); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	for _, p := range residue {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("the first boot left the shell installer's own residue at %s", p)
		}
	}
	marker := filepath.Join(env.tools, legacyPurgeMarker)
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("the first boot did not record %s, so every later boot re-runs the sweep: %v", legacyPurgeMarker, err)
	}

	replanted := env.plantLegacyResidue(nsTool + "-chat")
	if err := env.manager().Ensure(t.Context()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	for _, p := range replanted {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("the second boot removed %s: the migration sweep still runs on every boot", p)
		}
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Errorf("the completion marker was removed by a later boot: %v", err)
	}
}

// shellQuote wraps s in single quotes for the fake dispatcher's script body. The
// only inputs are t.TempDir paths, so escaping an embedded quote would be dead
// code; a path that somehow held one would break the script loudly rather than
// silently misdirect the witness write.
func shellQuote(s string) string {
	return "'" + s + "'"
}
