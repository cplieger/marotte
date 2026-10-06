package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// lock returns the per-chat mutex for chatID, creating it lazily. Entries
// are never removed from the map: removing an entry races with any
// caller that already fetched the *sync.Mutex pointer, letting two
// goroutines hold distinct mutexes for the same id.
func (s *Store) lock(chatID marotte.ChatID) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(chatID, &sync.Mutex{})
	//nolint:errcheck // LoadOrStore guarantees v is the stored *sync.Mutex.
	return v.(*sync.Mutex)
}

// Lock returns the per-chat mutex for the archive package.
func (s *Store) Lock(chatID marotte.ChatID) *sync.Mutex { return s.lock(chatID) }

// Dir returns the store's base directory.
func (s *Store) Dir() string { return s.dir }

// Remove deletes a chat's directory and tombstones the id (only a chat that existed). The log is
// closed and tombstoned first, so a folding turn cannot re-create entries.jsonl under the directory
// being unlinked. The caller must hold Lock for chatID. Returns the `chats` version minted, or ""
// when nothing was removed.
func (s *Store) Remove(chatID marotte.ChatID) (string, error) {
	dir, err := s.pathFor(chatID)
	if err != nil {
		return "", err
	}
	if v, ok := s.logs.LoadAndDelete(chatID); ok {
		if l, isLog := v.(*EntryLog); isLog {
			_ = l.Remove()
		}
	}
	// The index entry goes whatever the remove reports: a chat that was already gone
	// has no business staying findable.
	s.index.drop(chatID)
	s.dequeued.forget(chatID)
	if _, err := os.Stat(filepath.Join(dir, headerFileName)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A directory with no header is not a chat; sweep whatever is there.
			_ = os.RemoveAll(dir)
			return "", os.ErrNotExist
		}
		return "", err
	}
	var name string
	if c, err := s.header(chatID).Read(context.Background()); err == nil {
		name = c.Name
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	s.markDeleted(chatID, name)
	return s.versions.BumpCounter(subject.KindChats, ""), nil
}

// markDeleted records that chatID was just deleted. Mutate calls for
// the same id within tombstoneTTL will refuse to auto-create.
func (s *Store) markDeleted(chatID marotte.ChatID, name string) {
	now := time.Now()
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	s.tombstone[chatID] = tombstone{at: now, name: name}
	cutoff := now.Add(-tombstoneTTL)
	for id, t := range s.tombstone {
		if t.at.Before(cutoff) {
			delete(s.tombstone, id)
		}
	}
}

func (s *Store) liveTombstone(chatID marotte.ChatID) (tombstone, bool) {
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	t, ok := s.tombstone[chatID]
	if !ok {
		return tombstone{}, false
	}
	if time.Since(t.at) > tombstoneTTL {
		delete(s.tombstone, chatID)
		return tombstone{}, false
	}
	return t, true
}

// isTombstoned reports whether chatID was deleted within tombstoneTTL.
func (s *Store) isTombstoned(chatID marotte.ChatID) bool {
	_, ok := s.liveTombstone(chatID)
	return ok
}

// DepartedName is the display name chatID had when it was deleted, within
// tombstoneTTL; false for a chat this store has not deleted.
func (s *Store) DepartedName(chatID marotte.ChatID) (string, bool) {
	t, ok := s.liveTombstone(chatID)
	return t.name, ok
}

// Exists reports whether chatID is a chat this store serves: its header is present
// and it was not deleted within tombstoneTTL. It takes NO per-chat mutex — a
// stat and the tombstone set's own lock — so the digest resolver can ask it
// without parking behind a Mutate's header rewrite.
func (s *Store) Exists(chatID marotte.ChatID) bool {
	if _, err := s.pathFor(chatID); err != nil || s.isTombstoned(chatID) {
		return false
	}
	return s.headerExists(chatID)
}

// headerExists reports whether chat.json is on disk for chatID. No lock.
func (s *Store) headerExists(chatID marotte.ChatID) bool {
	dir, err := s.pathFor(chatID)
	if err != nil {
		return false
	}
	//nolint:gosec,nolintlint // G703: the path is <root>/<id>/... over an id a door already admitted (ids.ValidChatID for a chat, runLog.dir for a run)
	_, err = os.Stat(filepath.Join(dir, headerFileName))
	return err == nil
}

// pathFor is the chat's DIRECTORY.
func (s *Store) pathFor(chatID marotte.ChatID) (string, error) {
	id := string(chatID)
	// The ".." test is already implied by chatIDPattern; it is the guard form
	// CodeQL's go/path-injection recognises, which a custom predicate is not.
	if !chatIDPattern(chatID) || strings.Contains(id, "..") {
		return "", errInvalidChatID(chatID)
	}
	return filepath.Join(s.dir, id), nil
}

// header is the chat's header file. The id is validated by every caller's pathFor;
// this is the one place the two file names meet.
func (s *Store) header(chatID marotte.ChatID) EntryHeader {
	return NewEntryHeader(filepath.Join(s.dir, string(chatID)))
}

// load reads a chat's header into memory. Returns os.ErrNotExist if the chat has
// no header.
func (s *Store) load(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, error) {
	if _, err := s.pathFor(chatID); err != nil {
		return nil, err
	}
	return s.header(chatID).Read(ctx)
}

// save stamps the chat's last-activity time and writes its header. Every
// mutation except a composer autosave goes through here, since every other
// mutation IS activity.
func (s *Store) save(ctx context.Context, chatID marotte.ChatID, chat *marotte.Chat) error {
	chat.UpdatedAt = time.Now().UnixMilli()
	return s.writeHeader(ctx, chatID, chat)
}

// writeHeader atomically replaces the chat's header, leaving UpdatedAt exactly as
// the caller left it — which is what SetDraft needs. The caller holds the per-chat
// mutex.
//
// THE DESTINATION IS THE ARGUMENT, and the object's own id is verified against it:
// a chat whose stored id is not its directory would otherwise overwrite the header
// that id names, under the requested id's lock.
func (s *Store) writeHeader(ctx context.Context, chatID marotte.ChatID, chat *marotte.Chat) error {
	if _, err := s.pathFor(chatID); err != nil {
		return err
	}
	if chat.ID != string(chatID) {
		return errChatIDMismatch(chatID, chat.ID)
	}
	return s.header(chatID).Write(ctx, chat)
}
