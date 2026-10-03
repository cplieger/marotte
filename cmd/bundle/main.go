// Command bundle builds marotte's browser client: it bundles the TypeScript
// entrypoints with esbuild (a Go library — no Node, no npm) and assembles the CSS
// bundle from the manifest files. tsc remains the TYPE gate; esbuild does not
// typecheck. Compression is the server's job; the bundler emits plain artifacts.
//
// Usage: go run ./cmd/bundle (from the repo root; also run by the Dockerfile builder
// stage). Inputs are static-src/ plus static-src/node_modules/; outputs land in
// static/, which go:embed ships.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const (
	srcDir = "static-src"
	outDir = "static"
	// jsExt is the extension every bundled module and chunk lands under.
	jsExt = ".js"
	// fixedEntryName keeps an entry's output name free of a content hash.
	fixedEntryName = "[name]"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bundle:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(filepath.Join(srcDir, "app.ts")); err != nil {
		return fmt.Errorf("run from the repo root: %w", err)
	}
	if err := cleanOutputs(); err != nil {
		return err
	}
	if err := bundleScripts(); err != nil {
		return err
	}
	if err := bundleServiceWorker(); err != nil {
		return err
	}
	if err := bundlePrepaint(); err != nil {
		return err
	}
	if err := buildCSS(); err != nil {
		return err
	}
	if err := fingerprintFonts(); err != nil {
		return err
	}
	return writePrecacheManifest()
}

// precacheManifest is what static/precache.json holds: the cacheable asset list and a
// stamp over their names. Field names are the wire contract with static-src/sw.ts.
// FETCHED rather than inlined into the worker: an inlined list would reach a client
// only through a worker update, and this worker never calls skipWaiting.
type precacheManifest struct {
	Stamp  string   `json:"stamp"`
	Assets []string `json:"assets"`
}

// precacheName is the manifest's own path, relative to outDir. Three things must agree
// on it: this writer, cleanOutputs (via bundleOwns), and the worker's fetch.
const precacheName = "precache.json"

// writePrecacheManifest enumerates the shell's cacheable assets and stamps them. The
// stamp is over the NAMES, which is honest because every entry carries esbuild's
// content hash, so bytes that move move a name. Sourcemaps are excluded.
func writePrecacheManifest() error {
	assets, err := precacheAssets()
	if err != nil {
		return err
	}
	sum := sha256.New()
	for _, name := range assets {
		// Separated, or two adjacent names could be re-cut into the same stream.
		sum.Write([]byte(name + "\n"))
	}
	doc, err := json.Marshal(precacheManifest{
		Stamp:  hex.EncodeToString(sum.Sum(nil))[:16],
		Assets: assets,
	})
	if err != nil {
		return fmt.Errorf("precache marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(outDir, precacheName), doc, 0o600)
}

// precacheAssets lists the content-hashed chunks, sorted, as URL paths relative to the
// site root. Eligibility is by NAME: cacheable without revalidation exactly when the
// bytes cannot change under the name, which excludes app.js and style.css. sw.js is
// out because a worker that caches itself makes a broken worker permanent.
func precacheAssets() ([]string, error) {
	// Non-nil even when empty: a nil slice marshals to JSON `null`, and
	// parseManifest reads that as an unusable document rather than as no assets.
	assets := []string{}
	entries, err := os.ReadDir(filepath.Join(outDir, "chunks"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No chunks at all is a valid document, and the worker treats it as one.
			return assets, nil
		}
		return nil, fmt.Errorf("precache chunks: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != jsExt {
			continue
		}
		assets = append(assets, "chunks/"+e.Name())
	}
	slices.Sort(assets)
	return assets, nil
}

// cleanOutputs removes previous build artifacts from static/ so stale modules never
// linger into the embed; committed assets (index.html, manifest.json, icons) are
// untouched. Ownership is by EXTENSION AT ANY DEPTH, matching .gitignore's
// `static/**/*.js`: an enumerated directory list let `static/exec-view/` survive every
// rebuild and reach the embedded tree. `chunks` is removed whole, because it may hold
// entries no extension rule owns. `vendor` is NOT: the bundler writes nothing there,
// and the Dockerfile fetches the terminal's web fonts into it BEFORE running the
// bundle, so sweeping it shipped every image without its fonts.
func cleanOutputs() error {
	if err := os.RemoveAll(filepath.Join(outDir, "chunks")); err != nil {
		return err
	}
	if err := removeBundleFiles(outDir); err != nil {
		return err
	}
	// A directory left holding only bundle output is an empty shell that still embeds.
	return pruneEmptyDirs(outDir)
}

// bundleOwns reports whether the bundler owns this file name, so removing it can never
// take a hand-authored asset. Kept in step with .gitignore's static/ block.
func bundleOwns(name string) bool {
	switch filepath.Ext(name) {
	case jsExt, ".map", ".gz":
		return true
	}
	// Named rather than matched by ".json": static/manifest.json is hand-authored.
	return name == "style.css" || name == precacheName
}

func removeBundleFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := removeBundleFiles(path); err != nil {
				return err
			}
			continue
		}
		if !bundleOwns(e.Name()) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// pruneEmptyDirs removes every directory under dir left empty, deepest first.
// dir itself is kept: it is the embed root and holds the committed assets.
func pruneEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if err := pruneEmptyDirs(sub); err != nil {
			return err
		}
		rest, err := os.ReadDir(sub)
		if err != nil {
			return err
		}
		if len(rest) == 0 {
			if err := os.Remove(sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// bundleScripts builds the page's two scripts in dependency order: the SSE worker
// first, because the page constructs it by the content-hashed URL that build emits,
// and that URL is injected into the app build as a compile-time constant.
func bundleScripts() error {
	workerURL, err := bundleSSEWorker()
	if err != nil {
		return err
	}
	return bundleApp(workerURL)
}

// workerURLDefine is the identifier the page reads the worker's URL through
// (static-src/globals.d.ts declares it; static-src/sse-adapter.ts constructs the
// worker from it).
const workerURLDefine = "__SSE_WORKER_URL__"

// bundleApp bundles the main client entry as ESM with code splitting: the dynamic
// import() sites and the code they share with the entry become hashed chunks under
// /chunks/. The entry keeps its stable /app.js name, so the HTML never needs rewriting
// and cache correctness comes from the server's ETag revalidation. workerURL is the
// site-root path of the SSE worker script, spliced in as a string literal.
func bundleApp(workerURL string) error {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "app.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatESModule,
		Splitting:         true,
		EntryNames:        fixedEntryName,
		ChunkNames:        "chunks/[name]-[hash]",
		Define:            map[string]string{workerURLDefine: strconv.Quote(workerURL)},
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Write:             true,
	})
	return buildErr("app", &result)
}

// bundleSSEWorker bundles sse-worker.ts as a single classic script (IIFE) at a
// content-hashed name under /chunks/: the URL IS the worker's identity, so a tab of an
// old bundle can never attach to a new bundle's worker, and the server's immutable
// caching of /chunks/ applies. It returns the emitted site-root path, read back from
// esbuild's metafile rather than predicted.
func bundleSSEWorker() (string, error) {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "sse-worker.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatIIFE,
		EntryNames:        "chunks/[name]-[hash]",
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Metafile:          true,
		Write:             true,
	})
	if err := buildErr("sse-worker", &result); err != nil {
		return "", err
	}
	return emittedEntry(result.Metafile)
}

// emittedEntry reads the one JavaScript output that esbuild's metafile marks as the
// entry and returns it as a site-root URL path.
func emittedEntry(metafile string) (string, error) {
	var meta struct {
		Outputs map[string]struct {
			EntryPoint string `json:"entryPoint"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &meta); err != nil {
		return "", fmt.Errorf("sse-worker metafile: %w", err)
	}
	var entries []string
	for out, info := range meta.Outputs {
		if info.EntryPoint == "" || filepath.Ext(out) != jsExt {
			continue
		}
		rel, err := filepath.Rel(outDir, out)
		if err != nil {
			return "", fmt.Errorf("sse-worker output %q is outside %s: %w", out, outDir, err)
		}
		entries = append(entries, "/"+filepath.ToSlash(rel))
	}
	if len(entries) != 1 {
		return "", fmt.Errorf("sse-worker build emitted %d entry scripts, want 1: %q", len(entries), entries)
	}
	return entries[0], nil
}

// bundleServiceWorker bundles sw.ts as a single classic script (IIFE): app.ts registers
// it without {type:"module"}, and a worker must stay a single file at a stable URL,
// because a byte-diff at /sw.js is the browser's update signal.
func bundleServiceWorker() error {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "sw.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatIIFE,
		EntryNames:        fixedEntryName,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Write:             true,
	})
	return buildErr("sw", &result)
}

// bundlePrepaint bundles prepaint.ts as one classic script (IIFE) at the fixed
// /prepaint.js: index.html loads it as a blocking <script src> in <head>, which a
// module cannot be, and a fixed name keeps the HTML free of a build-time rewrite.
// The name carries no hash, so the server revalidates it rather than caching it.
func bundlePrepaint() error {
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{filepath.Join(srcDir, "prepaint.ts")},
		Outdir:            outDir,
		Bundle:            true,
		Format:            api.FormatIIFE,
		EntryNames:        fixedEntryName,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Sourcemap:         api.SourceMapLinked,
		Charset:           api.CharsetUTF8,
		LogLevel:          api.LogLevelWarning,
		Write:             true,
	})
	return buildErr("prepaint", &result)
}

func buildErr(what string, result *api.BuildResult) error {
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage, Color: false})
		return fmt.Errorf("%s bundle failed:\n%s", what, strings.Join(msgs, "\n"))
	}
	return nil
}

const (
	fontsSubdir   = "vendor/fonts"
	fontURLPrefix = "/vendor/fonts/"
)

// fontRef captures a /vendor/fonts name out of a url(), whatever its quoting and
// whitespace — the bundle concatenates the published UI's CSS beside marotte's own, so
// neither is one author's to choose. Anchored on url( because a PROSE mention of the
// path in a comment is not a face: 00-fonts.css carries one, and an unanchored scan read
// a face out of it and failed the build on an asset nothing fetched. Matched everywhere
// it occurs: a reference this misses keeps naming a file the rename already moved, which
// is the one failure mode that is silent.
var fontRef = regexp.MustCompile(`url\(\s*["']?/vendor/fonts/([^"')\s]+)`)

// stampedFontName matches the name stampFont produces, which is also the name
// internal/server.assetCachePolicy reads its cache verdict off.
var stampedFontName = regexp.MustCompile(`^(.+)\.[0-9a-f]{8}(\.[^.]+)$`)

// fingerprintFonts renames each face the bundle names to <stem>.<8 hex><ext> and
// rewrites the bundle's url()s.
//
// An unfetched font tree is SKIPPED, matching the server's own boot report, because
// scripts/dev-fonts.sh is a separate step from this command. A tree that exists and
// lacks a named face is a half-finished fetch, so that is an error.
func fingerprintFonts() error {
	cssPath := filepath.Join(outDir, "style.css")
	css, err := os.ReadFile(cssPath)
	if err != nil {
		return fmt.Errorf("font fingerprint: read bundle: %w", err)
	}
	names := cssFontNames(string(css))
	if len(names) == 0 {
		return errors.New("font fingerprint: the bundle names no /vendor/fonts asset")
	}
	dir := filepath.Join(outDir, fontsSubdir)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("bundle: fonts not fetched, %d faces left un-stamped (run scripts/dev-fonts.sh)\n", len(names))
		return nil
	} else if err != nil {
		return fmt.Errorf("font fingerprint: %w", err)
	}
	rewrites := make(map[string]string, len(names))
	for _, name := range names {
		hashed, err := stampFont(dir, name)
		if err != nil {
			return err
		}
		if hashed != name {
			rewrites[name] = hashed
		}
	}
	rewritten := fontRef.ReplaceAllStringFunc(string(css), func(ref string) string {
		head := strings.LastIndex(ref, fontURLPrefix) + len(fontURLPrefix)
		if hashed, ok := rewrites[ref[head:]]; ok {
			return ref[:head] + hashed
		}
		return ref
	})
	//nolint:gosec // G703: cssPath is this bundler's own output under static/, never request input
	if err := os.WriteFile(cssPath, []byte(rewritten), 0o600); err != nil {
		return fmt.Errorf("font fingerprint: rewrite bundle: %w", err)
	}
	fmt.Printf("bundle: fonts %d content-addressed of %d named\n", len(rewrites), len(names))
	return nil
}

// cssFontNames lists the /vendor/fonts names the bundle references, deduplicated: the
// overlay is named once per weight/style pair and must be renamed once.
func cssFontNames(css string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range fontRef.FindAllStringSubmatch(css, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return names
}

// stampFont renames one face to its content-addressed name and returns that name.
func stampFont(dir, name string) (string, error) {
	// A URL component reaches a filesystem path here.
	if name != filepath.Base(name) || name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("font fingerprint: %q names a path rather than a file", name)
	}
	src, err := fontSource(dir, name)
	if err != nil {
		return "", err
	}
	sum, err := hashFile(src)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	hashed := strings.TrimSuffix(name, ext) + "." + sum + ext
	if filepath.Base(src) == hashed {
		return hashed, nil
	}
	if err := os.Rename(src, filepath.Join(dir, hashed)); err != nil {
		return "", fmt.Errorf("font fingerprint: rename %s: %w", name, err)
	}
	return hashed, nil
}

// fontSource resolves the file holding the named face: the upstream name when present,
// else the one stamped sibling a previous run left. Both spellings are accepted because
// the bundle is regenerated from css/00-fonts.css and so always names the upstream face
// while the tree holds the stamped one, which is what makes a repeat run a no-op.
func fontSource(dir, name string) (string, error) {
	upstream := filepath.Join(dir, name)
	if info, err := os.Lstat(upstream); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("font fingerprint: %s is not a regular file", name)
		}
		return upstream, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("font fingerprint: %w", err)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	matches, err := filepath.Glob(filepath.Join(dir, stem+".????????"+ext))
	if err != nil {
		return "", fmt.Errorf("font fingerprint: %w", err)
	}
	stamped := make([]string, 0, 1)
	for _, m := range matches {
		if stampedFontName.MatchString(filepath.Base(m)) {
			stamped = append(stamped, m)
		}
	}
	switch len(stamped) {
	case 1:
		return stamped[0], nil
	case 0:
		return "", fmt.Errorf("font fingerprint: the bundle names %s, which the font tree does not hold", name)
	default:
		return "", fmt.Errorf("font fingerprint: %s has %d stamped copies; remove %s and re-fetch", name, len(stamped), fontsSubdir)
	}
}

// hashFile returns the first 8 hex digits of the file's SHA-256, streamed so a 9 MB face
// is not held in memory beside the bundle.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("font fingerprint: %w", err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("font fingerprint: hash %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(sum.Sum(nil))[:8], nil
}

// cssManifest is one ordered concat source: a manifest file listing CSS paths relative
// to baseDir; blank lines and #-comments are skipped.
type cssManifest struct {
	manifestPath string
	baseDir      string
}

// buildCSS assembles static/style.css: the @cplieger/web-terminal-ui component bundle
// FIRST (root-scoped, zero-specificity :where(.wt-root) selectors), then marotte's own
// splits — library-before-consumer source order is the override mechanism.
func buildCSS() error {
	wtui := filepath.Join(srcDir, "node_modules", "@cplieger", "web-terminal-ui", "css")
	appCSS := filepath.Join(srcDir, "css")
	sources := []cssManifest{
		{manifestPath: filepath.Join(wtui, "MANIFEST.touch"), baseDir: wtui},
		{manifestPath: filepath.Join(appCSS, "MANIFEST"), baseDir: appCSS},
	}
	var out strings.Builder
	parts := 0
	for _, src := range sources {
		data, err := os.ReadFile(src.manifestPath)
		if err != nil {
			return fmt.Errorf("css manifest: %w", err)
		}
		for line := range strings.Lines(string(data)) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			part, err := os.ReadFile(filepath.Join(src.baseDir, line))
			if err != nil {
				return fmt.Errorf("css part: %w", err)
			}
			out.Write(part)
			parts++
		}
	}
	// A manifest that EXISTS and lists nothing is the shape neither the
	// missing-manifest nor the missing-part error covers, and nothing downstream
	// catches it: a zero-byte stylesheet would ship as a build success.
	if out.Len() == 0 {
		return fmt.Errorf("css: the manifests listed no parts (%d manifests read)", len(sources))
	}
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), []byte(out.String()), 0o600); err != nil {
		return err
	}
	// So a shrink is visible in the build log rather than only in the browser.
	fmt.Printf("bundle: css %d parts, %d bytes\n", parts, out.Len())
	return nil
}
