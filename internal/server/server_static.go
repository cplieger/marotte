package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/cplieger/webhttp/v3"
)

// shellContentType is what the SPA shell is served as.
const shellContentType = "text/html; charset=utf-8"

// Each policy is a claim about the NAME, not the bytes: immutableAsset on a name
// whose content can change serves a stale asset for a year with no request that
// could discover it.
const (
	immutableAsset  = "public, max-age=31536000, immutable"
	revalidateAsset = "no-cache"
	noStoreHTML     = "no-store"
)

// fontAssetPrefix is where the Dockerfile writes the two web faces. The trailing
// slash is load-bearing: without it the prefix also matches a sibling directory
// whose name merely starts with it, which would hand that directory a policy
// nothing here decided to give it.
const fontAssetPrefix = "vendor/fonts/"

// stampedFont matches the `<stem>.<8 lowercase hex><ext>` name cmd/bundle's
// fingerprintFonts stamps. Reading the verdict off the NAME is what makes dropping
// that step degrade to revalidation rather than to a stale face, and it keeps the
// unhashed licence texts beside the fonts out of a year-long promise.
var stampedFont = regexp.MustCompile(`\.[0-9a-f]{8}\.[^./]+$`)

// contentHashedAsset matches the one naming shape whose bytes its name pins:
// cmd/bundle's `chunks/[name]-[hash]`, the hash 8 uppercase base32 characters.
// Anchored and pinned to the chunk directory so a hand-authored asset can never
// match and get cached for a year; it degrades to revalidating if ChunkNames moves.
var contentHashedAsset = regexp.MustCompile(`^chunks/[^/]+-[A-Z0-9]{8}\.js(\.map)?$`)

// assetCachePolicy is the per-asset Cache-Control policy webhttp.StaticHandler asks
// for. assetPath is normalized: no leading slash, "index.html" for a root request.
func assetCachePolicy(assetPath string) string {
	switch {
	case contentHashedAsset.MatchString(assetPath):
		return immutableAsset
	case strings.HasSuffix(assetPath, ".html"):
		return noStoreHTML
	case strings.HasPrefix(assetPath, fontAssetPrefix) && stampedFont.MatchString(assetPath):
		return immutableAsset
	default:
		return revalidateAsset
	}
}

// serviceWorkerPath is where app.ts registers the worker, so it is the name a
// browser fetches and the one whose absence makes push unreachable.
const serviceWorkerPath = "sw.js"

// reportMissingServiceWorker states at boot that this build cannot be subscribed
// to for push notifications. `static/**/*.js` is gitignored and /sw.js is emitted
// only by `go run ./cmd/bundle`, so a plain `go build` of a fresh clone embeds a
// tree without it; the SPA fallback then answers /sw.js with index.html, which
// fails registration in one tab's console and nowhere else. Without this line
// "push is broken" and "built without the bundle" produce identical server logs.
//
// spaHandler is built once per ListenAndServe, so this is one line per boot.
func reportMissingServiceWorker(staticFS fs.FS) {
	if _, err := fs.Stat(staticFS, serviceWorkerPath); err == nil {
		return
	}
	slog.Warn("server: no service worker in the embedded static tree; push notifications cannot be subscribed to",
		"path", serviceWorkerPath, "remedy", "go run ./cmd/bundle, then rebuild")
}

// terminalFontGlob names one of the faces css/00-fonts.css declares, so no match means
// the whole vendor/fonts tree is missing. A glob rather than a path because cmd/bundle
// stamps a content hash before the extension.
const terminalFontGlob = fontAssetPrefix + "WebTerminalGlyphs*.woff2"

// reportMissingTerminalFonts states at boot that the shell terminal will render on the
// platform monospace. `//go:embed static` fails SOFT on a missing subdirectory and
// static/vendor is gitignored, so a clone that never ran scripts/dev-fonts.sh embeds no
// faces — and the SPA fallback answers each .woff2 with index.html, so the fault has no
// server-side symptom at all.
//
// spaHandler is built once per ListenAndServe, so this is one line per boot.
func reportMissingTerminalFonts(staticFS fs.FS) {
	if matches, err := fs.Glob(staticFS, terminalFontGlob); err == nil && len(matches) > 0 {
		return
	}
	slog.Warn("server: no terminal fonts in the embedded static tree; the shell renders on the platform monospace",
		"path", terminalFontGlob, "remedy", "bash scripts/dev-fonts.sh, then rebuild")
}

// spaHandler serves the embedded FS, falling back to index.html for any path
// that is not a real file (History-API client routing). Assets get their ETag,
// gzip and cache policy from webhttp.StaticHandler; HTML is no-store so a
// release takes effect on the next load. The fallback WRITES the shell rather
// than calling http.ServeFileFS: net/http's serveFile applies index-page
// canonicalization to r.URL.Path unconditionally (fs.go:686-689, go1.27.0), so
// every path ending in "/index.html" answered a bodyless 301 to "./".
func spaHandler(staticFS fs.FS) http.Handler {
	static, err := webhttp.StaticHandler(staticFS, webhttp.WithStaticCacheControl(assetCachePolicy))
	if err != nil {
		// An unreadable embedded FS is a build defect, not a runtime condition.
		panic("server: static handler: " + err.Error())
	}
	shell, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		panic("server: static handler: read index.html: " + err.Error())
	}
	reportMissingServiceWorker(staticFS)
	reportMissingTerminalFonts(staticFS)
	shellLen := strconv.Itoa(len(shell))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && p != "index.html" {
			if info, statErr := fs.Stat(staticFS, p); statErr == nil && !info.IsDir() {
				static.ServeHTTP(w, r)
				return
			}
		}
		h := w.Header()
		h.Set("Content-Type", shellContentType)
		h.Set("Content-Length", shellLen)
		h.Set("Cache-Control", "no-store")
		// net/http suppresses the body for HEAD, so no method branch is needed.
		if _, wErr := w.Write(shell); wErr != nil {
			slog.Debug("server: spa shell write failed", "error", wErr)
		}
	})
}
