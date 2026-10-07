package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/procout"
)

const (
	jsonKeyOutput = httpreply.JSONKeyOutput
)

const (
	// diagnosticsMaxBytes caps the report returned to the browser; the rest is "[truncated]".
	diagnosticsMaxBytes = 256 * 1024

	// cliStderrCap bounds stderr capture in RunStdoutCapped (logged, never returned).
	cliStderrCap = 32 * 1024

	// settingsListMaxBytes is the hostile-output bound on the `settings list` document.
	settingsListMaxBytes = 64 * 1024
)

// CLIRunner abstracts subprocess execution for kiro-cli commands.
type CLIRunner interface {
	// Run executes the CLI and returns combined stdout+stderr.
	Run(ctx context.Context, args ...string) ([]byte, error)
	// RunStdoutCapped executes the CLI capturing STDOUT only, stopping at limit
	// bytes and reporting whether it truncated. stderr is captured separately,
	// bounded and logged — never returned, never merged into the result.
	RunStdoutCapped(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error)
}

// execCLIRunner is the production CLIRunner. The path is a FUNCTION: the install manager
// selects and can switch the active version after construction.
type execCLIRunner struct {
	cliPath func() string
	// env is the spawn's environment overlay (pinstall's Manager.PathEnv). Load-bearing:
	// `settings` re-execs kiro-cli-chat by a PATH search, which fails unless the version
	// directory leads PATH. nil inherits the parent environment.
	env func() []string
}

// command builds the spawn both methods run.
func (r *execCLIRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.cliPath(), args...) //nolint:gosec // G204: binary path from the install manager, never user input
	if r.env != nil {
		// The overlay lands LAST: os/exec keeps the last value for a repeated key.
		cmd.Env = append(os.Environ(), r.env()...)
	}
	return cmd
}

func (r *execCLIRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.command(ctx, args...).CombinedOutput()
}

func (r *execCLIRunner) RunStdoutCapped(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error) {
	stdout := procout.NewBuffer(limit)
	stderr := procout.NewBuffer(cliStderrCap)
	cmd := r.command(ctx, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if stderr.Len() > 0 {
		slog.Debug("cli stderr captured", "args", args, "stderr", logsafe.Field(stderr.String()))
	}
	return stdout.Bytes(), stdout.Truncated(), err
}

// cliTimeouts holds the timeout budget for each kiro-cli subprocess invocation.
type cliTimeouts struct {
	Version     time.Duration
	Diagnostics time.Duration
	Settings    time.Duration
}

// defaultCLITimeouts returns the production timeout budget.
func defaultCLITimeouts() cliTimeouts {
	return cliTimeouts{
		Version:     2 * time.Second,
		Diagnostics: 20 * time.Second,
		Settings:    3 * time.Second,
	}
}

// settingKind distinguishes boolean-only from numeric-only kiro-cli settings. settingInt has
// no user yet; it stays as the validation vocabulary for the next numeric setting.
type settingKind int

const (
	settingBool settingKind = iota
	settingInt
	_settingKindCount // must remain last — compile-time exhaustiveness guard
)

// Fails to compile if a settingKind is added without updating safeKiroSettingValueFor.
var _ = [1]struct{}{}[_settingKindCount-2]

const (
	kiroTrue  = "true"
	kiroFalse = "false"
)

// settingMeta carries validation metadata for an allowed kiro-cli setting.
type settingMeta struct {
	Seed string
	Kind settingKind
}

// allowedKiroSettings bounds what /api/kiro-settings can read and write. Only keys with a
// kiro-cli-SIDE role belong: KAS's ACP path reads no kiro-cli setting, so a chat change
// goes through internal/kascap's table instead.
var allowedKiroSettings = map[string]settingMeta{
	"chat.enableKnowledge":   {Kind: settingBool, Seed: kiroTrue},
	"chat.enableSubagent":    {Kind: settingBool, Seed: kiroTrue},
	"chat.enablePromptHints": {Kind: settingBool, Seed: kiroTrue},
	"hooks.showStatus":       {Kind: settingBool, Seed: kiroTrue},
	"telemetry.enabled":      {Kind: settingBool, Seed: kiroFalse},
	// cleanup.periodDays is NOT here: marotte owns chat retention and pins kiro-cli's purge off.
	"chat.disableInheritingDefaultResources": {Kind: settingBool, Seed: kiroFalse},
}

func safeKiroSetting(k string) string {
	if _, ok := allowedKiroSettings[k]; ok {
		return k
	}
	return ""
}

func safeKiroSettingValueFor(v string, kind settingKind) string {
	switch kind {
	case settingBool:
		if v == kiroTrue || v == kiroFalse {
			return v
		}
		return ""
	case settingInt:
		for _, c := range v {
			if c < '0' || c > '9' {
				return ""
			}
		}
		if v != "" && len(v) <= 4 {
			return v
		}
		return ""
	}
	return ""
}

// parseKiroSettingOutput strips the scope suffix kiro-cli appends to every
// non-empty setting value ("true (global)", "0 (local)") and returns the bare
// value. `before != ""` is the guard for a value that is entirely parenthesized,
// which is a value rather than a suffix.
func parseKiroSettingOutput(s string) string {
	s = strings.TrimSpace(s)
	if before, _, found := strings.CutLast(s, "("); found && before != "" {
		s = strings.TrimSpace(before)
	}
	return s
}

// settingsListArgs reads every kiro-cli setting in ONE invocation (one flat JSON object).
var settingsListArgs = []string{"settings", "list", "--format", "json"}

// parseKiroSettingsList maps the settings-list document to the string values the per-key
// form answers, keeping only allowlisted keys.
func parseKiroSettingsList(raw []byte) (map[string]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(allowedKiroSettings))
	for k, v := range obj {
		if safeKiroSetting(k) == "" {
			continue
		}
		out[k] = kiroSettingValueText(v)
	}
	return out, nil
}

// kiroSettingValueText renders one JSON setting value as the string the wire
// carries: a JSON string is unquoted, a bool or number is its own literal.
func kiroSettingValueText(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}

// kiroSettingsKeysParam is the ONE query parameter GET /api/kiro-settings reads.
// Named so unknownKiroSettingsQuery and the reader cannot disagree about it.
const kiroSettingsKeysParam = "keys"

// unknownKiroSettingsQuery reports whether q carries a parameter this endpoint does not
// read: ignoring one would fail OPEN to "the whole allowlist".
func unknownKiroSettingsQuery(q url.Values) bool {
	for name := range q {
		if name != kiroSettingsKeysParam {
			return true
		}
	}
	return false
}

// requestedKiroSettings resolves ?keys= to the allowlisted keys, sorted; absent means every
// key, and unknown names are dropped.
func requestedKiroSettings(spec string) []string {
	if strings.TrimSpace(spec) == "" {
		return slices.Sorted(maps.Keys(allowedKiroSettings))
	}
	var out []string
	for name := range strings.SplitSeq(spec, ",") {
		key := safeKiroSetting(strings.TrimSpace(name))
		if key == "" || slices.Contains(out, key) {
			continue
		}
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
