// Package policyfile reads and writes kiro-cli's native Cedar permission files for the user and
// workspace scopes, under kiro-cli's state directory: <kiroHome>/settings/permissions.yaml and
// <kiroHome>/workspace-roots/<workspace.SessionsBucket>/permissions.yaml. KAS also writes the user file
// for a user-scope consent, and hot-reloads both, so a write reaches every live session. Load
// accepts block YAML or JSON; Save writes block YAML (KAS 2.12).
package policyfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/envx/yamlenv/v2"
	"github.com/cplieger/marotte/internal/workspace"
	"go.yaml.in/yaml/v3"
)

// KAS reads the .yaml unless it is missing or blank, then the .json sibling. marotte writes the
// .yaml only, so Load falls back to the .json and the first Save carries its content over.
const (
	filename     = "permissions.yaml"
	jsonFilename = "permissions.json"
)

// Limits guarding hand-edited / API-supplied rule payloads so a pathological
// input can't bloat the file or the CLI argv. Real rules are far smaller.
const (
	maxMatchEntries   = 128
	maxPatternLen     = 512
	maxRulesPerFile   = 512
	maxPolicyFileSize = 1 << 20 // 1 MiB — the policy file is tiny in practice
)

// Rule is one policy rule. Field order (capability, effect, match, exclude) is
// the canonical KAS order. YAML tags only: the REST layer decodes into its own
// policyRuleBody, so nothing encodes a Rule as JSON.
type Rule struct {
	Capability string   `yaml:"capability"`
	Effect     string   `yaml:"effect"`
	Match      []string `yaml:"match,omitempty"`
	Exclude    []string `yaml:"exclude,omitempty"`
}

// File is the whole permissions document. Rules is the one key marotte edits; every other
// top-level key (KAS's `policies`, the TUI's `workspaceTrusted`) is kept as read and written back
// by Save, since dropping one silently narrows grants or re-asks for workspace trust.
type File struct {
	Rules []Rule
	// rest holds the other top-level key and value nodes, in file order.
	rest []*yaml.Node
}

const rulesKey = "rules"

// UnmarshalYAML splits the top-level mapping into Rules and the keys File does not model.
func (f *File) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errNotAMapping
	}
	seen := make(map[string]struct{}, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if _, dup := seen[key.Value]; dup {
			return fmt.Errorf("%w: %q", errDuplicateKey, key.Value)
		}
		seen[key.Value] = struct{}{}
		if key.Value == rulesKey {
			if err := val.Decode(&f.Rules); err != nil {
				return err
			}
			continue
		}
		f.rest = append(f.rest, key, val)
	}
	return nil
}

// MarshalYAML writes rules first, then every kept key in the order Load read it.
func (f *File) MarshalYAML() (any, error) {
	var rules yaml.Node
	if err := rules.Encode(f.Rules); err != nil {
		return nil, err
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: rulesKey}, &rules)
	out.Content = append(out.Content, f.rest...)
	return out, nil
}

// Scope names marotte can write. Kiro/administration are read-only
// baselines; agent comes from the agent profile; session is runtime.
const (
	ScopeUser      = "user"
	ScopeWorkspace = "workspace"
)

// Effect values.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
	EffectAsk   = "ask"
	// EffectEvaluate hands a call to KAS's trust classifier; KAS skips the rule unless the engine runs
	// with KIRO_TRUST_CLASSIFIER_MODE=enforce.
	EffectEvaluate = "evaluate"
)

// suggestedCapabilities seeds the UI picker, its only job: a hand-copied snapshot of KAS's internal
// VALID_CAPABILITIES with no discovery method. It never validates (SanitizeRule), and the view
// handler unions in every capability KAS's rules use, so staleness costs a dropdown entry, not a
// refused write.
var suggestedCapabilities = map[string]struct{}{
	"all": {}, "builtin": {}, "filesystem": {},
	"fs_read": {}, "fs_write": {}, "shell": {},
	"web_fetch": {}, "web_search": {}, "mcp": {},
	"subagent": {}, "skill": {}, "power": {},
	"context": {}, "diagnostics": {}, "sandbox_network": {},
}

// Capabilities returns the suggested capability set, sorted, for the UI picker.
// It is a starting point, not a permitted set: a caller may write a rule naming
// a capability that is not in here.
func Capabilities() []string {
	out := make([]string, 0, len(suggestedCapabilities))
	for c := range suggestedCapabilities {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// KAS's own names are under 16 characters; this is generous headroom, and its job is only to keep a
// pathological value out of the file (see SanitizeRule).
const maxCapabilityLen = 128

// Errors surfaced to the HTTP edge. There is no "unknown capability" error: the vocabulary is
// KAS's. KAS skips such a rule non-fatally and reports it at the session door
// (_meta.policyErrors) and in _kiro/policy/changed; GET /api/permissions carries the door's
// report as load_errors and the client renders it.
var (
	errInvalidScope    = errors.New("scope must be user or workspace")
	errInvalidEffect   = errors.New("effect must be allow, deny, ask, or evaluate")
	errCapabilityShape = errors.New("capability must be a non-empty token with no control characters")
	errTooManyRules    = errors.New("policy file has too many rules")
	errPatternTooLong  = errors.New("match/exclude pattern too long")
	errPatternInvalid  = errors.New("match/exclude pattern contains invalid characters")
	errPatternEmpty    = errors.New("match/exclude list has no non-empty pattern")
	errNotAMapping     = errors.New("the document must be a mapping of keys")
	errDuplicateKey    = errors.New("duplicate top-level key")
)

// ValidScope reports whether scope is writable by marotte.
func ValidScope(scope string) bool {
	return scope == ScopeUser || scope == ScopeWorkspace
}

// ValidEffect reports whether effect is allow/deny/ask/evaluate.
func ValidEffect(effect string) bool {
	return effect == EffectAllow || effect == EffectDeny || effect == EffectAsk || effect == EffectEvaluate
}

// PresetSourcePrefix starts the source KAS stamps on a rule resolved from a preset:
// `preset:<id>` at session scope, `preset:<id> in <file>` for a file's `policies:` entry.
const PresetSourcePrefix = "preset:"

// Editable reports whether marotte's rule writer can change or remove a live rule: a writable
// scope, an effect the writer knows, and a rule written under `rules:` rather than resolved from
// a preset the file references.
func Editable(scope, source, effect string) bool {
	return ValidScope(scope) && ValidEffect(effect) && !strings.HasPrefix(source, PresetSourcePrefix)
}

// Roots are the two filesystem roots a permissions.yaml path resolves against. A struct because a
// transposition of two path strings would write a security policy under the wrong root with nothing
// to detect it.
type Roots struct {
	// KiroHome is kiro-cli's state directory (workspace.KiroHome), the base KAS resolves both
	// scopes from.
	KiroHome string
	// WorkDir is the bridge's cwd, needed only for the workspace scope.
	WorkDir string
}

// PathFor returns the permissions.yaml path for the given scope.
//
// scope stays a separate parameter: it is the discriminator this switches on,
// and confusing it with a root is already loud (a path is not "user" or
// "workspace", so it returns errInvalidScope). The silent mistake was the pair.
func PathFor(scope string, roots Roots) (string, error) {
	switch scope {
	case ScopeUser:
		return filepath.Join(roots.KiroHome, "settings", filename), nil
	case ScopeWorkspace:
		return filepath.Join(roots.KiroHome, "workspace-roots",
			workspace.SessionsBucket(roots.WorkDir), filename), nil
	default:
		return "", errInvalidScope
	}
}

// Load reads and parses the permissions file KAS would read for path: path itself, or its
// permissions.json sibling when path is a missing or blank permissions.yaml. Nothing to read
// yields an empty File. Parse failures and oversize files are errors, so a hand-authored file is
// never clobbered.
func Load(path string) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", filename, err)
	}
	data, err := readPolicy(abs)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(abs)
	if strings.TrimSpace(string(data)) == "" && name == filename {
		name = jsonFilename
		if data, err = readPolicy(filepath.Join(filepath.Dir(abs), name)); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(string(data)) == "" {
		return &File{Rules: []Rule{}}, nil
	}
	// Reject a multi-document file loudly: yaml.Unmarshal reads only the
	// first document, so everything below a stray "---" would half-load
	// here and then be silently dropped by the next Save — silent rule
	// loss in a security policy file.
	if err := yamlenv.CheckSingleDocument(data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	return &f, nil
}

// readPolicy returns abs's bytes, nil for a missing file. Through atomicfile.OpenRegular: a FIFO
// at the name (the agent can write $HOME/.kiro) would block os.ReadFile forever, the size is
// checked off the descriptor before reading, and a final-component symlink is refused as Save
// refuses it.
func readPolicy(abs string) ([]byte, error) {
	fh, _, err := atomicfile.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	data, err := atomicfile.ReadBoundedFile(context.Background(), fh, maxPolicyFileSize)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(abs), err)
	}
	return data, nil
}

// Save writes the file atomically (temp → fsync → rename → dir-fsync) via
// cplieger/atomicfile, mode 0600 (policy is sensitive), creating parent dirs
// at 0700. A crash mid-write can never leave a truncated policy file (which
// would silently drop every rule at the next KAS reload).
func Save(ctx context.Context, path string, f *File) error {
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	// WithMaxBytes mirrors Load's size guard: Save can never persist a
	// policy file its own Load would reject as oversize.
	_, err = atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700),
		atomicfile.WithMaxBytes(maxPolicyFileSize))
	return err
}

// SanitizeRule validates and normalizes a rule for writing: trims and de-dups match/exclude,
// enforces the caps, checks the effect and the capability's SHAPE (not empty, oversized or
// control-bearing). Its VOCABULARY is KAS's to judge. It does not default the effect; the caller
// applies "default to ask".
func SanitizeRule(r *Rule) (Rule, error) {
	capability := strings.TrimSpace(r.Capability)
	if capability == "" || len(capability) > maxCapabilityLen ||
		!utf8.ValidString(capability) || strings.ContainsFunc(capability, isCtrl) {
		return Rule{}, errCapabilityShape
	}
	if !ValidEffect(r.Effect) {
		return Rule{}, errInvalidEffect
	}
	match, err := sanitizePatterns(r.Match)
	if err != nil {
		return Rule{}, err
	}
	exclude, err := sanitizePatterns(r.Exclude)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Capability: capability, Effect: r.Effect, Match: match, Exclude: exclude}, nil
}

func sanitizePatterns(in []string) ([]string, error) {
	if len(in) > maxMatchEntries {
		return nil, errTooManyRules
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxPatternLen {
			return nil, errPatternTooLong
		}
		if !utf8.ValidString(p) || strings.ContainsFunc(p, isCtrl) {
			return nil, errPatternInvalid
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	// A list that emptied itself is not an ABSENT list: Rule.Match is omitempty, and a
	// match-less rule is the broadest grant (KAS reads it as `**`), so `match:[""]`
	// would be written as "matches everything". Keyed on len(in), so a bare rule
	// stays writable.
	if len(in) > 0 && len(out) == 0 {
		return nil, errPatternEmpty
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// isCtrl reports whether r is a control character (unicode.Cc, all 65: C0, DEL and C1), which a
// capability or pattern may not hold. A hand-rolled C0-and-DEL check let C1 through, and this is a
// refuse gate, so a miss fails open; U+0085 is a line break to many renderers. Cc is fixed and
// cannot gain members.
func isCtrl(r rune) bool { return unicode.IsControl(r) }

// signature is the dedup/equality key for a rule: capability + effect +
// sorted match + sorted exclude. Mirrors KAS ruleSignature so marotte's
// notion of "same rule" matches the engine's.
func signature(r *Rule) string {
	m := slices.Clone(r.Match)
	e := slices.Clone(r.Exclude)
	slices.Sort(m)
	slices.Sort(e)
	var b strings.Builder
	b.WriteString(r.Capability)
	b.WriteByte('|')
	b.WriteString(r.Effect)
	b.WriteString("|m:")
	b.WriteString(strings.Join(m, ","))
	b.WriteString("|x:")
	b.WriteString(strings.Join(e, ","))
	return b.String()
}

// Has reports whether an identical rule (by signature) already exists.
func (f *File) Has(r *Rule) bool {
	sig := signature(r)
	for i := range f.Rules {
		if signature(&f.Rules[i]) == sig {
			return true
		}
	}
	return false
}

// Upsert appends r if no identical rule exists. Returns true if the file
// changed. Discrete-rule semantics (no auto-merge into match arrays) so each
// editor rule maps to exactly one removable YAML rule — predictable for a
// security-sensitive file.
func (f *File) Upsert(r *Rule) (bool, error) {
	if len(f.Rules) >= maxRulesPerFile {
		return false, errTooManyRules
	}
	if f.Has(r) {
		return false, nil
	}
	f.Rules = append(f.Rules, *r)
	return true, nil
}

// Remove deletes the first rule matching r by signature. Returns true if a
// rule was removed.
func (f *File) Remove(r *Rule) bool {
	sig := signature(r)
	for i := range f.Rules {
		if signature(&f.Rules[i]) == sig {
			// slices.Delete, not append(a[:i], a[i+1:]...): it zeroes the vacated
			// tail, so the removed rule's Match and Exclude slices are not still
			// reachable through the backing array of a security policy the caller
			// is about to hand to Save.
			f.Rules = slices.Delete(f.Rules, i, i+1)
			return true
		}
	}
	return false
}

// ReplaceEffect changes the effect of the rule matching old (by signature)
// IN PLACE, preserving its position in the file — one atomic mutation, so
// an in-place edit can never half-apply the way a client-side remove+add
// could. When the resulting rule would duplicate an existing one, the old
// rule is removed instead (the duplicate already expresses the target
// state). Returns true if the file changed: false means the old rule is
// absent or the effect is already effect.
func (f *File) ReplaceEffect(old *Rule, effect string) bool {
	sig := signature(old)
	idx := -1
	for i := range f.Rules {
		if signature(&f.Rules[i]) == sig {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	next := f.Rules[idx]
	next.Effect = effect
	nextSig := signature(&next)
	if nextSig == sig {
		return false // same effect — nothing to change
	}
	for i := range f.Rules {
		if i != idx && signature(&f.Rules[i]) == nextSig {
			f.Rules = slices.Delete(f.Rules, idx, idx+1)
			return true
		}
	}
	f.Rules[idx].Effect = effect
	return true
}
