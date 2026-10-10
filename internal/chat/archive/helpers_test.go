package archive

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

type fakeStore struct {
	dir   string
	mu    sync.Mutex
	locks map[marotte.ChatID]*sync.Mutex
	// removals counts successful Removes; its value is the `chats` version the
	// fake mints, so a purge's chat_deleted stamp is checkable.
	removals int
	// header, when non-nil, makes LoadRetentionHeader succeed with this
	// projection (default: it fails, the unreadable-chat path).
	header *RetentionHeader
}

func newFakeStore(dir string) *fakeStore {
	return &fakeStore{dir: dir, locks: make(map[marotte.ChatID]*sync.Mutex)}
}

func (f *fakeStore) Dir() string { return f.dir }

func (f *fakeStore) Lock(chatID marotte.ChatID) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.locks[chatID]
	if !ok {
		m = &sync.Mutex{}
		f.locks[chatID] = m
	}
	return m
}

func (f *fakeStore) LoadRetentionHeader(marotte.ChatID) (RetentionHeader, error) {
	if f.header != nil {
		return *f.header, nil
	}
	return RetentionHeader{}, errors.New("fakeStore: chat is unreadable")
}

func (f *fakeStore) Remove(chatID marotte.ChatID) (string, error) {
	dir := filepath.Join(f.dir, string(chatID))
	if _, err := os.Stat(dir); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removals++
	return strconv.Itoa(f.removals), nil
}

// Safe for concurrent use because Purge calls it from worker goroutines.
type purgeRecorder struct {
	mu     sync.Mutex
	ids    []marotte.ChatID
	chains map[marotte.ChatID][]string
}

// recordPurge satisfies WithOnPurge, keeping the session chain the purge
// handed over so tests can assert the chat's sessions were offered for reaping.
func (r *purgeRecorder) recordPurge(id marotte.ChatID, sessionChain []string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	if r.chains == nil {
		r.chains = map[marotte.ChatID][]string{}
	}
	r.chains[id] = sessionChain
	r.mu.Unlock()
}

func (r *purgeRecorder) chainFor(id marotte.ChatID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chains[id]
}

func (r *purgeRecorder) sorted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return idsToSortedStrings(r.ids)
}

func idsToSortedStrings(ids []marotte.ChatID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	slices.Sort(out)
	return out
}

func newPurgeTestService(t *testing.T, opts ...Option) (*Service, *fakeStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := newFakeStore(dir)
	return New(store, opts...), store, dir
}

// With no projection set on the fake, purgeReferenceTime falls through to the header MTIME this
// sets.
func writeAgedChat(t *testing.T, dir, id string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, id), 0o700); err != nil {
		t.Fatalf("mkdir aged chat %s: %v", id, err)
	}
	p := filepath.Join(dir, id, headerFileName)
	if err := os.WriteFile(p, []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatalf("write aged chat %s: %v", id, err)
	}
	if age != 0 {
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("chtimes %s: %v", id, err)
		}
	}
	return p
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}
