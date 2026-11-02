package translate

import (
	"net/url"
	"path/filepath"
	"strings"
)

// DocKeyFromAbs is the Docs tab's row identity for a file at the absolute path abs: the cleaned path with its leading
// "/" trimmed, as the server's scan builds KiroDoc.Path from the work root and every repository root. ok is false for
// a relative path, which joins no row.
func DocKeyFromAbs(abs string) (key string, ok bool) {
	if !filepath.IsAbs(abs) {
		return "", false
	}
	return strings.TrimPrefix(filepath.Clean(abs), "/"), true
}

// docKeyFromFileURI is DocKeyFromAbs of a file:// URI's path; ok is false for any other URI.
func docKeyFromFileURI(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	return DocKeyFromAbs(u.Path)
}
