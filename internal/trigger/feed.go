package trigger

import (
	"context"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/go/keyenc"
)

// Feed is one repository read through one connection: every event rule naming the pair shares its reads.
type Feed struct {
	// Connection is the forges connection id the reads go through.
	Connection string
	// Selector is the repository as the feed reads it, the stored spelling of its earliest-created rule.
	Selector string
}

// RepoID is the feed's repository identity, RepoRef.Encode() of its selector, which folds ASCII case.
func (f Feed) RepoID() string {
	return forgeapi.RepoRef{Selector: f.Selector}.Encode()
}

// Key groups rules onto one feed: the connection and the repository identity, so two spellings of one
// repository differing only in case share a feed.
func (f Feed) Key() string {
	return keyenc.Join(f.Connection, f.RepoID())
}

// FeedSource makes one list call of a feed, resuming from after (empty for the first page). A call may answer
// several pages; its Page carries the continuation and why it stopped.
type FeedSource interface {
	OpenIssues(ctx context.Context, f Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error)
	OpenPRs(ctx context.Context, f Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error)
	// Runs lists the runs created at or after since. Every call of one walk passes the same since, because a
	// continuation is refused under another bound.
	Runs(ctx context.Context, f Feed, since time.Time, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error)
}

// Walk is one list walked inside one poll.
type Walk[T any] struct {
	// Err is the call error that ended the walk, nil otherwise.
	Err error
	// Partial is the marker of the page the walk stopped on, nil when it read the list to its end.
	Partial *forgeapi.Partial
	// Items are every row the walk's calls answered, in order.
	Items []T
	// Calls counts the list calls the walk made.
	Calls int
}

// Complete reports whether the walk read the whole list: no error and no partial page.
func (w *Walk[T]) Complete() bool {
	return w.Err == nil && w.Partial == nil
}

// walk calls call until the list ends. Only a page cap with a continuation is followed, so no cursor outlives
// the poll that minted it; any other partial reason, an error or a continuation that does not move ends the
// walk partial, and the next poll starts again from the first page.
func walk[T any](call func(after forgeapi.Cursor) (forgeapi.Page[T], error)) Walk[T] {
	var w Walk[T]
	var after forgeapi.Cursor
	for {
		page, err := call(after)
		w.Calls++
		if err != nil {
			w.Err = err
			return w
		}
		w.Items = append(w.Items, page.Items...)
		w.Partial = page.Partial
		if page.Partial == nil {
			return w
		}
		if page.Partial.Reason != forgeapi.PartialPaginationCap || page.Next == "" || page.Next == after {
			return w
		}
		after = page.Next
	}
}

// walkIssues walks f's open issues.
func walkIssues(ctx context.Context, src FeedSource, f Feed) Walk[forgeapi.Issue] {
	return walk(func(after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
		return src.OpenIssues(ctx, f, after)
	})
}

// walkPRs walks f's open pull requests.
func walkPRs(ctx context.Context, src FeedSource, f Feed) Walk[forgeapi.PullRequest] {
	return walk(func(after forgeapi.Cursor) (forgeapi.Page[forgeapi.PullRequest], error) {
		return src.OpenPRs(ctx, f, after)
	})
}

// walkRuns walks f's runs created at or after since.
func walkRuns(ctx context.Context, src FeedSource, f Feed, since time.Time) Walk[forgeapi.Run] {
	return walk(func(after forgeapi.Cursor) (forgeapi.Page[forgeapi.Run], error) {
		return src.Runs(ctx, f, since, after)
	})
}
