package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// fakeChat is what the fake store's PurgeIdle answers for one chat directory.
type fakeChat struct {
	lastUse time.Time
	err     error
	keptBy  string
	chain   []string
}

// fakeStore follows chat.Store.PurgeIdle's contract over directories under dir: a chat with no directory is
// absent, an error stays, a keep reason keeps with no deadline, and otherwise the age decides.
type fakeStore struct {
	chats map[marotte.ChatID]fakeChat
	dir   string
	calls int
	mu    sync.Mutex
}

func newFakeStore(dir string) *fakeStore {
	return &fakeStore{dir: dir, chats: make(map[marotte.ChatID]fakeChat)}
}

func (f *fakeStore) Dir() string { return f.dir }

func (f *fakeStore) PurgeIdle(_ context.Context, chatID marotte.ChatID, cutoff time.Time) (PurgeVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	c, ok := f.chats[chatID]
	if _, err := os.Stat(filepath.Join(f.dir, string(chatID))); err != nil || !ok {
		return PurgeVerdict{Outcome: PurgeAbsent}, nil
	}
	if c.err != nil {
		return PurgeVerdict{Outcome: PurgeKept}, c.err
	}
	if c.keptBy != "" {
		v := PurgeVerdict{Outcome: PurgeKept}
		if c.lastUse.Before(cutoff) {
			v.KeptBy = c.keptBy
		}
		return v, nil
	}
	if !c.lastUse.Before(cutoff) {
		return PurgeVerdict{Outcome: PurgeKept, Deadline: c.lastUse.Add(time.Since(cutoff))}, nil
	}
	if err := os.RemoveAll(filepath.Join(f.dir, string(chatID))); err != nil {
		return PurgeVerdict{Outcome: PurgeKept}, err
	}
	delete(f.chats, chatID)
	return PurgeVerdict{Outcome: PurgePurged, SessionChain: c.chain}, nil
}

func (f *fakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeStore) set(t *testing.T, id string, c fakeChat) string {
	t.Helper()
	p := filepath.Join(f.dir, id)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("mkdir chat %s: %v", id, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats[marotte.ChatID(id)] = c
	return p
}

// aged adds a chat last used age ago.
func (f *fakeStore) aged(t *testing.T, id string, age time.Duration) string {
	t.Helper()
	return f.set(t, id, fakeChat{lastUse: time.Now().Add(-age)})
}

// Safe for concurrent use because Purge calls it from worker goroutines.
type purgeRecorder struct {
	mu     sync.Mutex
	ids    []marotte.ChatID
	chains map[marotte.ChatID][]string
}

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
	out := make([]string, len(r.ids))
	for i, id := range r.ids {
		out[i] = string(id)
	}
	slices.Sort(out)
	return out
}

func newPurgeTestService(t *testing.T, onPurge func(marotte.ChatID, []string)) (*Service, *fakeStore) {
	t.Helper()
	if onPurge == nil {
		onPurge = func(marotte.ChatID, []string) {}
	}
	store := newFakeStore(t.TempDir())
	return New(store, onPurge), store
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
