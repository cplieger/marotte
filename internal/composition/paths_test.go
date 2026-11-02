package composition

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/envx/v2"
)

func envSource(vars map[string]string) envx.Source {
	return envx.Source{Get: func(k string) string { return vars[k] }}
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
			if p.configDir != tc.wantConfig || p.toolsDir != tc.wantTD || p.toolsErr != nil {
				t.Errorf("resolveBootPaths(%v) = %q, %q, %v; want %q, %q, nil",
					tc.vars, p.configDir, p.toolsDir, p.toolsErr, tc.wantConfig, tc.wantTD)
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
			if p.toolsDir != tc.wantTools {
				t.Errorf("toolsDir = %q, want %q unchanged", p.toolsDir, tc.wantTools)
			}
			if !errors.Is(p.toolsErr, errToolsDirUnusable) {
				t.Fatalf("toolsErr = %v, want errToolsDirUnusable", p.toolsErr)
			}
			msg := p.toolsErr.Error()
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
			"no tools dir keeps an operator's GOPATH and GOBIN", "",
			map[string]string{"PATH": "/usr/bin", "GOPATH": "/g", "GOBIN": "/gb"},
			"/usr/bin",
			envValue{"/g", true},
			envValue{"/gb", true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }
			path, gopath, gobin := toolsEnv(tc.toolsDir, lookup)
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

// A tools folder the engine cannot use is still the kiro-cli install's, so TOOLS names it, but
// nothing of it reaches PATH, GOPATH or GOBIN.
func TestRunPaths_KeepsAnUnusableToolsFolderOffTheEnvironment(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base+"/home")
	t.Setenv("KIRO_CONFIG_DIR", base+"/c")
	t.Setenv("MAROTTE_TOOLS_DIR", base+"/a:b")
	t.Setenv("KIRO_HOME", "")
	t.Setenv("PATH", "/config/tools/bin:/config/tools/go/bin:/usr/bin")
	t.Setenv("GOPATH", "/config/tools/go")
	t.Setenv("GOBIN", "/config/tools/bin")
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			_, gotTools, env := evalPaths(t, sh)
			if gotTools != base+"/a:b" {
				t.Errorf("TOOLS = %q, want %q", gotTools, base+"/a:b")
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
