// Package kirorelease is what is true of the kiro-cli release whatever installs it: its name,
// where its archives are published, how its version is probed and how its settings are
// asserted. It imports nothing from marotte, so a test in any package can reach it.
package kirorelease

import (
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/cplieger/toolbelt/v3/pin"
)

// noAutoUpdateVar turns kiro-cli's self-updater off under any HOME. Without it, or HOME's app.disableAutoupdates, the
// updater replaces the executable the process ran from when it exits, a pinned version directory included.
const noAutoUpdateVar = "KIRO_NO_AUTO_UPDATE"

// SpawnEnv is env with self-update turned off (KIRO_NO_AUTO_UPDATE=1) as its last entry, which wins over an earlier
// value because os/exec keeps a key's last one. Every kiro-cli process marotte starts takes its environment from
// here. env is not modified.
func SpawnEnv(env []string) []string {
	return append(slices.Clip(env), noAutoUpdateVar+"=1")
}

// SetCommandEnv sets cmd.Env to SpawnEnv(env), then PWD to cmd.Dir when Dir is set: os/exec adds that PWD only when
// Env is nil (go.dev/issue/50599), so the child would otherwise see the caller's PWD. Set cmd.Dir first.
func SetCommandEnv(cmd *exec.Cmd, env []string) {
	cmd.Env = SpawnEnv(env)
	if cmd.Dir == "" {
		return
	}
	if pwd, err := filepath.Abs(cmd.Dir); err == nil {
		cmd.Env = append(cmd.Env, "PWD="+pwd)
	}
}

// The release's fixed facts.
const (
	// Name is the release's name and its binary's file name.
	Name = "kiro-cli"
	// URLTemplate is the archive URL, with pin's {version} and {arch} placeholders.
	URLTemplate = "https://desktop-release.q.us-east-1.amazonaws.com/{version}/kirocli-{arch}.zip"
	// ArtifactDir is where the binary sits inside the archive.
	ArtifactDir = "kirocli/bin"
	// Notice is the licence acknowledgement logged once per install attempt.
	Notice = "kiro-cli is proprietary AWS Content; by installing it you accept the AWS Customer Agreement at https://kiro.dev/license/"
)

// ArchTokens maps a GOARCH to the token kiro-cli's archive URLs use. A fresh map per call, so no
// caller can change another's.
func ArchTokens() map[string]string {
	return map[string]string{"amd64": "x86_64-linux", "arm64": "aarch64-linux"}
}

// ProbeArgs makes the binary print its version.
func ProbeArgs() []string { return []string{"--version"} }

// AutoUpdateGate is the Required assertion that stops kiro-cli replacing the binary whose digest
// was verified: a failure withholds readiness.
func AutoUpdateGate() pin.Assertion {
	a := Setting("app.disableAutoupdates", "true")
	a.Required = true
	return a
}

// Setting is the assertion `kiro-cli settings <key> <value>`, named after its key.
func Setting(key, value string) pin.Assertion {
	return pin.Assertion{Name: key, Args: []string{"settings", key, value}}
}

// ShellEraDispatchers are the names a shell installer linked for kiro-cli, its 2.x sidecars
// included.
func ShellEraDispatchers() []string {
	return []string{Name, Name + "-chat", Name + "-term"}
}
