// Credential names the kiro-cli child would inherit are dropped and logged by name. A denylist, because build tools
// read an unenumerable environment; a drop, because refusing would refuse the chat. Agent terminals are screened
// elsewhere.

package bridge

import (
	"strings"
)

// EnvAllowVar is the operator override: comma-separated names to pass through anyway. Composition reads it; this
// package reads no environment. A name-shape denylist will catch a non-credential, and a guard with no way past it
// gets disabled.
const EnvAllowVar = "MAROTTE_ALLOW_BRIDGE_ENV"

// credentialEnvSuffixes catch the two conventional credential shapes, `*_TOKEN` and `*_SECRET`, which cover every
// forge and registry spelling (TestScreenBridgeEnv_DropsEveryNameTheDecisionNames pins them). Case-sensitive like
// POSIX: a case variant is a variable nobody reads.
var credentialEnvSuffixes = []string{"_TOKEN", "_SECRET"}

// KiroAPIKeyVar is kiro-cli's headless API-key credential; composition reports at boot when it is allowlisted.
const KiroAPIKeyVar = "KIRO_API_KEY" //nolint:gosec // G101: an environment variable NAME, not a credential

// credentialEnvNames are credentials ending in `_ID` or `_KEY`, suffixes too broad for rules. KAS prefers an env API
// key to the relay's login, so an inherited KiroAPIKeyVar replaces the signed-in identity. AWS_REGION, AWS_PROFILE
// and AWS_DEFAULT_REGION are configuration, so no `AWS_` prefix rule.
var credentialEnvNames = map[string]struct{}{
	"AWS_ACCESS_KEY_ID":     {},
	"AWS_SECRET_ACCESS_KEY": {},
	KiroAPIKeyVar:           {},
}

// ParseEnvAllowlist turns the operator's comma-separated EnvAllowVar value into a set; nil for blank input.
// Composition owns the environment read.
func ParseEnvAllowlist(raw string) map[string]struct{} {
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

func isCredentialEnv(name string, allowed map[string]struct{}) bool {
	if _, ok := allowed[name]; ok {
		return false
	}
	if _, ok := credentialEnvNames[name]; ok {
		return true
	}
	for _, suffix := range credentialEnvSuffixes {
		// A variable named exactly `_TOKEN` is read by nobody.
		if len(name) > len(suffix) && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// screenBridgeEnv composes the kiro-cli spawn environment: inherited minus credential-shaped names, then extra
// unfiltered, returning the dropped names in order. extra is marotte's own overlay (the active install leading PATH)
// and os/exec keeps the last value of a key, so filtering it could resolve PATH out of the wrong install.
func screenBridgeEnv(inherited, extra []string, allowed map[string]struct{}) (env, dropped []string) {
	env = make([]string, 0, len(inherited)+len(extra))
	for _, kv := range inherited {
		// `KEY=` is an assignment; an entry with no `=` (seen on exotic platforms) is classified by its whole text.
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			name = kv
		}
		if isCredentialEnv(name, allowed) {
			dropped = append(dropped, name)
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...), dropped
}
