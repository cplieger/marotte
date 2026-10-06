package kascap

import (
	"flag"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// updateCensus rewrites the census fixture (and the version pin) from the local
// bundle. Spelled as a flag rather than the repo's UPDATE_GOLDEN env gate
// because regenerating this fixture is a review action, not a formatting one:
// every line it adds is a capability somebody has to judge.
var updateCensus = flag.Bool("update", false,
	"rewrite testdata/unclaimed.txt and testdata/kas-version.txt from the local agent-server bundle")

const (
	kasVersionPath  = "testdata/kas-version.txt"
	unclaimedPath   = "testdata/unclaimed.txt"
	entrypointPath  = "../../entrypoint.sh"
	censusUpdateCmd = "go test ./internal/kascap/ -run 'TestCapabilityCensus|TestAbsentTrueMatchesTheBundle' -update"

	// bindingScopeReach bounds how far back bindingScope looks for the block
	// that encloses a binding.
	bindingScopeReach = 64 << 10

	// jsIdent matches one JavaScript identifier; every anchor is built from it because the bundle's
	// identifiers are mangled.
	jsIdent = `[A-Za-z_$][A-Za-z0-9_$]*`
)

var (
	kiroCLIVersionRe = regexp.MustCompile(`(?m)^KIRO_CLI_VERSION="([^"]+)"`)

	// capResolverRe names the mangled resolveCapabilities, the one function mapping the whole
	// _meta.kiro block onto a resolved struct, anchored on property names that survive mangling.
	capResolverRe = regexp.MustCompile(
		`resolvedCapabilities\s*=\s*(` + jsIdent + `)\(\s*` + jsIdent + `\.clientCapabilities\s*\)`,
	)

	// clientMetaReadRe matches a read off the agent's stored copy, this.clientMeta, assigned only
	// from the initialize block.
	clientMetaReadRe = regexp.MustCompile(`\bclientMeta\??\.(` + jsIdent + `)`)

	// initBindRe matches a site binding a local to the client's _meta.kiro block; group 1 is the
	// local. The receiver is wildcarded.
	initBindRe = regexp.MustCompile(
		`(` + jsIdent + `)\s*=\s*` + jsIdent + `\.clientCapabilities\?\._meta\?\.kiro`,
	)

	// settingEnabledFnRe names the mangled isSettingEnabled by its BODY, since the module-local
	// identifier does not survive.
	settingEnabledFnRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\(` + jsIdent + `,\s*` + jsIdent +
			`\)\s*\{\s*(?:let|var|const)\s+` + jsIdent + `\s*=\s*` + jsIdent +
			`\[` + jsIdent + `\];\s*return typeof\s+` + jsIdent +
			`\s*==\s*"object"\s*&&\s*` + jsIdent + `\s*!==\s*null\s*\?\s*` +
			jsIdent + `\.enabled\s*:\s*!1\s*\}`,
	)

	// settingEnabledRe is the direct settings gate (absent means false) for a bundle that still
	// spells the callee out.
	settingEnabledRe = regexp.MustCompile(`isSettingEnabled\([^,()]*,\s*["'](` + jsIdent + `)["']\)`)

	// featureEnabledFnRe names the mangled isFeatureEnabled wrapper through the model-config
	// provider method it forwards to.
	featureEnabledFnRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\(` + jsIdent + `\)\s*\{\s*return\s+` +
			jsIdent + `\.isFeatureEnabled\(` + jsIdent + `\)\s*\}`,
	)

	// featureEnabledRe is the FEATURE-FLAG gate: isFeatureEnabled(key) resolves through the model
	// config, a reader shape the census once missed.
	featureEnabledRe = regexp.MustCompile(`isFeatureEnabled\(["'](` + jsIdent + `)["']\)`)

	// settingResolverRe is the resolver family, which reads the same block but applies a per-key
	// default; the receiver is a wildcard.
	settingResolverRe = regexp.MustCompile(jsIdent + `\.data\.(` + jsIdent + `)\?\.enabled`)

	// settingDestructureRe is the resolver family's second spelling, for resolvers with a compound
	// value (resolveSpecPlan, resolveSessionEviction).
	settingDestructureRe = regexp.MustCompile(
		`(?:let|var|const)\s+` + jsIdent + `\s*=\s*` + jsIdent + `\.data\.(` + jsIdent + `)[,;]`,
	)

	// settingComputedFnRe names the generic resolver reading a settings key through a computed
	// member, `function f(e,t,r){return e.data[t]?.enabled??r}`, by its body.
	settingComputedFnRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\(` + jsIdent + `,\s*` + jsIdent + `,\s*` + jsIdent +
			`\)\s*\{\s*return\s+` + jsIdent + `\.data\[` + jsIdent + `\]\?\.enabled\s*\?\?\s*` +
			jsIdent + `\s*;?\s*\}`,
	)

	// absentTrueResolverRe is settingResolverRe's subset whose fallback ends in a literal true, so
	// an ABSENT key resolves TRUE.
	absentTrueResolverRe = regexp.MustCompile(
		jsIdent + `\.data\.(` + jsIdent + `)\?\.enabled\s*\?\?[^;]*?\?\?\s*(?:true|!0)`,
	)
)

// activeKASVersion is the kiro-cli version this repo runs, read from the pin the
// image build and Renovate both use. Deliberately not "whatever is newest in the
// local cache": the census is only meaningful against the version marotte ships.
func activeKASVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointPath, err)
	}
	m := kiroCLIVersionRe.FindSubmatch(raw)
	if m == nil {
		t.Fatalf(`no KIRO_CLI_VERSION="..." pin in %s.
The census resolves the active agent server from that pin; if the pin moved,
point this test at its new home rather than guessing from the local cache.`, entrypointPath)
	}
	return string(m[1])
}

// loadBundle returns the pinned agent server's source, skipping when it is not installed locally and
// failing when the pin has moved under the fixture. A bundle missing from ~/.local/share/kiro-cli/kas/
// is unpacked as below; `env -u` stops an inherited KIRO_KAS_* path serving another tree instead.
//
//	curl -fsSLO https://desktop-release.q.us-east-1.amazonaws.com/<version>/kirocli-x86_64-linux.zip
//	unzip -q kirocli-x86_64-linux.zip
//	env -u KIRO_KAS_SERVER_PATH -u KIRO_KAS_NODE_PATH HOME=<scratch> ./kirocli/bin/kiro-cli-chat acp --agent-engine v3 </dev/null
//	HOME=<scratch> <censusUpdateCmd>
func loadBundle(t *testing.T) string {
	t.Helper()
	active := activeKASVersion(t)
	raw, path := bundleSource(t, active)

	pinnedRaw, err := os.ReadFile(kasVersionPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with: %s): %v", kasVersionPath, censusUpdateCmd, err)
	}
	pinned := strings.TrimSpace(string(pinnedRaw))

	// The stale-fixture check runs AFTER the bundle lookup, so a machine without the bundle skips;
	// TestCensusFixture_MatchesThePin gates fixture against pin without one.
	if pinned != active && !*updateCensus {
		t.Fatalf(`census fixture was generated against kiro-cli %s, active is %s.
A version bump can add, rename or drop a client capability, so this fixture has
to be RE-READ rather than diffed. Regenerate and review every added or dropped
line:
  %s`, pinned, active, censusUpdateCmd)
	}
	if *updateCensus && pinned != active {
		if err := os.WriteFile(kasVersionPath, []byte(active+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", kasVersionPath, err)
		}
		t.Logf("re-pinned %s to %s (read from %s)", kasVersionPath, active, path)
	}
	return raw
}

// bundleSource reads the pinned agent server's source, skipping when it is not installed locally;
// it knows nothing about the census fixture.
func bundleSource(t *testing.T, active string) (src, path string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory, so no local agent-server bundle: %v", err)
	}
	pattern := filepath.Join(home, ".local", "share", "kiro-cli", "kas",
		active+"-*", "node_modules", "@kiro", "agent", "dist", "server", "acp-server.js")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) == 0 {
		t.Skipf(`no agent-server bundle for kiro-cli %s under %s.
This is stage 2 of the capability gate and it is local-only by design; stage 1
(TestInitializeDeclaresExactly) needs no bundle and gates CI.`, active, pattern)
	}
	slices.Sort(matches)
	read := pristineBundle(matches[0])
	raw, err := os.ReadFile(read)
	if err != nil {
		t.Fatalf("read bundle %s: %v", read, err)
	}
	return string(raw), read
}

// pristineBundle returns path's .orig sibling when one exists, else path: local kiro-cli patches
// rewrite acp-server.js in place and keep the original beside it.
func pristineBundle(path string) string {
	orig := path + ".orig"
	if fi, err := os.Stat(orig); err == nil && fi.Mode().IsRegular() {
		return orig
	}
	return path
}

// TestBundleSource_PrefersThePristineSibling pins the .orig preference, which is
// the difference between reading upstream and reading our own patches.
func TestBundleSource_PrefersThePristineSibling(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "patched")
	orig := live + ".orig"
	if err := os.WriteFile(orig, []byte("pristine"), 0o600); err != nil {
		t.Fatalf("write %s: %v", orig, err)
	}

	src, path := bundleSource(t, active)
	if src != "pristine" {
		t.Errorf("bundleSource(%q) read %q, want %q (the .orig sibling)", active, src, "pristine")
	}
	if path != orig {
		t.Errorf("bundleSource(%q) path = %q, want %q", active, path, orig)
	}
}

// TestBundleSource_FallsBackToTheLiveBundle covers the unpatched machine, where
// there is no .orig to prefer.
func TestBundleSource_FallsBackToTheLiveBundle(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "stock")

	src, path := bundleSource(t, active)
	if src != "stock" {
		t.Errorf("bundleSource(%q) read %q, want %q", active, src, "stock")
	}
	if path != live {
		t.Errorf("bundleSource(%q) path = %q, want %q", active, path, live)
	}
}

// fakeBundle writes content to a stand-in agent-server bundle for version active
// under a scratch HOME, and returns its path.
func fakeBundle(t *testing.T, active, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "kiro-cli", "kas",
		active+"-0000", "node_modules", "@kiro", "agent", "dist", "server")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "acp-server.js")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Setenv("HOME", home)
	return path
}

// jsFuncBody returns the balanced-brace run starting at the brace at index open, so a sweep can be
// scoped to ONE function of the bundle.
func jsFuncBody(src string, open int) string {
	depth := 0
	for i := open; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == c {
					break
				}
			}
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

// discoverOne returns the single group-1 match of re and fails unless there is exactly one, for
// every dynamic anchor.
func discoverOne(t *testing.T, what string, re *regexp.Regexp, src string) string {
	t.Helper()
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	names := slices.Sorted(maps.Keys(seen))
	if len(names) != 1 {
		t.Fatalf(`%s: found %d candidate names %v in the bundle, want exactly 1.
This anchor names a mangled upstream function from its surrounding structure,
so it holds only while that structure is unique. Re-read the pattern against
the bundle; do not regenerate the fixture, which would record the miss as the
new expectation.`, what, len(names), names)
	}
	return names[0]
}

// keysIn returns the sorted, deduplicated first capture group of every match.
func keysIn(re *regexp.Regexp, src string) []string {
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// requireNonEmpty keeps the census honest: each source is a regex over a 21 MB third-party bundle,
// and it dies by matching nothing, which would read as a clean census.
func requireNonEmpty(t *testing.T, source string, keys []string) []string {
	t.Helper()
	if len(keys) == 0 {
		t.Fatalf(`census source %q extracted NOTHING from the bundle.
That is a broken extractor, not a clean result: the pattern this source relies
on was reshaped upstream. Fix the pattern; do not regenerate the fixture, which
would record the emptiness as the new expectation.`, source)
	}
	return keys
}

// readCapabilityKeys returns every top-level _meta.kiro key the pinned agent
// server reads from the client, from all three sites that read one.
func readCapabilityKeys(t *testing.T, src string) []string {
	t.Helper()
	resolved := requireNonEmpty(t, "resolveCapabilities body", resolvedBlockKeys(t, src))
	stored := requireNonEmpty(t, "clientMeta reads", keysIn(clientMetaReadRe, src))
	adhoc := requireNonEmpty(t, "initialize-scope reads", initScopeKeys(t, src))

	all := slices.Concat(resolved, stored, adhoc)
	slices.Sort(all)
	return slices.Compact(all)
}

// resolvedBlockKeys returns the keys resolveCapabilities reads off the client's top-level
// _meta.kiro block, discovered in three steps because none of the names survive mangling.
func resolvedBlockKeys(t *testing.T, src string) []string {
	t.Helper()
	name := discoverOne(t, "resolveCapabilities", capResolverRe, src)

	fnRe := regexp.MustCompile(`function\s+` + regexp.QuoteMeta(name) +
		`\s*\((` + jsIdent + `)\)\s*\{`)
	loc := fnRe.FindStringSubmatchIndex(src)
	if loc == nil {
		t.Fatalf(`resolveCapabilities resolved to %q, but no `+
			`"function %s(<one parameter>) {" declares it.
The name came from its call site, so it exists; the declaration is either
spelled another way (an arrow function, a method) or takes a different number
of parameters. Re-read the pattern rather than regenerating the fixture.`,
			name, name)
	}
	param := src[loc[2]:loc[3]]
	body := jsFuncBody(src, loc[1]-1)
	if body == "" {
		t.Fatalf("resolveCapabilities (%s): unbalanced braces from its declaration", name)
	}

	bindRe := regexp.MustCompile(`(` + jsIdent + `)\s*=\s*` +
		regexp.QuoteMeta(param) + `\??\._meta\?\.kiro`)
	local := discoverOne(t, "the top-level _meta.kiro local in "+name, bindRe, body)
	return keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), body)
}

// initScopeKeys returns the keys read off every local the agent server binds to
// the client's _meta.kiro block, within the block that scopes each binding.
// Zero sites is caught by readCapabilityKeys' requireNonEmpty.
func initScopeKeys(t *testing.T, src string) []string {
	t.Helper()
	var keys []string
	for _, m := range initBindRe.FindAllStringSubmatchIndex(src, -1) {
		local := src[m[2]:m[3]]
		scope := bindingScope(src, m[0])
		if scope == "" {
			t.Fatalf("the _meta.kiro binding at byte %d has no enclosing block within %d bytes", m[0], bindingScopeReach)
		}
		keys = append(keys, keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), scope)...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// bindingScope returns src from pos to the end of the innermost block that
// encloses pos, which is the lexical scope of a let binding made there. A read
// of the same name outside that block is a different variable.
func bindingScope(src string, pos int) string {
	floor := max(0, pos-bindingScopeReach)
	for open := strings.LastIndexByte(src[:pos], '{'); open >= floor; open = strings.LastIndexByte(src[:open], '{') {
		if body := jsFuncBody(src, open); body != "" && open+len(body) > pos {
			return src[pos : open+len(body)]
		}
	}
	return ""
}

// readSettingKeys returns the _meta.kiro.settings keys the agent server reads through its five
// static reader shapes.
func readSettingKeys(t *testing.T, src string) []string {
	t.Helper()
	settingFn := discoverOne(t, "isSettingEnabled", settingEnabledFnRe, src)
	featureFn := discoverOne(t, "the isFeatureEnabled wrapper", featureEnabledFnRe, src)

	direct := requireNonEmpty(t, "isSettingEnabled calls", slices.Concat(
		keysIn(settingEnabledRe, src),
		keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(settingFn)+
			`\([^,()]*,\s*["'](`+jsIdent+`)["']\)`), src),
	))
	features := requireNonEmpty(t, "isFeatureEnabled calls", slices.Concat(
		keysIn(featureEnabledRe, src),
		keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(featureFn)+
			`\(["'](`+jsIdent+`)["']\)`), src),
	))
	resolved := requireNonEmpty(t, "settings resolvers", keysIn(settingResolverRe, src))
	destructured := requireNonEmpty(t, "destructuring resolvers", keysIn(settingDestructureRe, src))
	computed := requireNonEmpty(t, "computed-member resolver calls", computedResolverKeys(t, src))

	all := slices.Concat(direct, features, resolved, destructured, computed)
	slices.Sort(all)
	return slices.Compact(all)
}

// computedResolverKeys returns the literal keys passed to the computed-member
// settings resolver at its call sites.
func computedResolverKeys(t *testing.T, src string) []string {
	t.Helper()
	fn := discoverOne(t, "the computed-member settings resolver", settingComputedFnRe, src)
	return keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(fn)+`\([^,()]*,\s*["'](`+jsIdent+`)["']`), src)
}

// declaredKeys returns what the table accounts for, split by container. A withheld row counts as
// declared: recording a deliberate omission is the table's job.
func declaredKeys() (capabilities, settings map[string]bool) {
	capabilities = map[string]bool{
		settingsKey: true,
	}
	settings = make(map[string]bool)
	for _, row := range table {
		switch {
		case row.door == doorEnvironment:
		case inSettings(row.resolver):
			settings[row.key] = true
		default:
			capabilities[row.key] = true
		}
	}
	return capabilities, settings
}

// TestCapabilityCensus reports every client-side key the pinned agent server reads that the table
// does not account for.
func TestCapabilityCensus(t *testing.T) {
	src := loadBundle(t)
	declaredCaps, declaredSettings := declaredKeys()

	var unclaimed []string
	for _, key := range readCapabilityKeys(t, src) {
		if !declaredCaps[key] {
			unclaimed = append(unclaimed, "capability "+key)
		}
	}
	for _, key := range readSettingKeys(t, src) {
		if !declaredSettings[key] {
			unclaimed = append(unclaimed, "setting "+key)
		}
	}
	slices.Sort(unclaimed)

	got := censusHeader(activeKASVersion(t)) + strings.Join(unclaimed, "\n") + "\n"
	if *updateCensus {
		if err := os.WriteFile(unclaimedPath, []byte(got), 0o600); err != nil {
			t.Fatalf("write %s: %v", unclaimedPath, err)
		}
		t.Logf("wrote %s (%d unclaimed)", unclaimedPath, len(unclaimed))
	}
	wantRaw, err := os.ReadFile(unclaimedPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with: %s): %v", unclaimedPath, censusUpdateCmd, err)
	}
	if string(wantRaw) == got {
		return
	}
	t.Errorf(`the capability census changed.
A key appearing here is one the agent server reads and the table does not
account for; a key disappearing means it was claimed or upstream stopped reading
it. Either way it is a review, not a diff to wave through. Regenerate with:
  %s
--- want
%s
+++ got
%s`, censusUpdateCmd, wantRaw, got)
}

// censusHeader is the fixture's preamble, regenerated with the body so the
// version it was read from travels with the findings.
func censusHeader(version string) string {
	return `# Client-side keys the kiro-cli agent server reads that internal/kascap does
# NOT account for: neither sent nor deliberately withheld.
#
# This is not a to-do list. Most entries are capabilities marotte has no handler
# for and should not claim. An entry leaves this file by gaining a table row in
# either direction: send:true with an implementation, or send:false with a
# because saying why not.
#
# Read from kiro-cli ` + version + ` by TestCapabilityCensus.
# Regenerate: go test ./internal/kascap/ -run TestCapabilityCensus -update
`
}

// TestCensusFixture_MatchesThePin fails when the census fixture was read from a
// different kiro-cli than the one this repo pins. It needs no bundle, so it runs in
// CI, where the bundle-reading census tests skip: a pin bump has to carry a
// regenerated census in the same change.
func TestCensusFixture_MatchesThePin(t *testing.T) {
	raw, err := os.ReadFile(kasVersionPath)
	if err != nil {
		t.Fatalf("read %s: %v", kasVersionPath, err)
	}
	recorded := strings.TrimSpace(string(raw))
	active := activeKASVersion(t)
	if recorded != active {
		t.Fatalf(`census fixture %s records kiro-cli %s, but the pin is %s.
Fetch and unpack the pinned bundle as loadBundle's doc comment describes, then
regenerate and review every added or dropped line:
  %s`, kasVersionPath, recorded, active, censusUpdateCmd)
	}
}

// TestAbsentTrueMatchesTheBundle validates the absentTrue column against the agent server in both
// directions: it is a claim about somebody else's code.
func TestAbsentTrueMatchesTheBundle(t *testing.T) {
	src := loadBundle(t)
	bundleTrue := requireNonEmpty(t, "inverse-default resolvers", keysIn(absentTrueResolverRe, src))

	for _, row := range table {
		if !row.absentTrue {
			continue
		}
		if !slices.Contains(bundleTrue, row.key) {
			t.Errorf(`%s declares absentTrue, but no resolver in kiro-cli %s defaults it to true.
The bundle's inverse-default resolvers are %v. Either the claim was always wrong
or upstream changed the default, and both make the row's because misleading.`,
				rowID(row), activeKASVersion(t), bundleTrue)
		}
	}

	declared := make(map[string]*decl)
	for i, row := range table {
		if row.resolver == resolverSetting {
			declared[row.key] = &table[i]
		}
	}
	for _, key := range bundleTrue {
		row, ok := declared[key]
		if !ok {
			continue
		}
		if !row.absentTrue {
			t.Errorf(`setting.%s is declared without absentTrue, but kiro-cli %s resolves an
ABSENT %s to TRUE. Withholding the key therefore ENABLES the feature, which
inverts how send reads on this row.`, key, activeKASVersion(t), key)
		}
	}
}

// featureEnvRe matches every KIRO_FEATURE_*_ENABLED and KIRO_DISABLE_* literal in
// the bundle. derivedGateRe matches the experiment-gate helper's settingKey,
// whose env name the bundle builds at runtime (camelCase to SCREAMING_SNAKE,
// KIRO_FEATURE_ prefix, _ENABLED suffix), so it never appears as a literal.
var (
	featureEnvRe  = regexp.MustCompile(`\bKIRO_(?:FEATURE_[A-Z0-9_]+_ENABLED|DISABLE_[A-Z0-9_]+)\b`)
	derivedGateRe = regexp.MustCompile(`settingKey:"([A-Za-z][A-Za-z0-9]*)"`)
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

// bundleEnvArms returns every child-environment switch the bundle knows.
func bundleEnvArms(src string) []string {
	seen := make(map[string]bool)
	for _, name := range featureEnvRe.FindAllString(src, -1) {
		seen[name] = true
	}
	for _, m := range derivedGateRe.FindAllStringSubmatch(src, -1) {
		upper := strings.ToUpper(camelBoundary.ReplaceAllString(m[1], "${1}_${2}"))
		seen["KIRO_FEATURE_"+upper+"_ENABLED"] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// TestEnvironmentArmCensus fails on any KIRO_FEATURE_* arm (or KIRO_DISABLE_*
// switch) the pinned bundle knows that the environment door has no row for, so a
// new experiment arm becomes a decision at the release review rather than a
// silent ramp. Every arm needs a row, sent or withheld.
func TestEnvironmentArmCensus(t *testing.T) {
	src := loadBundle(t)
	arms := bundleEnvArms(src)
	if len(arms) < 10 {
		t.Fatalf("only %d env arms extracted (%v); the extraction patterns no longer match the bundle", len(arms), arms)
	}
	rows := make(map[string]bool)
	for _, row := range table {
		if row.door == doorEnvironment {
			rows[row.key] = true
		}
	}
	for _, arm := range arms {
		if !rows[arm] {
			t.Errorf("kiro-cli %s reads %s and the environment door has no row for it; add one (withheld follows the ramp)",
				activeKASVersion(t), arm)
		}
	}
	for key := range rows {
		if !slices.Contains(arms, key) {
			t.Errorf("environment row %s names a variable kiro-cli %s does not read", key, activeKASVersion(t))
		}
	}
}

func TestBundleEnvArms_DerivesTheGateHelperName(t *testing.T) {
	got := bundleEnvArms(`x="KIRO_FEATURE_TOOL_LOAD_ENABLED";lkr({settingKey:"unifiedAgent"});"KIRO_DISABLE_RECAP";` + "`KIRO_FEATURE_${n}`")
	want := []string{"KIRO_DISABLE_RECAP", "KIRO_FEATURE_TOOL_LOAD_ENABLED", "KIRO_FEATURE_UNIFIED_AGENT_ENABLED"}
	if !slices.Equal(got, want) {
		t.Errorf("bundleEnvArms = %q, want %q", got, want)
	}
}
