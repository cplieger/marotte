// The production PRSource. ListMyPRs answers the credential's own open pull
// requests in every repository it reaches, or every open one under an owner, so
// there is no author to filter on. A notice is only for a row whose target or
// source repository a workspace clone tracks, the scope the setting's copy
// promises (a clone of a fork tracks the pull requests opened from it), so a
// cycle with nobody present does not read a connection no clone tracks.
// cloneRepos is the one answer to which repository a clone is; a clone's origin
// must name a connection's web base, so an ssh clone joins nothing.

package forges

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/cplieger/forgeapi"
)

// RepoOrigin is one workspace clone's origin as the caller resolved it: the git
// panel's directory name, the remote's web base ("" for an ssh remote) and the
// repository path on the forge.
type RepoOrigin struct {
	Dir     string
	WebBase string
	Slug    string
}

// cloneRepos joins each clone to the connection whose web base its origin
// names. The repository id is the canonical one of the remote's path on that
// connection's family, so a mixed-case remote and the forge's own spelling are
// one repository; a path the family refuses joins nothing. It sends no request.
func cloneRepos(hs []readHandle, origins []RepoOrigin) []CloneRepo {
	byOrigin := make(map[string]*readHandle, len(hs))
	for i := range hs {
		byOrigin[originKey(hs[i].webBase)] = &hs[i]
	}
	out := make([]CloneRepo, 0, len(origins))
	for _, o := range origins {
		h, ok := byOrigin[originKey(o.WebBase)]
		if !ok {
			continue
		}
		if forgeapi.ValidateSelector(h.family, o.Slug) != nil {
			continue
		}
		ref := forgeapi.RepoRef{Family: h.family, Selector: o.Slug}
		out = append(out, CloneRepo{Dir: o.Dir, ForgeID: h.id, RepoID: ref.Encode()})
	}
	return out
}

// originKey is a web base reduced to what two spellings of one origin share:
// the scheme and authority lower-cased, the scheme's default port dropped. A
// connection's web base always names a host, so the empty web base of an ssh
// remote matches none.
func originKey(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && port != defaultPorts[scheme] {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

var defaultPorts = map[string]string{"https": "443", "http": "80"}

type managerPRSource struct {
	mgr *Manager
	// origins is injected rather than computed here: which repositories are
	// checked out and where their origins point is git knowledge.
	origins func(context.Context) []RepoOrigin
}

// NewManagerPRSource returns the production PRSource over a forge manager and
// the workspace clones' origins.
func NewManagerPRSource(mgr *Manager, origins func(context.Context) []RepoOrigin) PRSource {
	return &managerPRSource{mgr: mgr, origins: origins}
}

// Read answers every usable connection with the clones it serves. Present, it
// reads each one's scopes; otherwise only the ones a clone tracks, by the
// authored call alone. Every call starts from the cursor after gives its scope,
// and a failed call answers its error alone, so one broken scope or connection
// does not stop the others being read.
func (s *managerPRSource) Read(ctx context.Context, present bool, after func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	hs := s.mgr.readers()
	if len(hs) == 0 {
		return nil
	}
	clones := make(map[string][]CloneRepo)
	for _, c := range cloneRepos(hs, s.origins(ctx)) {
		clones[c.ForgeID] = append(clones[c.ForgeID], c)
	}
	out := make([]ConnectionRead, 0, len(hs))
	for i := range hs {
		h := &hs[i]
		r := ConnectionRead{
			Conn:   PRConnection{ID: h.id, Account: h.account, WebBase: h.webBase},
			Clones: clones[h.id], Family: h.family,
		}
		if present || len(r.Clones) > 0 {
			read(ctx, h, &r, present, after)
		}
		out = append(out, r)
	}
	return out
}

// read makes r's calls on h's client: present, one ListMyPRs per scope;
// otherwise the authored call alone.
func read(ctx context.Context, h *readHandle, r *ConnectionRead, present bool,
	after func(PRConnection, Scope) forgeapi.Cursor,
) {
	for _, sc := range cycleScopes(h, present) {
		r.Pages = append(r.Pages, listScope(ctx, h.core, sc, after(r.Conn, sc)))
	}
	r.Credential = h.cred.State().String()
	r.ReadPR = readerOf(h.core, r.Family)
}

func readerOf(core forgeapi.Core, family forgeapi.Family) func(context.Context, string, int) (PR, error) {
	return func(ctx context.Context, repoID string, number int) (PR, error) {
		ref, err := forgeapi.DecodeRepoRef(repoID, family)
		if err != nil {
			return PR{}, err
		}
		got, err := core.ReadPR(ctx, ref, forgeapi.PRRef{Number: number})
		if err != nil {
			return PR{}, err
		}
		return listRow(&got), nil
	}
}

// GitLab's owner scope takes a group only and refuses the user's own namespace, so its authored
// rows stand in for the login's.
func cycleScopes(h *readHandle, present bool) []Scope {
	if !present {
		return []Scope{authoredScope}
	}
	scopes := make([]Scope, 0, 2+len(h.owners))
	if h.login != "" && h.family != forgeapi.FamilyGitLab {
		scopes = append(scopes, Scope{Kind: scopeOwner, Owner: h.login})
	}
	scopes = append(scopes, authoredScope)
	for _, o := range h.owners {
		scopes = append(scopes, Scope{Kind: scopeAdded, Owner: o})
	}
	return scopes
}

// listScope is one ListMyPRs call for sc from after.
func listScope(ctx context.Context, core forgeapi.Core, sc Scope, after forgeapi.Cursor) scopePage {
	page := scopePage{Scope: sc}
	opts := []forgeapi.ListOption{forgeapi.WithAfter(after)}
	if sc.Owner != "" {
		opts = append(opts, forgeapi.WithOwner(sc.Owner))
	}
	got, err := core.ListMyPRs(ctx, opts...)
	if err != nil {
		page.Err = err
		return page
	}
	page.Rows = rowsOf(got.Items, listRow)
	page.Next = got.Next
	page.Partial = partialOf(got.Partial)
	return page
}

// listRow is p's row without its body, which the detail reads on demand.
func listRow(p *forgeapi.PullRequest) PR {
	row := prWire(p)
	row.Body = ""
	return row
}
