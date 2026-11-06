package forges

import (
	"context"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/trigger"
)

// feedSource reads a trigger feed through its connection's client, so the poll shares the connection's read
// cache, budget and sign-in state with every other reader.
type feedSource struct {
	mgr *Manager
}

var _ trigger.FeedSource = feedSource{}

// NewFeedSource returns the production trigger.FeedSource over m's connections. A call on a connection that is
// signed out or unusable answers the reader's error and sends nothing.
func NewFeedSource(m *Manager) trigger.FeedSource {
	return feedSource{mgr: m}
}

func (s feedSource) open(f trigger.Feed) (readHandle, forgeapi.RepoRef, error) {
	h, err := s.mgr.reader(f.Connection)
	if err != nil {
		return readHandle{}, forgeapi.RepoRef{}, err
	}
	ref := forgeapi.RepoRef{Family: h.family, Selector: f.Selector}
	ref.ID = ref.Encode()
	return h, ref, nil
}

func (s feedSource) OpenIssues(ctx context.Context, f trigger.Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
	h, ref, err := s.open(f)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	return h.core.ListIssues(ctx, ref, forgeapi.WithState(forgeapi.ListStateOpen), forgeapi.WithAfter(after))
}

func (s feedSource) OpenPRs(ctx context.Context, f trigger.Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
	h, ref, err := s.open(f)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return h.core.ListPRs(ctx, ref, forgeapi.WithState(forgeapi.ListStateOpen), forgeapi.WithAfter(after))
}

func (s feedSource) Runs(ctx context.Context, f trigger.Feed, since time.Time, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error) {
	h, ref, err := s.open(f)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	return h.core.ListRuns(ctx, ref, forgeapi.WithSince(since), forgeapi.WithAfter(after))
}
