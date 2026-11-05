package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/preview"
)

// streamPaths answer with writes that must reach the client as they happen, so the compressor
// never wraps them.
var streamPaths = []string{"/api/events", "/api/shell/ws"}

func isCompressSkipped(r *http.Request) bool {
	// A preview file and a download (a .json one is application/json) are served by
	// http.ServeContent with byte ranges and a strong ETag for If-Range, which a compressed 200
	// gives up: it drops Accept-Ranges and weakens the tag.
	if strings.HasPrefix(r.URL.Path, preview.PathPrefix) || r.URL.Path == filebrowse.DownloadPath {
		return true
	}
	for _, p := range streamPaths {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
			return true
		}
	}
	return false
}

// compressJSON gzips JSON answers (httpserver's default types and 1 KiB floor) for a client that
// offers gzip.
func compressJSON() httpserver.Middleware {
	return httpserver.Gzip(httpserver.WithGzipSkip(isCompressSkipped))
}
