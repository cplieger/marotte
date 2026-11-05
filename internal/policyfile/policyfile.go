// Package policyfile reads and writes kiro-cli's native Cedar permission files for the user and
// workspace scopes, under kiro-cli's state directory: <kiroHome>/settings/permissions.yaml and
// <kiroHome>/workspace-roots/<workspace.SessionsBucket>/permissions.yaml. KAS also writes the user file
// for a user-scope consent, and hot-reloads both, so a write reaches every live session. Load
// accepts block YAML or JSON; Save writes new and changed rules as block YAML (KAS 2.12) and keeps
// the other keys, their order and comments.
package policyfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/go/yamlenv"
	"github.com/cplieger/marotte/internal/workspace"
	"go.yaml.in/yaml/v4"
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

// File is the part of a permissions file marotte owns, its rules. Save writes them into the
// file's own document, so every other top-level key (KAS's `policies`, the TUI's
// `workspaceTrusted` and `workspaceTrustCase`), their order and comments survive: dropping one
// silently narrows grants or re-asks for workspace trust.
type File struct {
	Rules []Rule `yaml:"rules"`
}

// rulesKey is the one top-level key Save rewrites.
const rulesKey = "rules"

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

// Load reads and parses the permissions file KAS would read for path (see readSource). Nothing to
// read yields an empty File. Parse failures and oversize files are errors, so a hand-authored file
// is never clobbered.
func Load(path string) (*File, error) {
	data, name, err := readSource(context.Background(), path)
	if err != nil {
		return nil, err
	}
	_, f, err := parse(data, name)
	return f, err
}

// readSource returns the bytes and base name of the file KAS reads for path: path itself, or its
// permissions.json sibling when path is a missing or blank permissions.yaml. Nothing to read yields
// nil bytes.
func readSource(ctx context.Context, path string) (data []byte, name string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s: %w", filename, err)
	}
	if data, err = readPolicy(ctx, abs); err != nil {
		return nil, "", err
	}
	name = filepath.Base(abs)
	if strings.TrimSpace(string(data)) == "" && name == filename {
		name = jsonFilename
		if data, err = readPolicy(ctx, filepath.Join(filepath.Dir(abs), name)); err != nil {
			return nil, "", err
		}
	}
	return data, name, nil
}

// readPolicy returns abs's bytes, nil for a missing file. Through filesafe.OpenRegular: a FIFO
// at the name (the agent can write $HOME/.kiro) would block os.ReadFile forever, the size is
// checked off the descriptor before reading, and a final-component symlink is refused as Save
// refuses it.
func readPolicy(ctx context.Context, abs string) ([]byte, error) {
	fh, _, err := filesafe.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	data, err := filesafe.ReadBoundedFile(ctx, fh, maxPolicyFileSize)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(abs), err)
	}
	return data, nil
}

// parse decodes data, read from the file called name, into its document node and its rules. doc
// is nil for a blank file, and has Kind 0 for one holding only comments. The root must be a
// mapping or null; go-yaml refuses a duplicated key.
func parse(data []byte, name string) (doc *yaml.Node, f *File, err error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, &File{Rules: []Rule{}}, nil
	}
	// Reject a multi-document file loudly: yaml.Unmarshal reads only the
	// first document, so everything below a stray "---" would half-load
	// here and then be silently dropped by the next Save — silent rule
	// loss in a security policy file.
	if err := yamlenv.CheckSingleDocument(data); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", name, err)
	}
	doc = &yaml.Node{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", name, err)
	}
	f = &File{}
	if doc.Kind == yaml.DocumentNode {
		if root := doc.Content[0]; root.Kind != yaml.MappingNode && root.ShortTag() != "!!null" {
			return nil, nil, fmt.Errorf("parse %s: %w", name, errNotAMapping)
		}
		if err := doc.Decode(f); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	return doc, f, nil
}

// Save replaces the rules of the file Load reads for path with f.Rules, keeping every other key,
// their order and comments (kiro-cli's workspace-trust record and policies list live there). It
// refuses to overwrite a file Load would refuse. The write is atomic (temp, fsync, rename,
// dir-fsync), mode 0600, parent dirs 0700, so a crash never leaves a truncated policy.
//
// KAS and kiro-cli write this file under a lock marotte does not take. A key they wrote before
// Save's read survives; one written between that read and the rename is lost, as is a rules edit
// made after the caller's Load (last writer wins on rules).
func Save(ctx context.Context, path string, f *File) error {
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	old, name, err := readSource(ctx, path)
	if err != nil {
		return err
	}
	doc, _, err := parse(old, name)
	if err != nil {
		return err
	}
	// go-yaml drops the comment on a `...` line and every comment after one; the same document
	// without its end markers parses with them.
	if text := dropDocumentEnds(string(old)); text != string(old) {
		old = []byte(text)
		if doc, _, err = parse(old, name); err != nil {
			return err
		}
	}
	if name == jsonFilename && doc != nil {
		// Load read the .json sibling: path is written fresh from its content, all block YAML since
		// JSON holds no comments, and the .json is left alone; KAS reads the .yaml over it from now on.
		blockStyle(doc)
	}
	data, err := render(old, doc, f.Rules)
	if err != nil {
		return err
	}
	// An edit that breaks the document (an alias into a dropped rule), would read back with other
	// rules, or retargets any alias to an earlier anchor of the same name fails here, not at KAS's
	// next reload.
	if backErr := readsBack(data, filepath.Base(path), f.Rules); backErr != nil {
		return backErr
	}
	if doc != nil {
		if aliasErr := aliasesHold(doc); aliasErr != nil {
			return fmt.Errorf("refusing to write %s: %w", filepath.Base(path), aliasErr)
		}
	}
	// WithMaxBytes mirrors Load's size guard: Save can never persist a
	// policy file its own Load would reject as oversize.
	_, err = filesafe.WriteFile(ctx, path, data,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700),
		filesafe.WithMaxBytes(maxPolicyFileSize))
	return err
}

// render writes rules into the parsed document doc of old, which holds no `...` line. A blank file
// gets a fresh `rules:` document; one holding only comments, `---` or a directive keeps its text
// above it, since go-yaml drops those from an empty document.
// Stays on yaml.Marshal, which is Dump with WithV3Defaults and WithLineWidth(-1): an Encoder or a
// bare Dump wraps at 80 columns and would refold kiro-cli's long values.
func render(old []byte, doc *yaml.Node, rules []Rule) ([]byte, error) {
	fresh := &File{Rules: rules}
	if doc == nil {
		return yaml.Marshal(fresh)
	}
	if doc.Kind == 0 || emptyRoot(doc.Content[0]) {
		out, err := yaml.Marshal(fresh)
		if err != nil {
			return nil, err
		}
		head := strings.TrimRight(string(old), " \t\r\n") + "\n"
		return append([]byte(head), out...), nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		// A null root (`~`): parse refuses every other non-mapping root. go-yaml writes no line
		// comment on a root block mapping, so `~ # side` keeps its comment as a head line.
		root = &yaml.Node{
			Kind: yaml.MappingNode, Tag: "!!map",
			HeadComment: joinComments("\n", root.HeadComment, root.LineComment), FootComment: root.FootComment,
		}
		doc.Content[0] = root
	}
	// A JSON-shaped root would force the new rules into flow style; the other values keep theirs.
	root.Style &^= yaml.FlowStyle
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != rulesKey {
			continue
		}
		key, prev := root.Content[i], root.Content[i+1]
		value, err := rulesNode(prev, rules)
		if err != nil {
			return nil, err
		}
		value.HeadComment, value.FootComment = prev.HeadComment, prev.FootComment
		// go-yaml reads `rules: [] # x` onto the value and `rules: # x` over a block onto the key,
		// and writes a key's line comment over `[]` onto the next key.
		line := joinComments(" ", key.LineComment, prev.LineComment)
		if len(rules) == 0 {
			key.LineComment, value.LineComment = "", line
		} else {
			key.LineComment = line
		}
		root.Content[i+1] = value
		return yaml.Marshal(doc)
	}
	value, err := rulesNode(nil, rules)
	if err != nil {
		return nil, err
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: rulesKey}
	root.Content = append(root.Content, key, value)
	return yaml.Marshal(doc)
}

// rulesNode encodes rules as a block sequence (`[]` when empty), reusing the file's item node of
// each unchanged rule so its comments and any field marotte does not model survive. Unchanged is
// every field equal in order; a key whose count differs between the file and rules is re-encoded
// whole, since which of its duplicates went is unknowable and a reused node would carry the
// removed one's comment.
func rulesNode(prev *yaml.Node, rules []Rule) (*yaml.Node, error) {
	exact := func(r *Rule) string { return ruleKey(r.Capability, r.Effect, r.Match, r.Exclude) }
	kept := map[string][]*yaml.Node{}
	if prev != nil && prev.Kind == yaml.SequenceNode {
		for _, item := range prev.Content {
			var r Rule
			if err := item.Decode(&r); err != nil {
				return nil, err
			}
			k := exact(&r)
			kept[k] = append(kept[k], item)
		}
	}
	want := map[string]int{}
	for i := range rules {
		want[exact(&rules[i])]++
	}
	value := &yaml.Node{}
	if err := value.Encode(rules); err != nil {
		return nil, err
	}
	for i := range rules {
		k := exact(&rules[i])
		if items := kept[k]; len(items) > 0 && len(items) == want[k] {
			value.Content[i] = items[0]
			kept[k] = items[1:]
			want[k]--
		}
	}
	return value, nil
}

// aliasesHold reports an alias in doc that would read back as another value: in document order,
// each alias must name the anchored node it was parsed with, which a dropped rule holding that
// anchor, or a reordered one, would change.
func aliasesHold(doc *yaml.Node) error {
	latest := map[string]*yaml.Node{}
	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode {
			if latest[n.Value] != n.Alias {
				return fmt.Errorf("the alias *%s would point at another value", n.Value)
			}
			return nil
		}
		if n.Anchor != "" {
			latest[n.Anchor] = n
		}
		for _, c := range n.Content {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(doc)
}

// readsBack refuses data, the text Save is about to write to the file called name, unless it
// parses and holds exactly rules: a file KAS would read with other rules is never written.
func readsBack(data []byte, name string, rules []Rule) error {
	_, back, err := parse(data, name)
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", name, err)
	}
	same := func(a, b Rule) bool {
		return ruleKey(a.Capability, a.Effect, a.Match, a.Exclude) == ruleKey(b.Capability, b.Effect, b.Match, b.Exclude)
	}
	if !slices.EqualFunc(back.Rules, rules, same) {
		return fmt.Errorf("refusing to write %s: it would read back with other rules", name)
	}
	return nil
}

// blockStyle clears every node's style but its explicit tag, so a document read from JSON writes
// as block YAML: an untagged string that would read as another type is quoted, a tagged one keeps
// its tag, and emptyRoot tells a root tagged `!!null` with an empty value from an empty document
// by it.
func blockStyle(n *yaml.Node) {
	n.Style &= yaml.TaggedStyle
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// emptyRoot reports an implicit null root (`---` with nothing after it). An explicit `~` or
// `!!null` cannot stay above the new mapping, so it takes the mapping path.
func emptyRoot(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null" && n.Value == "" && n.Style&yaml.TaggedStyle == 0
}

// dropDocumentEnds removes each `...` line, keeping a comment on it. In a document parse accepts,
// such a line at column 0 is always a document end: a quoted scalar refuses it and a block scalar
// under a top-level key is indented.
func dropDocumentEnds(text string) string {
	var kept []string
	for line := range strings.SplitSeq(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "...")
		if ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			if rest = strings.TrimLeft(rest, " \t"); rest == "" {
				continue
			}
			if rest[0] == '#' {
				line = rest
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func joinComments(sep, a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + sep + b
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
// sorted match + sorted exclude. Mirrors KAS's rule key (a JSON string of the
// same four, KAS 3.0.0), so marotte's notion of "same rule" matches the engine's.
func signature(r *Rule) string {
	m := slices.Clone(r.Match)
	e := slices.Clone(r.Exclude)
	slices.Sort(m)
	slices.Sort(e)
	return ruleKey(r.Capability, r.Effect, m, e)
}

// ruleKey quotes each field, so a pattern holding a comma or a quote never collides with a
// different split of patterns.
func ruleKey(capability, effect string, match, exclude []string) string {
	var b strings.Builder
	b.WriteString(strconv.Quote(capability))
	b.WriteString(strconv.Quote(effect))
	b.WriteString("m")
	for _, p := range match {
		b.WriteString(strconv.Quote(p))
	}
	b.WriteString("x")
	for _, p := range exclude {
		b.WriteString(strconv.Quote(p))
	}
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
