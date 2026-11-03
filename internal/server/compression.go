package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/webhttp/v4"
)

// streamPaths answer with writes that must reach the client as they happen, so the compressor
// never wraps them.
var streamPaths = []string{"/api/events", "/api/shell/ws"}

func isCompressSkipped(r *http.Request) bool {
	// A preview file is served by http.ServeContent with Range support, which a compressed 200
	// gives up (it drops Accept-Ranges).
	if strings.HasPrefix(r.URL.Path, preview.PathPrefix) {
		return true
	}
	for _, p := range streamPaths {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
			return true
		}
	}
	return false
}

// compressJSON gzips JSON answers (webhttp's default types and 1 KiB floor) for a client that
// offers gzip.
func compressJSON() webhttp.Middleware {
	return webhttp.Gzip(webhttp.WithGzipSkip(isCompressSkipped))
}
