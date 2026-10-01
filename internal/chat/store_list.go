package chat

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"golang.org/x/sync/singleflight"
)

// sfDo wraps singleflight.Group.Do so the result needs no type assertion at
// each call site.
func sfDo(sf *singleflight.Group, key string, fn func() listResult) listResult {
	v, _, _ := sf.Do(key, func() (any, error) { return fn(), nil })
	r, _ := v.(listResult)
	return r
}

// List returns every chat's header sorted by UpdatedAt desc. Unreadable headers
// are logged and skipped: one bad file must not hide the rest.
// Never nil, so JSON encoders emit `[]` rather than the `null` the wire decoder
// rejects.
func (s *Store) List(ctx context.Context) []marotte.ChatHeader {
	headers, _ := s.listWithCompleteness(ctx)
	return headers
}

// ListStamped is List plus the `chats` stamp the REST envelope carries. The scan
// takes no lock, so the version is read FIRST: a Mutate landing during the scan
// puts its header in the list and its bump outside the stamp, and the client then
// holds a list at least as new as its version, which the next digest reads as one
// spurious changed and never as a false unchanged.
func (s *Store) ListStamped(ctx context.Context) ([]marotte.ChatHeader, *marotte.SubjectStamp) {
	version, _ := s.versions.Current(subject.KindChats, "")
	headers, _ := s.listWithCompleteness(ctx)
	return headers, s.restStamp(subject.KindChats, "", version)
}

// listResult carries a scan and its completeness through one singleflight slot.
type listResult struct {
	headers  []marotte.ChatHeader
	complete bool
}

// ReferencedSessionIDs returns every ACP session id in any kept chat's CHAIN,
// not just its current one, and reports whether that set is COMPLETE.
//
// It backs the orphan session sweep, which reaps any session absent from the
// set, so it FAILS CLOSED: complete is false when a chat file that exists could
// not be read. A chat that vanished mid-scan (ENOENT) is not a failure.
func (s *Store) ReferencedSessionIDs(ctx context.Context) (refs map[string]struct{}, complete bool) {
	refs = make(map[string]struct{})
	headers, complete := s.listWithCompleteness(ctx)
	for i := range headers {
		for _, id := range headers[i].SessionChain() {
			refs[id] = struct{}{}
		}
	}
	return refs, complete
}

// listWithCompleteness is List plus the read-completeness flag the sweep needs,
// coalescing concurrent refreshes into one directory scan.
//
// The shared scan drops the caller's cancellation (values are kept) because
// coalescing makes one caller's lifetime everybody's: the request that opens the
// slot is routinely aborted by a second one already waiting on its answer, and
// both then received a truncated header list.
func (s *Store) listWithCompleteness(ctx context.Context) ([]marotte.ChatHeader, bool) {
	scanCtx := context.WithoutCancel(ctx)
	r := sfDo(&s.listSF, "list", func() listResult {
		headers, complete := s.listOnce(scanCtx)
		return listResult{headers: headers, complete: complete}
	})
	if r.headers == nil {
		return []marotte.ChatHeader{}, r.complete
	}
	return r.headers, r.complete
}

func (s *Store) listOnce(ctx context.Context) ([]marotte.ChatHeader, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat list", "dir", s.dir, "error", err)
		// Nothing is known about what chats exist, so never report complete.
		return []marotte.ChatHeader{}, false
	}
	valid := chatDirs(entries, s.dir)
	if len(valid) == 0 {
		return []marotte.ChatHeader{}, true
	}

	// No per-chat lock: reads are read-only and writes land by temp+rename, so a
	// reader always sees a complete file.
	headers, complete := readHeadersParallel(ctx, valid)
	slices.SortFunc(headers, func(a, b marotte.ChatHeader) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	if !complete {
		// Warned here because List drops the flag: downstream a truncated
		// sidebar is indistinguishable from having fewer chats.
		slog.Warn("chat list: incomplete scan; some chats that exist were not read",
			"dir", s.dir, "found", len(valid), "returned", len(headers))
	}
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"entries", len(entries),
		"returned", len(headers),
		"complete", complete)
	return headers, complete
}

// chatDirs filters a listing of the store's directory down to the chat directories:
// a directory named by a valid chat id that holds a header. A directory with no
// header is a chat mid-creation or mid-delete and is not listed.
func chatDirs(entries []os.DirEntry, dir string) []chatEntry {
	var valid []chatEntry
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if !chatIDPattern(marotte.ChatID(name)) {
			slog.Debug("chat list: skipped non-chat entry",
				"name", name, "reason", "invalid chat id pattern")
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(path, headerFileName)); err != nil {
			continue
		}
		valid = append(valid, chatEntry{id: name, path: path})
	}
	return valid
}
