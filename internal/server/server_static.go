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

const shellContentType = "text/html; charset=utf-8"

// Each policy is a claim about the NAME: immutableAsset on a name whose content can change
// serves a stale asset for a year.
const (
	immutableAsset  = "public, max-age=31536000, immutable"
	revalidateAsset = "no-cache"
	noStoreHTML     = "no-store"
)

// The trailing slash keeps a sibling directory sharing the prefix out.
const fontAssetPrefix = "vendor/fonts/"

// Without the stamp a face revalidates rather than going stale.
var stampedFont = regexp.MustCompile(`\.[0-9a-f]{8}\.[^./]+$`)

// contentHashedAsset matches cmd/bundle's `chunks/[name]-[hash]` (8 uppercase base32),
// anchored so a hand-authored asset never gets a year-long cache.
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

// reportMissingServiceWorker logs at boot that /sw.js is absent (a `go build` without
// `go run ./cmd/bundle`): the SPA fallback would otherwise hide it from the server log.
func reportMissingServiceWorker(staticFS fs.FS) {
	if _, err := fs.Stat(staticFS, serviceWorkerPath); err == nil {
		return
	}
	slog.Warn("server: no service worker in the embedded static tree; push notifications cannot be subscribed to",
		"path", serviceWorkerPath, "remedy", "go run ./cmd/bundle, then rebuild")
}

// A glob because cmd/bundle stamps a content hash before the extension.
const terminalFontGlob = fontAssetPrefix + "WebTerminalGlyphs*.woff2"

// reportMissingTerminalFonts logs at boot that the terminal faces are absent: `//go:embed`
// fails soft on a missing gitignored vendor/ dir, and the SPA fallback hides it.
func reportMissingTerminalFonts(staticFS fs.FS) {
	if matches, err := fs.Glob(staticFS, terminalFontGlob); err == nil && len(matches) > 0 {
		return
	}
	slog.Warn("server: no terminal fonts in the embedded static tree; the shell renders on the platform monospace",
		"path", terminalFontGlob, "remedy", "bash scripts/dev-fonts.sh, then rebuild")
}

// HTML is no-store. The fallback WRITES the shell rather than calling http.ServeFileFS, whose
// serveFile 301s every path ending "/index.html" to "./" (net/http fs.go:686-689, go1.27.0).
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
