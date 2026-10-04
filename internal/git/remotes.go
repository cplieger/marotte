// Resolving a workspace repo's origin into forge coordinates.
//
// Nothing here serves an HTTP route. It exists because the PR-status poller
// (internal/forges) needs to know WHICH repos to ask a forge about, and the
// answer — the repos actually checked out on this box — is git knowledge. Putting
// the resolver in the forges package instead would have meant a second copy of
// repo discovery beside discoverRepos, and putting it in composition would have
// meant a wiring file that parses remote URLs.

package git

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// RepoRemote is one workspace repo's origin, resolved into the coordinates a
// forge addresses it by.
//
// WebBase is what selects the forge connection, compared with a connection's web
// base URL, and Slug is the owner/name path the forge addresses the repository
// by. A repo with no origin, or one whose origin does not parse, is simply absent
// from the result: there is no forge to ask about it.
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
		host, slug := ParseRemoteSlug(raw)
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

// ParseRemoteSlug splits a git remote URL into its host and its owner/name path.
//
// Both of git's spellings are accepted, because both are what a clone leaves
// behind: the scp-like form (`git@github.com:owner/name.git`) and a URL
// (`https://gitlab.com/group/sub/project.git`). The path is kept WHOLE rather
// than reduced to two segments — a GitLab subgroup is part of the project's
// address, and truncating it would produce a slug the forge cannot find.
//
// Returns ("", "") for anything that does not resolve, which every caller treats
// as "no forge to ask". Host resolution is parseRemoteHost's, so the
// control-character and remote-helper refusals are the ones the rest of the git
// surface already applies rather than a second set.
func ParseRemoteSlug(raw string) (host, slug string) {
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

// forbiddenInSlug reports whether r may not appear in an accepted slug: every C0
// control plus DEL (`url.Parse` percent-DECODES the path, so `%00` survives into
// u.Path as a real NUL and would reach a repository selector and the log stream),
// backslash (a path separator in no forge slug vocabulary), and `?` and `#` (URL
// delimiters that would change what the path means). SP is covered by `r <= ' '`.
func forbiddenInSlug(r rune) bool {
	return r <= ' ' || r == 0x7F || r == '\\' || r == '?' || r == '#'
}
