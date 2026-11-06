package trigger

import (
	"context"
	"sync"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// fakeCall is one recorded FeedSource call.
type fakeCall struct {
	since time.Time
	list  string
	after forgeapi.Cursor
}

// fakeSource answers each list from a queue of pages per list, and records every call. A list with no
// page left answers an empty, complete page.
type fakeSource struct {
	issues []forgeapi.Page[forgeapi.Issue]
	prs    []forgeapi.Page[forgeapi.PullRequest]
	runs   []forgeapi.Page[forgeapi.Run]
	errs   map[string]error
	calls  []fakeCall
	mu     sync.Mutex
}

var _ FeedSource = (*fakeSource)(nil)

func (s *fakeSource) record(list string, after forgeapi.Cursor, since time.Time) error {
	s.calls = append(s.calls, fakeCall{list: list, after: after, since: since})
	return s.errs[list]
}

func pop[T any](q *[]forgeapi.Page[T]) forgeapi.Page[T] {
	if len(*q) == 0 {
		return forgeapi.Page[T]{}
	}
	p := (*q)[0]
	*q = (*q)[1:]
	return p
}

func (s *fakeSource) OpenIssues(_ context.Context, _ Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("issues", after, time.Time{}); err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	return pop(&s.issues), nil
}

func (s *fakeSource) OpenPRs(_ context.Context, _ Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("prs", after, time.Time{}); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return pop(&s.prs), nil
}

func (s *fakeSource) Runs(_ context.Context, _ Feed, since time.Time, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("runs", after, since); err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	return pop(&s.runs), nil
}

func (s *fakeSource) recorded() []fakeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeCall(nil), s.calls...)
}
