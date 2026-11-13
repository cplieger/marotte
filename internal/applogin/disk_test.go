package applogin

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/marotte/internal/filemode"
)

// errInjected is the error the test disk returns for an injected fault.
var errInjected = errors.New("test: injected disk fault")

// errNotDurableCommit, returned by an inject func for "commit", makes the commit happen and
// report durable false.
var errNotDurableCommit = errors.New("test: commit not durable")

type diskCall struct {
	op, path string
	owned    bool
}

// testDisk wraps the real seam for every store test: it fails the test on a
// call made while the store's diskOwned flag is clear, records every call and every Discard with
// the flag's value, counts durable publishes of access.json and injects faults.
type testDisk struct {
	t      testing.TB
	real   filemode.Disk
	path   string
	st     atomic.Pointer[Store]
	inject atomic.Pointer[func(op, path string) error]
	// onStore, when set, sees the store OpenStore builds over this disk before its writer starts.
	onStore   func(*Store)
	publishes atomic.Int64
	mu        sync.Mutex
	calls     []diskCall
	before    int // armed BeforePublish faults
	after     int // armed AfterPublish faults
}

var testDisks sync.Map // store path → *testDisk

func TestMain(m *testing.M) {
	newStoreHook = func(s *Store) {
		if v, ok := testDisks.Load(s.path); ok {
			d := v.(*testDisk)
			d.st.Store(s)
			if d.onStore != nil {
				d.onStore(s)
			}
		}
	}
	os.Exit(m.Run())
}

// newTestDisk makes a disk over a fresh directory's access.json.
func newTestDisk(t testing.TB) *testDisk {
	t.Helper()
	d := &testDisk{t: t, real: filemode.OS(), path: filepath.Join(t.TempDir(), "access.json")}
	testDisks.Store(d.path, d)
	t.Cleanup(func() { testDisks.Delete(d.path) })
	return d
}

func (d *testDisk) setInject(fn func(op, path string) error) {
	if fn == nil {
		d.inject.Store(nil)
		return
	}
	d.inject.Store(&fn)
}

// enter records op and returns the injected fault for it, failing the test when the store did not
// own the call. Discard is admitted unowned: a failed publication's leftover loop makes it.
func (d *testDisk) enter(op, path string) error {
	owned := true
	if s := d.st.Load(); s != nil {
		owned = s.diskOwned.Load()
	}
	d.mu.Lock()
	d.calls = append(d.calls, diskCall{op: op, path: path, owned: owned})
	d.mu.Unlock()
	if !owned && op != "discard" {
		d.t.Errorf("disk %s on %s made while diskOwned is clear", op, path)
	}
	if fn := d.inject.Load(); fn != nil {
		if err := (*fn)(op, path); err != nil {
			return err
		}
	}
	if path != d.path {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case op == "stage" && d.before > 0:
		d.before--
		return errInjected
	case op == "commit" && d.after > 0:
		d.after--
		return errNotDurableCommit
	}
	return nil
}

func (d *testDisk) recorded() []diskCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]diskCall(nil), d.calls...)
}

func (d *testDisk) discards() []diskCall {
	var out []diskCall
	for _, c := range d.recorded() {
		if c.op == "discard" {
			out = append(out, c)
		}
	}
	return out
}

func (d *testDisk) Lstat(path string) (fs.FileInfo, error) {
	if err := d.enter("lstat", path); err != nil {
		return nil, err
	}
	return d.real.Lstat(path)
}

func (d *testDisk) OpenNoFollow(path string) (filemode.Handle, error) {
	if err := d.enter("open", path); err != nil {
		return nil, err
	}
	h, err := d.real.OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	return &testHandle{Handle: h, d: d, path: path}, nil
}

func (d *testDisk) Remove(path string) error {
	if err := d.enter("remove", path); err != nil {
		return err
	}
	return d.real.Remove(path)
}

func (d *testDisk) SyncDir(dir string) error {
	if err := d.enter("syncdir", dir); err != nil {
		return err
	}
	return d.real.SyncDir(dir)
}

func (d *testDisk) Stage(ctx context.Context, path string) (filemode.Staged, error) {
	if err := d.enter("stage", path); err != nil {
		return nil, err
	}
	st, err := d.real.Stage(ctx, path)
	if err != nil {
		return nil, err
	}
	return &testStaged{Staged: st, d: d, path: path}, nil
}

type testHandle struct {
	filemode.Handle
	d    *testDisk
	path string
}

func (h *testHandle) Read(p []byte) (int, error) {
	if err := h.d.enter("read", h.path); err != nil {
		return 0, err
	}
	return h.Handle.Read(p)
}

func (h *testHandle) Stat() (fs.FileInfo, error) {
	if err := h.d.enter("handle.stat", h.path); err != nil {
		return nil, err
	}
	return h.Handle.Stat()
}

func (h *testHandle) Chmod(mode fs.FileMode) error {
	if err := h.d.enter("handle.chmod", h.path); err != nil {
		return err
	}
	return h.Handle.Chmod(mode)
}

type testStaged struct {
	filemode.Staged
	d    *testDisk
	path string
}

func (s *testStaged) Stat() (fs.FileInfo, error) {
	if err := s.d.enter("staged.stat", s.path); err != nil {
		return nil, err
	}
	return s.Staged.Stat()
}

func (s *testStaged) Chmod(mode fs.FileMode) error {
	if err := s.d.enter("staged.chmod", s.path); err != nil {
		return err
	}
	return s.Staged.Chmod(mode)
}

func (s *testStaged) Write(p []byte) (int, error) {
	if err := s.d.enter("write", s.path); err != nil {
		return 0, err
	}
	return s.Staged.Write(p)
}

func (s *testStaged) Commit(ctx context.Context) (bool, error) {
	err := s.d.enter("commit", s.path)
	if err != nil && !errors.Is(err, errNotDurableCommit) {
		return false, err
	}
	durable, cErr := s.Staged.Commit(ctx)
	if cErr != nil {
		return false, cErr
	}
	if err != nil {
		return false, nil
	}
	if durable && s.path == s.d.path {
		s.d.publishes.Add(1)
	}
	return durable, nil
}

func (s *testStaged) Discard() error {
	if err := s.d.enter("discard", s.path); err != nil {
		return err
	}
	return s.Staged.Discard()
}

// openTestStore opens a store over a fresh test disk and closes it at cleanup.
func openTestStore(t testing.TB) (*Store, *testDisk) {
	t.Helper()
	d := newTestDisk(t)
	s := d.open(t)
	return s, d
}

func (d *testDisk) open(t testing.TB) *Store {
	t.Helper()
	s, _, err := OpenStore(context.Background(), d.path, d)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// faults maps the contract's four hooks onto the test disk and the writer's hold.
func (d *testDisk) faults() accesstest.DurabilityFaults {
	return accesstest.DurabilityFaults{
		BeforePublish: func() { d.mu.Lock(); d.before++; d.mu.Unlock() },
		AfterPublish:  func() { d.mu.Lock(); d.after++; d.mu.Unlock() },
		Publishes:     func() int { return int(d.publishes.Load()) },
		HoldPublish: func() (func() int, func()) {
			s := d.st.Load()
			return s.holdPublish()
		},
	}
}
