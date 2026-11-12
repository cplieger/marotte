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

	"github.com/cplieger/marotte/internal/workspace"
)

// Spelled as a flag rather than the repo's UPDATE_GOLDEN env gate because regenerating this fixture
// is a review action, not a formatting one: every line it adds is a capability somebody has to
// judge.
var updateCensus = flag.Bool("update", false,
	"rewrite testdata/unclaimed.txt from the local agent-server bundle")

const (
	unclaimedPath   = "testdata/unclaimed.txt"
	entrypointPath  = "../../entrypoint.sh"
	censusUpdateCmd = "go test ./internal/kascap/ -run TestCapabilityCensus -update"

	bindingScopeReach = 64 << 10

	// Every anchor is built from it because the bundle's identifiers are mangled.
	jsIdent = `[A-Za-z_$][A-Za-z0-9_$]*`
	// identStart opens an anchor on a mangled name: RE2's \b is ASCII, so it misses `$x` and matches inside it.
	identStart = `(?:^|[^A-Za-z0-9_$])`
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
	initBindLit = ".clientCapabilities?._meta?.kiro"

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

	// wireReaderRe is the settings registry's reader object for one fixed key,
	// `{keys:["sessionTools"],read:...}`.
	wireReaderRe  = regexp.MustCompile(`\{keys:\["(` + jsIdent + `)"\],read:`)
	wireReaderLit = `{keys:[`

	// wireFactoryRe names a registry reader factory taking its key as the first parameter,
	// `function f(t,e){return{keys:[t],read:...}}`; group 2 must equal group 3, which RE2 cannot say.
	wireFactoryRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\((` + jsIdent + `)[^()]*\)\s*\{\s*return\s*\{keys:\[(` +
			jsIdent + `)\],read:`,
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

// bundle is one pinned agent server's source for the bundle readers.
type bundle struct {
	name string // "local", or the architecture of the fetched archive
	src  string
	path string
}

// forEachBundle runs check as a subtest against every bundle bundles returns; CONTRIBUTING.md says how
// to unpack a missing one.
func forEachBundle(t *testing.T, check func(t *testing.T, b bundle)) {
	t.Helper()
	for _, b := range bundles(t, activeKASVersion(t)) {
		t.Run(b.name, func(t *testing.T) { check(t, b) })
	}
}

// bundleRel is the agent server's path inside a kas/<version>-<hash>/ runtime tree.
var bundleRel = filepath.Join("node_modules", "@kiro", "agent", "dist", "server", "acp-server.js")

// bundles returns the pinned agent server kiro-cli unpacked on this machine
// (workspace.KASLookup.Find); else, on a pull request that changes the pin, the one inside each
// changed archive (bundle_fetch_test.go); else it skips.
func bundles(t *testing.T, active string) []bundle {
	t.Helper()
	if local := workspace.KASLookupFromEnv().Find(active).Server; local != "" {
		read := pristineBundle(local)
		raw, err := os.ReadFile(read)
		if err != nil {
			t.Fatalf("read bundle %s: %v", read, err)
		}
		return []bundle{{name: "local", src: string(raw), path: read}}
	}
	var fetchedBundles []bundle
	for _, arch := range changedArchesOnThisPR(t) {
		fetchedBundles = append(fetchedBundles, fetchedBundle(t, active, arch))
	}
	if len(fetchedBundles) == 0 {
		t.Skipf(`no agent-server bundle for kiro-cli %s under KIRO_RUNTIME_DIR (%q) or kiro-cli's data dir
(~/.local/share/kiro-cli/kas/). It is fetched only on a pull request that changes the pin;
elsewhere unpack it as CONTRIBUTING.md's census paragraph describes.`, active, os.Getenv("KIRO_RUNTIME_DIR"))
	}
	return fetchedBundles
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

// TestBundles_PrefersThePristineSibling pins the .orig preference, which is
// the difference between reading upstream and reading our own patches.
func TestBundles_PrefersThePristineSibling(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "patched")
	orig := live + ".orig"
	if err := os.WriteFile(orig, []byte("pristine"), 0o600); err != nil {
		t.Fatalf("write %s: %v", orig, err)
	}

	got := bundles(t, active)
	want := []bundle{{name: "local", src: "pristine", path: orig}}
	if !slices.Equal(got, want) {
		t.Errorf("bundles(%q) = %+v, want %+v (the .orig sibling)", active, got, want)
	}
}

// TestBundles_FallsBackToTheLiveBundle covers the unpatched machine, where
// there is no .orig to prefer.
func TestBundles_FallsBackToTheLiveBundle(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "stock")

	got := bundles(t, active)
	want := []bundle{{name: "local", src: "stock", path: live}}
	if !slices.Equal(got, want) {
		t.Errorf("bundles(%q) = %+v, want %+v", active, got, want)
	}
}

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
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("KIRO_RUNTIME_DIR", "")
	return path
}

// TestBundles_FindsTheRuntimeDirEntry reads the bundle from kiro-cli's runtime dir, where kiro-cli
// 3.0.0 unpacks it when KIRO_RUNTIME_DIR is set.
func TestBundles_FindsTheRuntimeDirEntry(t *testing.T) {
	const active = "9.9.9"
	rt := t.TempDir()
	entry := filepath.Join(rt, active+"-0000000000000000")
	path := filepath.Join(entry, "kas", bundleRel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	for p, content := range map[string]string{path: "runtime-dir", filepath.Join(entry, ".complete"): ""} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("KIRO_RUNTIME_DIR", rt)

	// In a subtest, so a lookup that misses the entry skips only the subtest and fails here.
	var got []bundle
	t.Run("lookup", func(t *testing.T) { got = bundles(t, active) })
	want := []bundle{{name: "local", src: "runtime-dir", path: path}}
	if !slices.Equal(got, want) {
		t.Errorf("bundles(%q) = %+v, want %+v (the runtime-dir entry)", active, got, want)
	}
}

// jsBracketed returns the balanced `{...}` or `[...]` run opening at index open; strings are skipped.
func jsBracketed(src string, open int) string {
	depth := 0
	for i := open; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			i = jsStringEnd(src, i)
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

// jsStringEnd returns the index of the quote closing the string opening at i.
func jsStringEnd(src string, i int) int {
	q := src[i]
	for i++; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case q:
			return i
		}
	}
	return i
}

// identBefore returns the identifier ending at src[end] and where it starts: the maximal run of
// identifier bytes, "" when that run is empty or opens with a digit.
func identBefore(src string, end int) (string, int) {
	start := end
	for start > 0 && isIdentByte(src[start-1], true) {
		start--
	}
	if start == end || !isIdentByte(src[start], false) {
		return "", start
	}
	return src[start:end], start
}

// identAt returns the identifier starting at src[i], "" when there is none.
func identAt(src string, i int) string {
	end := i
	for end < len(src) && isIdentByte(src[end], end > i) {
		end++
	}
	return src[i:end]
}

// literalSites returns every index where lit starts in src at an identifier boundary: not after an
// identifier byte, nor after a dot unless afterDot. A literal search, because a regexp opening on a
// character class scans the whole bundle byte by byte, which -race makes minutes per test.
func literalSites(src, lit string, afterDot bool) []int {
	var sites []int
	for i := 0; ; {
		j := strings.Index(src[i:], lit)
		if j < 0 {
			return sites
		}
		at := i + j
		if at == 0 || !isIdentByte(src[at-1], true) && (afterDot || src[at-1] != '.') {
			sites = append(sites, at)
		}
		i = at + len(lit)
	}
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

func keysIn(re *regexp.Regexp, src string) []string {
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// indexesOf returns every non-overlapping index of lit in src.
func indexesOf(src, lit string) []int {
	var out []int
	for next := 0; ; {
		j := strings.Index(src[next:], lit)
		if j < 0 {
			return out
		}
		out = append(out, next+j)
		next += j + len(lit)
	}
}

// aroundReach is how far a findAround match may extend on either side of its literal.
const aroundReach = 1 << 10

// findAround returns re's submatch indices over src for the matches containing an occurrence of
// lit, which every match of re must contain. Go's regexp skips ahead only on a literal prefix, so a
// whole-bundle pattern opening on a class, an alternation or \b costs seconds under -race; this
// runs re only on identifier-bounded windows around each lit.
func findAround(re *regexp.Regexp, lit, src string) [][]int {
	var out [][]int
	seen := make(map[int]bool)
	for _, at := range indexesOf(src, lit) {
		lo, hi := max(0, at-aroundReach), min(len(src), at+len(lit)+aroundReach)
		for lo > 0 && isIdentByte(src[lo-1], true) {
			lo--
		}
		for hi < len(src) && isIdentByte(src[hi], true) {
			hi++
		}
		for _, m := range re.FindAllStringSubmatchIndex(src[lo:hi], -1) {
			if lo+m[0] > at || lo+m[1] < at+len(lit) || seen[lo+m[0]] {
				continue
			}
			seen[lo+m[0]] = true
			for i := range m {
				if m[i] >= 0 {
					m[i] += lo
				}
			}
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b []int) int { return a[0] - b[0] })
	return out
}

// submatches is FindStringSubmatch over findAround's first match: nil when there is none.
func submatches(src string, ms [][]int) []string {
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, len(ms[0])/2)
	for i := range out {
		if ms[0][2*i] >= 0 {
			out[i] = src[ms[0][2*i]:ms[0][2*i+1]]
		}
	}
	return out
}

// keysAround is keysIn over the whole bundle, through findAround.
func keysAround(re *regexp.Regexp, lit, src string) []string {
	seen := make(map[string]bool)
	for _, m := range findAround(re, lit, src) {
		seen[src[m[2]:m[3]]] = true
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
	stored := requireNonEmpty(t, "clientMeta reads", keysAround(clientMetaReadRe, "clientMeta", src))
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
	body := jsBracketed(src, loc[1]-1)
	if body == "" {
		t.Fatalf("resolveCapabilities (%s): unbalanced braces from its declaration", name)
	}

	bindRe := regexp.MustCompile(`(` + jsIdent + `)\s*=\s*` +
		regexp.QuoteMeta(param) + `\??\._meta\?\.kiro`)
	local := discoverOne(t, "the top-level _meta.kiro local in "+name, bindRe, body)
	return keysIn(regexp.MustCompile(identStart+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), body)
}

// initScopeKeys returns the keys read off every local the agent server binds to
// the client's _meta.kiro block, within the block that scopes each binding.
// Zero sites is caught by readCapabilityKeys' requireNonEmpty.
func initScopeKeys(t *testing.T, src string) []string {
	t.Helper()
	var keys []string
	for _, m := range findAround(initBindRe, initBindLit, src) {
		local := src[m[2]:m[3]]
		scope := bindingScope(src, m[0])
		if scope == "" {
			t.Fatalf("the _meta.kiro binding at byte %d has no enclosing block within %d bytes", m[0], bindingScopeReach)
		}
		keys = append(keys, keysIn(regexp.MustCompile(identStart+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), scope)...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// bindingScope returns src from pos to the end of the innermost block that encloses pos, which is
// the lexical scope of a let binding made there. A read of the same name outside that block is a
// different variable.
func bindingScope(src string, pos int) string {
	floor := max(0, pos-bindingScopeReach)
	for open := strings.LastIndexByte(src[:pos], '{'); open >= floor; open = strings.LastIndexByte(src[:open], '{') {
		if body := jsBracketed(src, open); body != "" && open+len(body) > pos {
			return src[pos : open+len(body)]
		}
	}
	return ""
}

// readSettingKeys returns the _meta.kiro.settings keys the agent server reads through its seven
// static reader shapes.
func readSettingKeys(t *testing.T, src string) []string {
	t.Helper()
	settingFn := discoverOne(t, "isSettingEnabled", settingEnabledFnRe, src)
	featureFn := discoverOne(t, "the isFeatureEnabled wrapper", featureEnabledFnRe, src)

	direct := requireNonEmpty(t, "isSettingEnabled calls", slices.Concat(
		keysIn(settingEnabledRe, src),
		keysAround(regexp.MustCompile(identStart+regexp.QuoteMeta(settingFn)+
			`\([^,()]*,\s*["'](`+jsIdent+`)["']\)`), settingFn+"(", src),
	))
	features := requireNonEmpty(t, "isFeatureEnabled calls", slices.Concat(
		keysIn(featureEnabledRe, src),
		keysAround(regexp.MustCompile(identStart+regexp.QuoteMeta(featureFn)+
			`\(["'](`+jsIdent+`)["']\)`), featureFn+"(", src),
	))
	resolved := requireNonEmpty(t, "settings resolvers", keysAround(settingResolverRe, ".data.", src))
	destructured := requireNonEmpty(t, "destructuring resolvers", keysAround(settingDestructureRe, ".data.", src))
	computed := requireNonEmpty(t, "computed-member resolver calls", computedResolverKeys(t, src))
	wired := requireNonEmpty(t, "fixed-key wire readers", keysAround(wireReaderRe, wireReaderLit, src))
	built := requireNonEmpty(t, "wire-reader factory calls", wireFactoryKeys(t, src))

	all := slices.Concat(direct, features, resolved, destructured, computed, wired, built)
	slices.Sort(all)
	return slices.Compact(all)
}

// wireFactoryKeys returns the literal keys passed to the settings registry's reader factories.
func wireFactoryKeys(t *testing.T, src string) []string {
	t.Helper()
	var keys []string
	for _, fn := range requireNonEmpty(t, "wire-reader factories", wireFactories(src)) {
		keys = append(keys, keysAround(regexp.MustCompile(identStart+regexp.QuoteMeta(fn)+`\("(`+jsIdent+`)"`), fn+"(", src)...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func wireFactories(src string) []string {
	seen := make(map[string]bool)
	for _, m := range findAround(wireFactoryRe, wireReaderLit, src) {
		if src[m[4]:m[5]] == src[m[6]:m[7]] {
			seen[src[m[2]:m[3]]] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// computedResolverKeys returns the literal keys passed to the computed-member
// settings resolver at its call sites.
func computedResolverKeys(t *testing.T, src string) []string {
	t.Helper()
	fn := discoverOne(t, "the computed-member settings resolver", settingComputedFnRe, src)
	return keysAround(regexp.MustCompile(identStart+regexp.QuoteMeta(fn)+`\([^,()]*,\s*["'](`+jsIdent+`)["']`), fn+"(", src)
}

// A withheld row counts as declared: recording a deliberate omission is the table's job.
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
	forEachBundle(t, func(t *testing.T, b bundle) {
		src := b.src
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
		sessionKeys, err := sessionSchemaKeys(src)
		if err != nil {
			t.Fatalf("census source %q: %v", "session _meta.kiro schema", err)
		}
		declaredSession := declaredSessionKeys()
		for _, key := range requireNonEmpty(t, "session _meta.kiro schema", sessionKeys) {
			if !declaredSession[key] {
				unclaimed = append(unclaimed, "session "+key)
			}
		}
		slices.Sort(unclaimed)

		got := censusHeader + strings.Join(unclaimed, "\n") + "\n"
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
	})
}

// censusHeader names no kiro-cli version, so a version-only bump (same keys read, same table rows)
// leaves the fixture byte-identical.
const censusHeader = `# Client-side keys the kiro-cli agent server reads that internal/kascap does
# NOT account for: neither sent nor deliberately withheld.
#
# This is not a to-do list. Most entries are capabilities marotte has no handler
# for and should not claim. An entry leaves this file by gaining a table row in
# either direction: send:true with an implementation, or send:false with a
# because saying why not.
#
# Read from the pinned kiro-cli by TestCapabilityCensus.
# Regenerate: go test ./internal/kascap/ -run TestCapabilityCensus -update
`

// TestAbsentTrueMatchesTheBundle validates the absentTrue column against the agent server in both
// directions: it is a claim about somebody else's code.
func TestAbsentTrueMatchesTheBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		src := b.src
		bundleTrue := requireNonEmpty(t, "inverse-default resolvers", keysAround(absentTrueResolverRe, ".data.", src))

		for _, row := range table {
			if !absentTrue[rowID(row)] {
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
			if !absentTrue[rowID(*row)] {
				t.Errorf(`setting.%s is declared without absentTrue, but kiro-cli %s resolves an
ABSENT %s to TRUE. Withholding the key therefore ENABLES the feature, which
inverts how send reads on this row.`, key, activeKASVersion(t), key)
			}
		}
	})
}

// featureEnvRe matches every KIRO_FEATURE_*_ENABLED, KIRO_FEATURE_*_CONFIG (a JSON
// config arm) and KIRO_DISABLE_* literal in the bundle, and KIRO_TRUST_CLASSIFIER_MODE, the one
// named switch KAS reads outside its env provider. derivedGateRe matches the
// experiment-gate helper's settingKey, whose env name the bundle builds at runtime
// (camelCase to SCREAMING_SNAKE, KIRO_FEATURE_ prefix, _ENABLED suffix), so it never
// appears as a literal.
var (
	featureEnvRe  = regexp.MustCompile(`\bKIRO_(?:FEATURE_[A-Z0-9_]+_(?:ENABLED|CONFIG)|DISABLE_[A-Z0-9_]+|TRUST_CLASSIFIER_MODE)\b`)
	derivedGateRe = regexp.MustCompile(`settingKey:"([A-Za-z][A-Za-z0-9]*)"`)
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

func bundleEnvArms(src string) []string {
	seen := make(map[string]bool)
	for _, m := range findAround(featureEnvRe, "KIRO_", src) {
		seen[src[m[0]:m[1]]] = true
	}
	for _, m := range derivedGateRe.FindAllStringSubmatch(src, -1) {
		upper := strings.ToUpper(camelBoundary.ReplaceAllString(m[1], "${1}_${2}"))
		seen["KIRO_FEATURE_"+upper+"_ENABLED"] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// TestEnvironmentArmCensus fails on any KIRO_FEATURE_* arm (or KIRO_DISABLE_*
// switch) the pinned bundle knows that the environment door has no row for. Every
// arm needs a row, sent or withheld, so none ramps silently.
func TestEnvironmentArmCensus(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		src := b.src
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
	})
}

// trustClassifierReaderRe is KAS's whole reader of the classifier gate: the variable held in a
// const, then read off process.env, trimmed, lower-cased and compared with "enforce".
var trustClassifierReaderRe = regexp.MustCompile(
	`([A-Za-z_$][\w$]*)="KIRO_TRUST_CLASSIFIER_MODE";function [A-Za-z_$][\w$]*\([A-Za-z_$][\w$]*=process\.env\)\{return [A-Za-z_$][\w$]*\[([A-Za-z_$][\w$]*)\]\?\.trim\(\)\.toLowerCase\(\)==="enforce"`,
)

// TestTrustClassifierGateMatchesBundle pins the value marotte's row sends against the bundle's
// reader: a renamed variable or another accepted value would leave the row inert.
func TestTrustClassifierGateMatchesBundle(t *testing.T) {
	forEachBundle(t, func(t *testing.T, b bundle) {
		m := submatches(b.src, findAround(trustClassifierReaderRe, `="KIRO_TRUST_CLASSIFIER_MODE";function `, b.src))
		if m == nil || m[1] != m[2] {
			t.Fatalf("kiro-cli %s has no reader comparing KIRO_TRUST_CLASSIFIER_MODE with \"enforce\"; re-read the gate and the row's value",
				activeKASVersion(t))
		}
		for _, row := range table {
			if row.key == "KIRO_TRUST_CLASSIFIER_MODE" && row.value != "enforce" {
				t.Errorf("the row sends %#v; kiro-cli %s accepts only \"enforce\"", row.value, activeKASVersion(t))
			}
		}
	})
}

func TestBundleEnvArms_DerivesTheGateHelperName(t *testing.T) {
	got := bundleEnvArms(`x="KIRO_FEATURE_AUTH_EXPIRY_RETRY_ENABLED";lkr({settingKey:"unifiedAgent"});"KIRO_DISABLE_RECAP";` +
		`y=process.env.KIRO_FEATURE_X_CONFIG;N="KIRO_TRUST_CLASSIFIER_MODE";` + "`KIRO_FEATURE_${n}`")
	want := []string{
		"KIRO_DISABLE_RECAP", "KIRO_FEATURE_AUTH_EXPIRY_RETRY_ENABLED", "KIRO_FEATURE_UNIFIED_AGENT_ENABLED", "KIRO_FEATURE_X_CONFIG",
		"KIRO_TRUST_CLASSIFIER_MODE",
	}
	if !slices.Equal(got, want) {
		t.Errorf("bundleEnvArms = %q, want %q", got, want)
	}
}
