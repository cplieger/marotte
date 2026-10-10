// Resolving workspace repos' origins into forge coordinates, for the PR-status poller in
// internal/forges: which repos are checked out is git knowledge, so it lives beside discoverRepos.

package git

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// RepoRemote is one workspace repo's origin in the coordinates a forge addresses it by: WebBase
// selects the forge connection, Slug is the owner/name path. A repo with no parseable origin is
// absent.
type RepoRemote struct {
	// Name is the workspace directory ("." for the workspace root itself), the
	// name the git panel addresses the repository by.
	Name string
	// WebBase is the origin's scheme and authority when the remote is an http or
	// https URL, and "" for an ssh or scp-like remote.
	WebBase string
	Slug    string
}

// RepoRemotes resolves every discovered workspace repo's `origin` remote.
//
// Discovery reuses cachedDiscoverRepos, so this shares the singleflighted scan
// the git views already perform rather than walking the workspace again. The one
// subprocess per repo is `git remote get-url origin`, which reads config and
// touches no network.
func (h *Handler) RepoRemotes(ctx context.Context) []RepoRemote {
	repos := h.cachedDiscoverRepos(ctx)
	out := make([]RepoRemote, 0, len(repos))
	for _, r := range repos {
		raw, err := gitCmd(ctx, r.Dir, subRemote, "get-url", remoteOrigin)
		if err != nil {
			// A repo with no origin is ordinary here (a scratch clone, a local-only
			// tree), so this is Debug rather than Warn.
			slog.Debug("git remotes: no origin", "repo", r.Name, "error", err)
			continue
		}
		host, slug := parseRemoteSlug(raw)
		if host == "" || slug == "" {
			slog.Debug("git remotes: origin did not resolve to forge coordinates",
				"repo", r.Name)
			continue
		}
		out = append(out, RepoRemote{Name: r.Name, WebBase: remoteWebBase(raw), Slug: slug})
	}
	return out
}

// remoteWebBase is the scheme and authority of an http or https remote URL,
// lower-cased and without userinfo, and "" for any other remote.
func remoteWebBase(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return ""
	}
	host := strings.ToLower(sanitizeHost(u.Hostname()))
	if host == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

// parseRemoteSlug splits a git remote URL (scp-like or URL form) into its host and its owner/name
// path, kept WHOLE so a GitLab subgroup survives. ("", "") means no forge to ask. Host resolution
// is parseRemoteHost's, so its refusals apply.
func parseRemoteSlug(raw string) (host, slug string) {
	raw = strings.TrimSpace(raw)
	host = parseRemoteHost(raw)
	if host == "" {
		return "", ""
	}
	var path string
	if _, p, ok := parseSCPStyle(raw); ok {
		path = p
	} else {
		u, err := url.Parse(raw)
		if err != nil {
			return "", ""
		}
		path = u.Path
	}
	slug = cleanSlug(path)
	if slug == "" {
		return "", ""
	}
	return host, slug
}

// cleanSlug normalises a remote path into an owner/name slug: no leading or
// trailing slash, no `.git` suffix, and at least two segments (a single segment
// is not a repository address on any forge marotte talks to).
func cleanSlug(path string) string {
	s := strings.Trim(path, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	if s == "" || !strings.Contains(s, "/") {
		return ""
	}
	// A slug becomes a forge repository selector, so refuse the shapes that would
	// mean something other than a repository name there.
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
	}
	if strings.ContainsFunc(s, forbiddenInSlug) {
		return ""
	}
	return s
}

// forbiddenInSlug reports whether r may not appear in a slug: C0 controls and DEL (url.Parse
// percent-decodes `%00` into a real NUL), backslash, and the URL delimiters `?` and `#`.
func forbiddenInSlug(r rune) bool {
	return r <= ' ' || r == 0x7F || r == '\\' || r == '?' || r == '#'
}
