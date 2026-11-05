package composition

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/marotte/internal/workspace"
	"github.com/cplieger/toolbelt/v3/pin"
)

func envSource(vars map[string]string) envx.Source {
	return envx.Source{Get: func(k string) string { return vars[k] }}
}

// The final stage's ENV PATH, and that PATH after its tools entries.
const (
	dockerfilePath       = "/config/tools/bin:/config/tools/go/bin:" + dockerfileSystemPath
	dockerfileSystemPath = "/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// toolsEnv recognises the image's entries by imageToolsDir and imageSystemPath, so both must spell
// the Dockerfile's final ENV values.
func TestImagePathConstants_MatchTheDockerfile(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	env := map[string]string{}
	for line := range strings.Lines(string(data)) {
		rest, isEnv := strings.CutPrefix(strings.TrimSpace(line), "ENV ")
		if k, v, ok := strings.Cut(rest, "="); isEnv && ok {
			env[k] = strings.Trim(v, `"`)
		}
	}
	for k, want := range map[string]string{
		"PATH":   dockerfilePath,
		"GOPATH": imageToolsDir + "/go",
		"GOBIN":  imageToolsDir + "/bin",
	} {
		if env[k] != want {
			t.Errorf("Dockerfile's last ENV %s = %q, want %q", k, env[k], want)
		}
	}
	if got := strings.Join(imageSystemPath, ":"); got != dockerfileSystemPath {
		t.Errorf("imageSystemPath = %q, want %q", got, dockerfileSystemPath)
	}
}

// The values are ConfigFromEnv's documented ones: an empty variable is unset, a relative one is
// joined to the cwd and cleaned, and a symlink is kept as named.
func TestResolveBootPaths_DerivesBothFolders(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Setup: Getwd: %v", err)
	}
	for _, tc := range []struct {
		name               string
		vars               map[string]string
		wantConfig, wantTD string
	}{
		{"unset", nil, "/config", "/config/tools"},
		{"empty", map[string]string{"KIRO_CONFIG_DIR": "", "MAROTTE_TOOLS_DIR": ""}, "/config", "/config/tools"},
		{"config dir only", map[string]string{"KIRO_CONFIG_DIR": "/srv/m"}, "/srv/m", "/srv/m/tools"},
		{"both", map[string]string{"KIRO_CONFIG_DIR": "/srv/m", "MAROTTE_TOOLS_DIR": "/opt/t"}, "/srv/m", "/opt/t"},
		{"tools dir only, trailing slash", map[string]string{"MAROTTE_TOOLS_DIR": "/opt/t/"}, "/config", "/opt/t"},
		{"dot-dot", map[string]string{"KIRO_CONFIG_DIR": "/srv/m/../n/"}, "/srv/n", "/srv/n/tools"},
		{"relative", map[string]string{"KIRO_CONFIG_DIR": "data/", "MAROTTE_TOOLS_DIR": "./t"}, cwd + "/data", cwd + "/t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := resolveBootPaths(envSource(tc.vars))
			toolsDir, err := p.tools.use()
			if p.configDir != tc.wantConfig || toolsDir != tc.wantTD || err != nil {
				t.Errorf("resolveBootPaths(%v) = %q, %q, %v; want %q, %q, nil",
					tc.vars, p.configDir, toolsDir, err, tc.wantConfig, tc.wantTD)
			}
		})
	}
}

// A tools folder PATH or the steering documents cannot carry keeps the engine off, named by the
// variable that set it. The value is kept as given, never trimmed, and stays out of the reason the
// Tools tab shows.
func TestResolveBootPaths_RefusesAToolsFolderTheEngineCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name, env, value, wantVar, wantTools string
	}{
		{"colon", "MAROTTE_TOOLS_DIR", "/srv/a:b", "MAROTTE_TOOLS_DIR", "/srv/a:b"},
		{"colon from the config dir", "KIRO_CONFIG_DIR", "/srv/a:b", "KIRO_CONFIG_DIR", "/srv/a:b/tools"},
		{"trailing newline", "MAROTTE_TOOLS_DIR", "/srv/t\n", "MAROTTE_TOOLS_DIR", "/srv/t\n"},
		{"carriage return", "MAROTTE_TOOLS_DIR", "/srv/t\rx", "MAROTTE_TOOLS_DIR", "/srv/t\rx"},
		{"backtick", "MAROTTE_TOOLS_DIR", "/srv/t`x", "MAROTTE_TOOLS_DIR", "/srv/t`x"},
		{"line separator", "MAROTTE_TOOLS_DIR", "/srv/t\u2028x", "MAROTTE_TOOLS_DIR", "/srv/t\u2028x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := resolveBootPaths(envSource(map[string]string{tc.env: tc.value}))
			if p.tools.dir != tc.wantTools {
				t.Errorf("tools.dir = %q, want %q unchanged", p.tools.dir, tc.wantTools)
			}
			toolsDir, err := p.tools.use()
			if toolsDir != "" || !errors.Is(err, errToolsDirUnusable) {
				t.Fatalf("tools.use() = %q, %v; want \"\", errToolsDirUnusable", toolsDir, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantVar+" names a folder whose path") {
				t.Errorf("toolsErr = %q, want it to name %s", msg, tc.wantVar)
			}
			if strings.Contains(msg, "/srv/") {
				t.Errorf("toolsErr = %q shows the path, which the Tools tab would render", msg)
			}
		})
	}
}

func TestToolsEnv(t *testing.T) {
	const image = "/config/tools/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/bin"
	for _, tc := range []struct {
		name, toolsDir      string
		env                 map[string]string
		wantPath            string
		wantGopath, wantBin envValue
	}{
		{
			"default dir leaves the image's values", "/config/tools",
			map[string]string{"PATH": image, "GOPATH": "/config/tools/go", "GOBIN": "/config/tools/bin"},
			image,
			envValue{"/config/tools/go", true},
			envValue{"/config/tools/bin", true},
		},
		{
			"configured dir takes the image's entries over in place", "/srv/t",
			map[string]string{"PATH": "/a:" + image, "GOPATH": "/config/tools/go", "GOBIN": "/config/tools/bin"},
			"/a:/srv/t/bin:/srv/t/go/bin:/config/home/.local/bin:/usr/bin",
			envValue{"/srv/t/go", true},
			envValue{"/srv/t/bin", true},
		},
		{
			"an operator's GOPATH and GOBIN are kept", "/srv/t",
			map[string]string{"PATH": image, "GOPATH": "/g", "GOBIN": "/gb"},
			"/srv/t/bin:/srv/t/go/bin:/config/home/.local/bin:/usr/bin",
			envValue{"/g", true},
			envValue{"/gb", true},
		},
		{
			"only whole entries move and empty entries survive", "/srv/t",
			map[string]string{"PATH": "/config/tools/bin2::/config/tools/bin:/x/config/tools/go/bin"},
			"/config/tools/bin2::/srv/t/bin:/srv/t/go/bin:/x/config/tools/go/bin",
			envValue{"/srv/t/go", true},
			envValue{"/srv/t/bin", true},
		},
		{
			"a PATH lacking the entries gains them in front, empty GOPATH and GOBIN follow", "/srv/t",
			map[string]string{"PATH": "/usr/bin:/srv/t/bin2", "GOPATH": "", "GOBIN": ""},
			"/srv/t/bin:/srv/t/go/bin:/usr/bin:/srv/t/bin2",
			envValue{"/srv/t/go", true},
			envValue{"/srv/t/bin", true},
		},
		{
			"a missing go/bin goes right after the bin, which keeps its place", "/srv/t",
			map[string]string{"PATH": "/a:/srv/t/bin:/b"},
			"/a:/srv/t/bin:/srv/t/go/bin:/b",
			envValue{"/srv/t/go", true},
			envValue{"/srv/t/bin", true},
		},
		{
			"an empty PATH gets the two entries and no empty one", "/srv/t",
			map[string]string{"PATH": ""},
			"/srv/t/bin:/srv/t/go/bin",
			envValue{"/srv/t/go", true},
			envValue{"/srv/t/bin", true},
		},
		{
			"no tools dir drops the image's entries and values", "",
			map[string]string{"PATH": image, "GOPATH": "/config/tools/go", "GOBIN": "/config/tools/bin"},
			"/config/home/.local/bin:/usr/bin",
			envValue{},
			envValue{},
		},
		{
			"a rejected dir leaves PATH, GOPATH and GOBIN with the image's and its own entries removed", "/srv/t`x",
			map[string]string{"PATH": "/srv/t`x/bin:/a:" + image + ":/srv/t`x/go/bin", "GOPATH": "/srv/t`x/go", "GOBIN": "/srv/t`x/bin"},
			"/a:/config/home/.local/bin:/usr/bin",
			envValue{},
			envValue{},
		},
		{
			"a rejected dir holding the separator is removed as the entries it splits into", "/srv/a:b",
			map[string]string{"PATH": "/srv/a:b/bin:/srv/a:/usr/bin:b/bin:/srv/a:b/go/bin"},
			"/srv/a:/usr/bin:b/bin",
			envValue{},
			envValue{},
		},
		{
			"a rejected dir is removed in its configured spelling", "a/../t`x",
			map[string]string{"PATH": "a/../t`x/bin:/usr/bin:a/../t`x/go/bin", "GOPATH": "a/../t`x/go", "GOBIN": "a/../t`x/bin"},
			"/usr/bin",
			envValue{},
			envValue{},
		},
		{
			"a rejected dir splitting into system entries leaves them", "/usr/sbin:/usr",
			map[string]string{"PATH": dockerfilePath, "GOPATH": "/usr/sbin:/usr/go", "GOBIN": "/config/tools/bin"},
			dockerfileSystemPath,
			envValue{},
			envValue{},
		},
		{
			"a rejected dir ending in the separator leaves the system entries", "/sbin:",
			map[string]string{"PATH": dockerfilePath + ":/sbin:/go/bin"},
			dockerfileSystemPath,
			envValue{},
			envValue{},
		},
		{
			"a rejected dir keeps an operator's GOPATH and GOBIN", "/srv/t`x",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/g", "GOBIN": "/gb"},
			"/usr/bin",
			envValue{"/g", true},
			envValue{"/gb", true},
		},
		{
			"a rejected dir leaves only the other roots of a GOPATH list", "a/../t`x",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/operator/go:a/../t`x/go:/config/tools/go:/g"},
			"/usr/bin",
			envValue{"/operator/go:/g", true},
			envValue{},
		},
		{
			"a rejected dir holding the separator leaves the GOPATH roots outside its run", "/srv/a:b",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/srv/a:/srv/a:b/go:/operator/go"},
			"/usr/bin",
			envValue{"/srv/a:/operator/go", true},
			envValue{},
		},
		{
			"no tools dir leaves only the other roots of a GOPATH list", "",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/config/tools/go:/operator/go"},
			"/usr/bin",
			envValue{"/operator/go", true},
			envValue{},
		},
		{
			"no tools dir keeps an operator's GOPATH and GOBIN", "",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/g", "GOBIN": "/gb"},
			"/usr/bin",
			envValue{"/g", true},
			envValue{"/gb", true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			root := toolsRoot{}
			if tc.toolsDir != "" {
				root = checkToolsRoot("MAROTTE_TOOLS_DIR", tc.toolsDir)
			}
			path, gopath, gobin := toolsEnv(root, lookup)
			if path != tc.wantPath {
				t.Errorf("PATH = %q, want %q", path, tc.wantPath)
			}
			if gopath != tc.wantGopath || gobin != tc.wantBin {
				t.Errorf("GOPATH, GOBIN = %+v, %+v; want %+v, %+v", gopath, gobin, tc.wantGopath, tc.wantBin)
			}
		})
	}
}

// evalPaths evals RunPaths' output in shell sh and returns CONFIG_DIR, TOOLS and the exported
// environment a child of that shell sees.
func evalPaths(t *testing.T, sh string) (configDir, tools string, env map[string]string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if rc := RunPaths(&out, &errOut); rc != 0 {
		t.Fatalf("RunPaths = %d, want 0; stderr: %s", rc, errOut.String())
	}
	script := `eval "$1" || exit 9; printf '%s\0%s\0' "$CONFIG_DIR" "$TOOLS"; exec /usr/bin/env -0`
	got, err := exec.Command(sh, "-c", script, "sh", out.String()).Output()
	if err != nil {
		t.Fatalf("%s eval of %q: %v", sh, out.String(), err)
	}
	fields := strings.Split(strings.TrimSuffix(string(got), "\x00"), "\x00")
	if len(fields) < 2 {
		t.Fatalf("%s printed %q, want CONFIG_DIR and TOOLS first", sh, got)
	}
	env = map[string]string{}
	for _, kv := range fields[2:] {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return fields[0], fields[1], env
}

func shells(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, sh := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			found = append(found, sh)
		}
	}
	if len(found) == 0 {
		t.Skip("no /bin/bash or /bin/sh to eval the output with")
	}
	return found
}

// The entrypoint evals RunPaths' output, so every byte of a folder must survive the shell.
func TestRunPaths_ShellsReadBackTheServersFolders(t *testing.T) {
	base := t.TempDir()
	odd := ` it's "here" $(false) \ é *`
	configDir, toolsDir := base+"/c"+odd, base+"/t"+odd
	kiroHome := base + "/k\n x"
	t.Setenv("HOME", base+"/home")
	t.Setenv("KIRO_CONFIG_DIR", configDir)
	t.Setenv("MAROTTE_TOOLS_DIR", toolsDir)
	t.Setenv("KIRO_HOME", kiroHome)
	t.Setenv("PATH", "/config/tools/bin:/config/tools/go/bin:/usr/bin")
	t.Setenv("GOPATH", "")
	t.Setenv("GOBIN", "/config/tools/bin")
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			gotConfig, gotTools, env := evalPaths(t, sh)
			if gotConfig != configDir || gotTools != toolsDir {
				t.Errorf("CONFIG_DIR, TOOLS = %q, %q; want %q, %q", gotConfig, gotTools, configDir, toolsDir)
			}
			for k, want := range map[string]string{
				"KIRO_HOME": kiroHome,
				"PATH":      toolsDir + "/bin:" + toolsDir + "/go/bin:/usr/bin",
				"GOPATH":    toolsDir + "/go",
				"GOBIN":     toolsDir + "/bin",
			} {
				if env[k] != want {
					t.Errorf("exported %s = %q, want %q", k, env[k], want)
				}
			}
		})
	}
}

// A rejected tools folder is no folder of the boot's: TOOLS is empty and nothing of it reaches
// PATH, GOPATH or GOBIN, even where the environment already held it.
func TestRunPaths_KeepsARejectedToolsFolderOffTheEnvironment(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base+"/home")
	t.Setenv("KIRO_CONFIG_DIR", base+"/c")
	t.Setenv("MAROTTE_TOOLS_DIR", base+"/a:b")
	t.Setenv("KIRO_HOME", "")
	t.Setenv("PATH", base+"/a:b/bin:/config/tools/bin:/config/tools/go/bin:/usr/bin")
	t.Setenv("GOPATH", base+"/a:b/go")
	t.Setenv("GOBIN", "/config/tools/bin")
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			_, gotTools, env := evalPaths(t, sh)
			if gotTools != "" {
				t.Errorf("TOOLS = %q, want empty", gotTools)
			}
			if env["PATH"] != "/usr/bin" {
				t.Errorf("exported PATH = %q, want /usr/bin", env["PATH"])
			}
			for _, k := range []string{"GOPATH", "GOBIN"} {
				if v, ok := env[k]; ok {
					t.Errorf("%s = %q stayed exported, want it unset", k, v)
				}
			}
			if env["KIRO_HOME"] != base+"/home/.kiro" {
				t.Errorf("exported KIRO_HOME = %q, want the default %q", env["KIRO_HOME"], base+"/home/.kiro")
			}
		})
	}
}

// An operator may have put a rejected folder on PATH as configured, relative and unclean, so the
// server removes that spelling as well as the absolute one.
func TestRunPaths_RemovesARejectedToolsFolderInEitherSpelling(t *testing.T) {
	for _, tc := range []struct{ name, configDir, toolsDir, spelled, cleaned string }{
		{"MAROTTE_TOOLS_DIR", "", "a/../t`x", "a/../t`x", "t`x"},
		{"KIRO_CONFIG_DIR", "c/../a:b", "", "c/../a:b/tools", "a:b/tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			cleaned := base + "/" + tc.cleaned
			t.Setenv("HOME", base+"/home")
			t.Setenv("KIRO_CONFIG_DIR", cmp.Or(tc.configDir, base+"/c"))
			t.Setenv("MAROTTE_TOOLS_DIR", tc.toolsDir)
			t.Setenv("KIRO_HOME", "")
			t.Setenv("PATH", tc.spelled+"/bin:/usr/bin:"+tc.spelled+"/go/bin:"+cleaned+"/bin")
			t.Setenv("GOPATH", tc.spelled+"/go")
			t.Setenv("GOBIN", tc.spelled+"/bin")
			_, gotTools, env := evalPaths(t, shells(t)[0])
			if gotTools != "" {
				t.Errorf("TOOLS = %q, want empty", gotTools)
			}
			if env["PATH"] != "/usr/bin" {
				t.Errorf("exported PATH = %q, want /usr/bin", env["PATH"])
			}
			for _, k := range []string{"GOPATH", "GOBIN"} {
				if v, ok := env[k]; ok {
					t.Errorf("%s = %q stayed exported, want it unset", k, v)
				}
			}
		})
	}
}

// A GOPATH may list the rejected folder's go/ beside the operator's own roots, so only that root
// goes, also when a colon splits it into several entries.
func TestRunPaths_KeepsTheOtherGoRootsBesideARejectedToolsFolder(t *testing.T) {
	for _, tc := range []struct{ name, toolsDir string }{
		{"a refused character", "/t`x"},
		{"the separator", "/a:b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("HOME", base+"/home")
			t.Setenv("KIRO_CONFIG_DIR", base+"/c")
			t.Setenv("MAROTTE_TOOLS_DIR", base+tc.toolsDir)
			t.Setenv("KIRO_HOME", "")
			t.Setenv("PATH", "/usr/bin")
			t.Setenv("GOPATH", base+tc.toolsDir+"/go:/operator/go")
			t.Setenv("GOBIN", "")
			_, gotTools, env := evalPaths(t, shells(t)[0])
			if gotTools != "" {
				t.Errorf("TOOLS = %q, want empty", gotTools)
			}
			if env["GOPATH"] != "/operator/go" {
				t.Errorf("exported GOPATH = %q, want /operator/go", env["GOPATH"])
			}
		})
	}
}

func TestRunPaths_RefusesAConfigFolderTheServerRefuses(t *testing.T) {
	t.Setenv("KIRO_CONFIG_DIR", "/srv/c\n## forged")
	var out, errOut bytes.Buffer
	if rc := RunPaths(&out, &errOut); rc != 1 {
		t.Errorf("RunPaths = %d, want 1", rc)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing for the entrypoint to eval", out.String())
	}
	lines := strings.Split(strings.TrimSuffix(errOut.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "level=ERROR") || !strings.Contains(lines[0], "KIRO_CONFIG_DIR") {
		t.Errorf("stderr = %q, want one ERROR line naming KIRO_CONFIG_DIR", errOut.String())
	}
}

// kiro-cli ignores such a KIRO_HOME, so the boot says so once, in a line a value cannot forge.
func TestRunPaths_WarnsOnceAboutAnIgnoredKiroHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("KIRO_CONFIG_DIR", base+"/c")
	t.Setenv("KIRO_HOME", "state\nlevel=ERROR msg=forged")
	var out, errOut bytes.Buffer
	if rc := RunPaths(&out, &errOut); rc != 0 {
		t.Fatalf("RunPaths = %d, want 0; stderr: %s", rc, errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(errOut.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "reason=relative") {
		t.Errorf("stderr = %q, want one WARN line giving the reason", errOut.String())
	}
	if !strings.Contains(out.String(), "export KIRO_HOME="+shellQuote(base+"/.kiro")+"\n") {
		t.Errorf("stdout = %q, want KIRO_HOME exported as the default", out.String())
	}
}

// Every consumer reads the one verdict, so a rejected tools folder is off at each of them, even
// when the environment already puts it on PATH.
func TestToolsRoot_ARejectedFolderIsOffAtEveryConsumer(t *testing.T) {
	const goodDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	for _, tc := range []struct{ name, char string }{
		{"colon", ":"},
		{"backtick", "`"},
		{"NUL", "\x00"},
		{"carriage return", "\r"},
		{"line feed", "\n"},
		{"escape", "\x1b"},
		{"delete", "\x7f"},
		{"line separator", "\u2028"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("HOME", filepath.Join(base, "home"))
			t.Setenv("KIRO_HOME", "")
			configDir, rejected := filepath.Join(base, "config"), filepath.Join(t.TempDir(), "t"+tc.char+"x")
			env := map[string]string{
				"KIRO_CONFIG_DIR": configDir, "MAROTTE_TOOLS_DIR": rejected, "HOME": filepath.Join(base, "home"),
				"PATH":   rejected + "/bin:" + rejected + "/go/bin:/config/tools/bin:/usr/bin",
				"GOPATH": rejected + "/go", "GOBIN": rejected + "/bin",
			}
			lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
			root := resolveBootPaths(envx.Source{Get: func(k string) string { return env[k] }}).tools
			captureDefaultLogger(t)

			if dir, err := root.use(); dir != "" || !errors.Is(err, errToolsDirUnusable) {
				t.Fatalf("use() = %q, %v; want \"\", errToolsDirUnusable", dir, err)
			}
			path, gopath, gobin := toolsEnv(root, lookup)
			if path != "/usr/bin" || gopath.set || gobin.set {
				t.Errorf("toolsEnv = %q, %+v, %+v; want /usr/bin with GOPATH and GOBIN unset", path, gopath, gobin)
			}
			var out, errOut bytes.Buffer
			if rc := runPaths(&out, &errOut, lookup); rc != 0 {
				t.Fatalf("runPaths = %d; stderr: %s", rc, errOut.String())
			}
			for _, want := range []string{"TOOLS=''\n", "export PATH='/usr/bin'\n", "unset GOPATH\n", "unset GOBIN\n"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("paths output = %q, want it to hold %q", out.String(), want)
				}
			}
			cfg := &Config{
				WorkDir: filepath.Join(base, "work"), ConfigDir: configDir, Tools: root,
				KiroCLIVersion: "2.14.2", KiroCLISHA256: goodDigest, KiroCLISHA256ARM64: goodDigest,
			}
			kiro := startKiroCLI(t.Context(), cfg)
			defer kiro.stop()
			if kiro.ready == nil || kiro.rescan != nil || kiro.cliPath() != "" {
				t.Fatal("startKiroCLI did not return the unavailable runtime")
			}
			if ready, reason := kiro.ready(); ready || reason != pin.ReasonUnavailable {
				t.Errorf("kiro-cli ready() = (%v, %s), want (false, %s)", ready, reason, pin.ReasonUnavailable)
			}
			if _, err := buildToolsEngine(cfg, testRuntime(t), nil); !errors.Is(err, errToolsDirUnusable) {
				t.Errorf("buildToolsEngine = %v, want errToolsDirUnusable", err)
			}
			for _, r := range ownedRoots(cfg) {
				if r.role == roleToolsDir {
					t.Errorf("ownedRoots claims %q as the tools dir", r.dir)
				}
			}
			if tc.char != "\x00" {
				if err := os.Mkdir(rejected, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(rejected, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			roots, err := claimAndHealRoots(cfg)
			if err != nil {
				t.Fatalf("claimAndHealRoots = %v", err)
			}
			roots.release()
			if tc.char != "\x00" {
				if got := permOf(t, rejected); got != 0o777 {
					t.Errorf("the rejected folder's mode = %04o, want 0777 untouched", got)
				}
				if entries, err := os.ReadDir(rejected); err != nil || len(entries) != 0 {
					t.Errorf("the rejected folder holds %v (%v), want nothing", entries, err)
				}
			}
			newSteering(cfg).Generate(t.Context())
			doc, err := os.ReadFile(workspace.KiroSteeringPath("environment.md"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(doc), filepath.Dir(rejected)) || !strings.Contains(string(doc), "The tools engine is off") {
				t.Errorf("environment.md names the rejected folder or does not say the engine is off:\n%s", doc)
			}
		})
	}
}
