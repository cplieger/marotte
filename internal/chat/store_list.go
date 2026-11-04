package chat

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// listFlight coalesces concurrent list scans, except that a caller never joins a scan that
// started before it arrived: it waits for that one and shares the next. A joined older scan can
// miss a write the caller already observed (a frame it applied, the version listStamped read),
// which hands it a list older than what it knows.
type listFlight struct {
	// running is closed when the scan in progress ends; nil when none runs.
	running chan struct{}
	// queued is the next scan, shared by every caller arriving while one runs.
	queued *listCall
	mu     sync.Mutex
}

type listCall struct {
	done chan struct{}
	r    listResult
}

func (f *listFlight) do(scan func() listResult) listResult {
	f.mu.Lock()
	if c := f.queued; c != nil {
		f.mu.Unlock()
		<-c.done
		return c.r
	}
	c := &listCall{done: make(chan struct{})}
	f.queued = c
	for f.running != nil {
		wait := f.running
		f.mu.Unlock()
		<-wait
		f.mu.Lock()
	}
	f.queued, f.running = nil, c.done
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running = nil
		f.mu.Unlock()
		close(c.done)
	}()
	c.r = scan()
	return c.r
}

// List returns every chat's header sorted by UpdatedAt desc. Unreadable headers
// are logged and skipped: one bad file must not hide the rest.
// Never nil, so JSON encoders emit `[]` rather than the `null` the wire decoder
// rejects.
func (s *Store) List(ctx context.Context) []marotte.ChatHeader {
	headers, _ := s.listWithCompleteness(ctx)
	return headers
}

// listStamped is List plus the `chats` stamp the REST envelope carries. The scan
// takes no lock, so the version is read FIRST: a Mutate landing during the scan
// puts its header in the list and its bump outside the stamp, and the client then
// holds a list at least as new as its version, which the next digest reads as one
// spurious changed and never as a false unchanged.
func (s *Store) listStamped(ctx context.Context) ([]marotte.ChatHeader, *marotte.SubjectStamp) {
	version, _ := s.versions.Current(subject.KindChats, "")
	headers, _ := s.listWithCompleteness(ctx)
	return headers, s.restStamp(subject.KindChats, "", version)
}

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
	r := s.listScans.do(func() listResult {
		headers, complete := s.listOnce(scanCtx)
		return listResult{headers: headers, complete: complete}
	})
	if r.headers == nil {
		return []marotte.ChatHeader{}, r.complete
	}
	return r.headers, r.complete
}

func (s *Store) listOnce(ctx context.Context) ([]marotte.ChatHeader, bool) {
	scan := s.scanOnce(ctx)
	headers := make([]marotte.ChatHeader, 0, len(scan.rows))
	for i := range scan.rows {
		h := scan.rows[i].header
		h.ReadPosition = s.openReadPosition(marotte.ChatID(h.ID))
		headers = append(headers, h)
	}
	complete := scan.complete()
	slices.SortFunc(headers, func(a, b marotte.ChatHeader) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	if !complete && scan.listed {
		// Warned here because List drops the flag: downstream a truncated
		// sidebar is indistinguishable from having fewer chats.
		slog.Warn("chat list: incomplete scan; some chats that exist were not read",
			"dir", s.dir, "skipped", len(scan.skipped), "returned", len(headers))
	}
	slog.Debug("chat list: scan complete",
		"dir", s.dir,
		"returned", len(headers),
		"complete", complete)
	return headers, complete
}

// headerScan is one fresh read of every chat header. listed is false only when the chats directory itself could not
// be listed, the one case where skipped cannot name what exists.
type headerScan struct {
	rows    []scannedRow
	skipped []marotte.ChatID
	listed  bool
}

func (h *headerScan) complete() bool { return h.listed && len(h.skipped) == 0 }

func (s *Store) scanOnce(ctx context.Context) headerScan {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat list", "dir", s.dir, "error", err)
		return headerScan{}
	}
	valid, skipped := chatDirs(entries, s.dir)
	rows, lost := readHeadersParallel(ctx, valid)
	return headerScan{rows: rows, skipped: append(skipped, lost...), listed: true}
}

// chatDirs filters a listing of the store's directory down to the chat directories:
// a directory named by a valid chat id that holds a header. A directory with no
// header is a chat mid-creation or mid-delete and is not listed.
//
// skipped names every chat whose header exists but could not be stat'ed: that chat is
// left out, and a caller feeding a delete must not read the listing as whole.
func chatDirs(entries []os.DirEntry, dir string) (valid []chatEntry, skipped []marotte.ChatID) {
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
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("chat list: header could not be stat'ed; scan marked incomplete",
					"chat_id", name, "error", err)
				skipped = append(skipped, marotte.ChatID(name))
			}
			continue
		}
		valid = append(valid, chatEntry{id: name, path: path})
	}
	return valid, skipped
}

// SessionOwners indexes, by session id, the chats whose session chain holds it. Of is the one ownership test: a run's
// keep fact goes to Of(its parent session), and the purge teardown deletes exactly the runs Of names the chat for.
type SessionOwners map[string][]marotte.ChatID

// Of answers the chats whose chain holds sessionID, sorted; nil for none.
func (o SessionOwners) Of(sessionID string) []marotte.ChatID {
	if sessionID == "" {
		return nil
	}
	return o[sessionID]
}

// OwnersOfChain is the index for one chat's chain.
func OwnersOfChain(chatID marotte.ChatID, chain []string) SessionOwners {
	o := make(SessionOwners, len(chain))
	for _, id := range chain {
		o.add(id, chatID)
	}
	return o
}

func (o SessionOwners) add(sessionID string, chatID marotte.ChatID) {
	if sessionID == "" || slices.Contains(o[sessionID], chatID) {
		return
	}
	o[sessionID] = append(o[sessionID], chatID)
	slices.Sort(o[sessionID])
}

// OwnerScan is one fresh scan's SessionOwners with what it read. Read holds every chat id whose header was read, empty
// chains included; Skipped every id that exists and was not read; Listed is false only when the chats directory could
// not be listed, the one case where Skipped cannot name what exists. Complete is Listed with nothing skipped.
type OwnerScan struct {
	Owners   SessionOwners
	Read     map[marotte.ChatID]struct{}
	Skipped  []marotte.ChatID
	Listed   bool
	Complete bool
}

// SessionOwners scans every chat header on its own rather than joining the coalesced List scan: a caller deciding
// under a lock must see every record written before it took that lock, and a joined scan may have started earlier.
func (s *Store) SessionOwners(ctx context.Context) OwnerScan {
	scan := s.scanOnce(ctx)
	out := OwnerScan{
		Owners:   make(SessionOwners),
		Read:     make(map[marotte.ChatID]struct{}, len(scan.rows)),
		Skipped:  scan.skipped,
		Listed:   scan.listed,
		Complete: scan.complete(),
	}
	for i := range scan.rows {
		id := scan.rows[i].id
		out.Read[id] = struct{}{}
		for _, sess := range scan.rows[i].header.SessionChain() {
			out.Owners.add(sess, id)
		}
	}
	return out
}
