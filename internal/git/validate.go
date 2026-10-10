package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/runesafe/v2"
)

// --- path validation ---

// isValidGitRef reports whether s is safe to pass as a git ref to a subprocess:
// git-check-ref-format(1)'s refname rules (per component: not empty, no leading '.', no trailing
// '.' or '.lock'; whole string: no "..", "@{", space, '~', '^', ':', '?', '*', '[', '\'), git
// checkout -b's leading-dash refusal, and runesafe.IsUnsafeSingleLine, since git accepts C1 and
// bidi controls the panel and logs must not carry.
// A DENYLIST on purpose: an allowlist would refuse branch names git accepts. One function serves
// every call site; "HEAD" and Windows device stems are accepted because git accepts them.
func isValidGitRef(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	if strings.ContainsFunc(s, func(r rune) bool {
		return runesafe.IsUnsafeSingleLine(r) || strings.ContainsRune(" ~^:?*[\\", r)
	}) {
		return false
	}
	for component := range strings.SplitSeq(s, "/") {
		if component == "" ||
			strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

const maxRepoPaths = 1024

// sanitizeRepoPaths validates the repo-relative paths of stage/unstage/discard: no absolute paths,
// NULs, `..` escapes (pathinside.RelEscapes) or oversize batches. `..\` is refused locally: a
// Windows-separator path is not one to forward to git.
func sanitizeRepoPaths(paths []string) ([]string, error) {
	if len(paths) > maxRepoPaths {
		return nil, fmt.Errorf("too many paths (max %d)", maxRepoPaths)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if strings.ContainsRune(p, '\x00') {
			return nil, errors.New("null byte in path")
		}
		if filepath.IsAbs(p) {
			return nil, errors.New("absolute path rejected")
		}
		clean := filepath.Clean(p)
		if pathinside.RelEscapes(clean) || strings.HasPrefix(clean, `..\`) {
			return nil, fmt.Errorf("path escapes repo: %q", p)
		}
		out = append(out, clean)
	}
	return out, nil
}

// validateFilePath reports whether path is safe for git show/diff: no leading dash, no `..`
// component, no control bytes, not absolute. pathinside.HasDotDot judges the path AS WRITTEN,
// because the value is forwarded verbatim and RelEscapes would clean "a/../b" into an accepted "b".
func validateFilePath(path string) bool {
	if strings.HasPrefix(path, "-") ||
		pathinside.HasDotDot(path) ||
		strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) != -1 ||
		strings.HasPrefix(path, "/") {
		return false
	}
	return true
}
