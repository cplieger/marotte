package preview

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

// PathPrefix is the serve route's mount point. The token is its first segment.
const PathPrefix = "/preview/"

var errNonCanonical = errors.New("non-canonical preview path")

func isPageName(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".html" || ext == ".htm"
}

func checkPageShape(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return errors.New("the path must be absolute")
	case p != path.Clean(p):
		return errors.New("the path must be clean (no ., .. or repeated slashes)")
	case strings.ContainsRune(p, 0):
		return errors.New("the path must not contain a NUL byte")
	case !isPageName(p):
		return errors.New("only .html and .htm pages can be previewed")
	}
	return nil
}

// relBeneath returns p relative to the clean root when p lies strictly beneath
// it. "/" is the one clean root that already ends in a separator.
func relBeneath(root, p string) (string, bool) {
	prefix := root
	if root != "/" {
		prefix += "/"
	}
	rel, ok := strings.CutPrefix(p, prefix)
	if !ok || rel == "" {
		return "", false
	}
	return rel, true
}

// checkRelComponents refuses a hidden or backslash-bearing component. The serve
// handler refuses a decoded backslash, so a granted name must never carry one.
func checkRelComponents(rel string) error {
	for c := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(c, ".") {
			return errors.New("files and folders starting with . cannot be previewed")
		}
		if strings.ContainsRune(c, '\\') {
			return errors.New("a path component must not contain a backslash")
		}
	}
	return nil
}

// splitServePath decodes the escaped path after PathPrefix exactly once, one
// segment at a time, so %2F stays inside its segment. It returns the token and
// the relative segments; a trailing empty segment is kept so the caller can
// answer a directory URL with 404 rather than 400.
func splitServePath(escaped string) (tok string, rel []string, err error) {
	rest, ok := strings.CutPrefix(escaped, PathPrefix)
	if !ok {
		return "", nil, errNonCanonical
	}
	raw := strings.Split(rest, "/")
	segs := make([]string, len(raw))
	for i, r := range raw {
		d, derr := url.PathUnescape(r)
		if derr != nil {
			return "", nil, errNonCanonical
		}
		trailing := i == len(raw)-1
		if (d == "" && !trailing) || d == "." || d == ".." || strings.ContainsAny(d, "/\\\x00") {
			return "", nil, errNonCanonical
		}
		segs[i] = d
	}
	return segs[0], segs[1:], nil
}
