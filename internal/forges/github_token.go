package forges

import (
	"context"
	"log/slog"
	"strings"
)

// dotcomAPIBase is github.com's API root, the one host the tools engine sends a
// GitHub token to.
const dotcomAPIBase = "https://api.github.com"

// GitHubToken answers the usable github.com connection's stored token for a
// request to api.github.com, or "" when there is none or its token cannot be
// handed over. It reads the store and sends nothing. An Enterprise connection
// never answers: its token is not github.com's.
func (m *Manager) GitHubToken(ctx context.Context) string {
	h, ok := dotcomGitHub(m.readers())
	if !ok {
		return ""
	}
	token, err := h.cred.Token(ctx)
	if err != nil {
		// The row's status and the keeper's check report this; a request goes out
		// anonymously rather than failing.
		slog.Debug("forges: no GitHub token for the tools engine", "connection", h.id, "error", err)
		return ""
	}
	return token
}

// dotcomGitHub is the github.com connection among hs when it addresses
// github.com's own API. The host compares without case, as forgeapi does when
// it picks api.github.com for the connection's own requests.
func dotcomGitHub(hs []readHandle) (readHandle, bool) {
	for i := range hs {
		h := &hs[i]
		api := strings.TrimSuffix(h.apiBase, "/")
		if h.kind == KindGitHub && strings.EqualFold(h.host, KindGitHub.defaultHost()) &&
			(api == "" || strings.EqualFold(api, dotcomAPIBase)) {
			return *h, true
		}
	}
	return readHandle{}, false
}
