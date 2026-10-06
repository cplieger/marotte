// Environment screening for terminal/create. os/exec keeps the last value of a repeated
// key, so an agent-supplied variable wins; a few names redirect execution, which would let
// an approved command run code the user never approved.

package agent

import (
	"strings"
	"sync"

	"github.com/cplieger/envx/v2"
)

// envAllowVar lets an operator re-permit specific names, comma-separated.
const envAllowVar = "MAROTTE_ALLOW_AGENT_ENV"

// dangerousAgentEnv is kiro-cli's own `dangerous_env_vars`, verbatim off the 2.18.1
// binary, so an agent behaves the same in the TUI and here. Matched exactly: env vars are
// case-sensitive.
var dangerousAgentEnv = map[string]struct{}{
	// Spawn a helper program.
	"PAGER": {}, "EDITOR": {}, "VISUAL": {}, "BROWSER": {}, "MANPAGER": {},
	"GIT_PAGER": {}, "LESS": {}, "LESSOPEN": {}, "LESSCLOSE": {},
	// Inject code into any dynamically linked process.
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {},
	"DYLD_INSERT_LIBRARIES": {}, "DYLD_LIBRARY_PATH": {},
	// Run code when an interpreter starts.
	"PYTHONWARNINGS": {}, "PYTHONSTARTUP": {}, "PYTHONPATH": {}, "PYTHONHOME": {},
	"PERL5OPT": {}, "PERL5LIB": {}, "RUBYOPT": {}, "RUBYLIB": {},
	"NODE_OPTIONS": {}, "NODE_PATH": {},
	// Change what a bare command name resolves to, or what the shell runs first.
	"IFS": {}, "PATH": {}, "HOME": {}, "SHELL": {}, "PROMPT_COMMAND": {},
	"BASH_ENV": {}, "ENV": {},
	// git's own hook and helper hooks, each of which takes a command.
	"GIT_EDITOR": {}, "GIT_SEQUENCE_EDITOR": {}, "GIT_ASKPASS": {},
	"GIT_EXTERNAL_DIFF": {}, "GIT_SSH": {}, "GIT_SSH_COMMAND": {},
	"GIT_PROXY_COMMAND": {}, "GIT_EXEC_PATH": {}, "GIT_TEMPLATE_DIR": {},
}

// safeAgentEnvValues neutralise a dangerous name (upstream `safe_env_values`):
// `GIT_PAGER=cat` and `PAGER=` are how git paging is stopped, so they must pass.
var safeAgentEnvValues = map[string]struct{}{"": {}, "true": {}, "cat": {}}

// parseAllowedEnv turns the operator's comma-separated list into a set.
func parseAllowedEnv(raw string) map[string]struct{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := make(map[string]struct{})
	for name := range strings.SplitSeq(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// operatorAllowedEnv reads envAllowVar once, lazily.
var operatorAllowedEnv = sync.OnceValue(func() map[string]struct{} {
	return parseAllowedEnv(envx.String(envAllowVar))
})

// screenAgentEnv returns the names refused for the agent, in request order. It reports
// rather than filters, so the agent never believes a dropped variable was set.
func screenAgentEnv(vars []termEnvVar, allowed map[string]struct{}) []string {
	var blocked []string
	for _, v := range vars {
		if _, dangerous := dangerousAgentEnv[v.Name]; !dangerous {
			continue
		}
		if _, inert := safeAgentEnvValues[v.Value]; inert {
			continue
		}
		if _, ok := allowed[v.Name]; ok {
			continue
		}
		blocked = append(blocked, v.Name)
	}
	return blocked
}
