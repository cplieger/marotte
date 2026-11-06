package forges

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/keyenc"
	"github.com/cplieger/marotte/internal/launchrule"
)

// ruleCheckTimeout bounds an event rule's save-time reads, inside the save request.
const ruleCheckTimeout = 20 * time.Second

// CheckEventRule reads, as ev's connection's account, what an event rule's polls will read: the repository's
// affordances, then one page of the list ev's kind walks. Nil when the rule can be watched; a refusal is a
// *launchrule.ForgeCheckError, Unavailable when the forge could not answer now.
func (m *Manager) CheckEventRule(ctx context.Context, ev *launchrule.EventSpec) error {
	host := connectionHost(ev.Connection)
	h, err := m.reader(ev.Connection)
	if err != nil {
		return &launchrule.ForgeCheckError{Text: fmt.Sprintf("Connect %s in Sources first", host)}
	}
	ctx, cancel := context.WithTimeout(ctx, ruleCheckTimeout)
	defer cancel()
	ref := forgeapi.RepoRef{Family: h.family, Selector: ev.Repo}
	ref.ID = ref.Encode()
	aff, err := h.core.RepoAffordances(ctx, ref)
	if err != nil {
		return checkRefusal(host, ev.Repo, err, true)
	}
	switch ev.Kind {
	case launchrule.KindIssueLabeled, launchrule.KindIssueOpened:
		if aff.HasIssues == forgeapi.SupportNo {
			return &launchrule.ForgeCheckError{Text: ev.Repo + " has issues turned off"}
		}
		_, err = h.core.ListIssues(ctx, ref, forgeapi.WithPageBound(1))
	case launchrule.KindPRLabeled, launchrule.KindPROpened:
		_, err = h.core.ListPRs(ctx, ref, forgeapi.WithPageBound(1))
	default:
		_, err = h.core.ListRuns(ctx, ref, forgeapi.WithSince(time.Now().Add(-time.Hour)), forgeapi.WithPageBound(1))
	}
	if err != nil {
		return checkRefusal(host, ev.Repo, err, false)
	}
	return nil
}

// checkRefusal words a failed save-time read. affordances marks the repository read, where a not-found or
// forbidden answer means the connection cannot see the repository; on a list read it is the forge's own refusal,
// a token that reads the repository but not its runs or issues.
func checkRefusal(host, repo string, err error, affordances bool) error {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	switch {
	case !ok:
		return &launchrule.ForgeCheckError{Text: host + " did not answer; try again", Unavailable: true}
	case fe.Code == forgeapi.CodeReconnectRequired || fe.Kind == forgeapi.KindUnauthorized:
		return &launchrule.ForgeCheckError{Text: host + " asks you to reconnect in Sources"}
	case fe.Code == forgeapi.CodeRunUndated:
		return &launchrule.ForgeCheckError{
			Text: "This Gitea lists runs with no creation time, so marotte cannot watch them for a finished run",
		}
	case fe.Kind == forgeapi.KindRateLimited:
		return &launchrule.ForgeCheckError{Text: host + " is limiting requests; try again later", Unavailable: true}
	case fe.Retryable || fe.Kind == forgeapi.KindTransient:
		return &launchrule.ForgeCheckError{Text: host + " did not answer; try again", Unavailable: true}
	case affordances && (fe.Kind == forgeapi.KindNotFound || fe.Kind == forgeapi.KindForbidden ||
		fe.Code == forgeapi.CodeRepoOrPRNotVisible):
		return &launchrule.ForgeCheckError{Text: host + " can't read " + repo}
	}
	msg := fe.Message
	if msg == "" {
		msg = fe.Error()
	}
	return &launchrule.ForgeCheckError{Text: host + " answered: " + msg}
}

// connectionHost is the host part of a MakeID connection id, the id itself when it has none.
func connectionHost(id string) string {
	if parts, err := keyenc.Split(id); err == nil && len(parts) == 2 && parts[1] != "" {
		return parts[1]
	}
	return id
}
