package forges

import (
	"context"
	"log/slog"
	"strings"
)

// dotcomAPIBase is github.com's API root, the one host the tools engine sends a
// GitHub token to.
const dotcomAPIBase = "https://api.github.com"

// GitHubToken answers the github.com connection's token for a request to
// api.github.com, or "" when there is no such connection or its credential
// cannot be read or renewed. The record file and the store are read on every
// call, so a connect or a disconnect reaches the next request. An Enterprise
// connection never answers: its token is not github.com's.
func (m *Manager) GitHubToken(ctx context.Context) string {
	if m.store == nil {
		return ""
	}
	recs, err := m.conns.load()
	if err != nil {
		return ""
	}
	rec := dotcomGitHub(recs)
	if rec == nil {
		return ""
	}
	c, err := m.clients.clientFor(m.store, rec)
	if err != nil {
		slog.Debug("forges: no GitHub token for the tools engine", "connection", rec.ID, "error", err)
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, keeperTokenBudget)
	defer cancel()
	token, err := c.cred.Token(tctx)
	if err != nil {
		// The row's reconnect state and the keeper's own line report this; a
		// request goes out anonymously rather than failing.
		slog.Debug("forges: no GitHub token for the tools engine", "connection", rec.ID, "error", err)
		return ""
	}
	return token
}

// dotcomGitHub is the github.com connection among recs when it addresses
// github.com's own API, else nil. The host compares without case, as forgeapi
// does when it picks api.github.com for the connection's own requests.
func dotcomGitHub(recs []connectionRecord) *connectionRecord {
	for i := range recs {
		r := &recs[i]
		api := strings.TrimSuffix(r.APIBaseURL, "/")
		if r.Kind == KindGitHub && strings.EqualFold(r.Host, KindGitHub.DefaultHost()) &&
			(api == "" || strings.EqualFold(api, dotcomAPIBase)) {
			return r
		}
	}
	return nil
}
