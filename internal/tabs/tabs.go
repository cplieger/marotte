// Package tabs is the open-tab set: what is open, in what order, at what version. One record
// per tab, removal stated explicitly, one version for the collection. No HTTP or SSE here.
//
// The lock ordering is the correctness argument. Every mutation runs:
//
//	writeMu.Lock
//	  stateMu.Lock ; clone tabs + version ; stateMu.Unlock
//	  mutate and validate the clone ; persist the clone
//	  stateMu.Lock ; publish the clone ; stateMu.Unlock
//	writeMu.Unlock
//
// writeMu FIRST, or two opens clone one state and the second persists over the first
// (TestOpen_ConcurrentOpensSurviveInMemoryAndOnDisk). No path may hold stateMu while waiting
// on writeMu. List takes stateMu alone, so a reader never waits on an fsync.
package tabs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/asidefile"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// fileName is the store's file, beside the chats directory in the config dir.
const fileName = "tabs.json"

// fileMode and dirMode are what the file and the config directory must carry.
// The arrangement names chat ids and absolute paths, so it is nobody else's
// business on a shared host.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// MaxTabs is the DECODE bound against a hostile or broken writer, not a product number.
const MaxTabs = 500

// MaxOpenTabs is the PRODUCT limit; Open refuses past it with ErrTooMany. At the limit New
// chat stops working (it opens a tab), so the client says "close a tab first".
const MaxOpenTabs = 48

// MaxBytes caps a decoded document, derived from MaxTabs and maxRefBytes, and is enforced on
// read and write, so the store never writes a file its load would refuse.
const maxBytes = 512 * 1024

// maxRefBytes caps one subject's Ref (a chat id or an absolute path).
const maxRefBytes = 512

// Nothing this store mints is longer than 32 characters; the slack is for a hand-edited file, and
// the bound is what stops one entry from consuming the whole document budget.
const maxIDBytes = 128

// The sentinels this package returns, compared with errors.Is; each is wrapped with the
// offending value.
var (
	// ErrOrderMismatch means the ids handed to Reorder do not name every open
	// tab exactly once. Nothing is applied.
	ErrOrderMismatch = errors.New("tab order does not name every open tab exactly once")
	// ErrTooMany means MaxOpenTabs tabs are already open.
	ErrTooMany = errors.New("too many open tabs")
	// ErrBadKind means the kind is not a marotte.TabKind.
	ErrBadKind = errors.New("unknown tab kind")
	// ErrBadRef means the ref does not fit its kind: missing where the kind
	// needs one, present on a singleton, or over maxRefBytes.
	ErrBadRef = errors.New("bad tab ref")
	// errNotOpen means the id handed to Reparent names no open tab (unlike a pin, a reparent is a
	// statement about a tab, so it is refused).
	errNotOpen = errors.New("tab is not open")
	// ErrCycle means Reparent was asked to hang a tab under itself or under one
	// of its own descendants.
	ErrCycle = errors.New("a tab cannot be its own ancestor")
)

// file is the on-disk document: the tabs and the version they carry. No format version: an
// unreadable document already warns and starts empty.
type file struct {
	Tabs    []marotte.TabSubject `json:"tabs"`
	Owed    []OwedRelease        `json:"owed,omitempty"`
	Version uint64               `json:"version"`
}

// Store is the open-tab set, safe for concurrent use. The ZERO VALUE IS NOT USABLE; construct
// with NewStore. The slice IS the order; lookup indexes are per call so they cannot desync.
// The caller must hand version N's event to its hub before starting the mutation that makes
// N+1 (the membership coordinator's lock does); a slip costs a client re-list, not a tab.
type Store struct {
	// path, unread and refuseWrites are immutable after construction, so they are read without a lock.
	path    string
	unread  string
	tabs    []marotte.TabSubject // guarded by stateMu
	owed    []OwedRelease        // guarded by stateMu
	version uint64               // guarded by stateMu
	stateMu sync.Mutex           // guards tabs and version; held briefly, NEVER across I/O
	writeMu sync.Mutex           // serialises mutate-and-persist; the I/O lock
	// refuseWrites makes persist fail with errWriteRefused (see keepAside and findAside).
	refuseWrites bool
}

// NewStore opens (or starts) the store at <dir>/tabs.json. It ALWAYS returns a store; the error is
// diagnostic (warn and continue, invariant 6). An arrangement it cannot read is moved aside and the
// store starts empty with Unread set; see Unread for when writes are then refused.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, fileName)}
	if err := s.load(); err != nil {
		err = s.keepAside(err)
		return s, err
	}
	err := s.markEarlierAside(dir)
	return s, err
}

// load reads tabs.json; a missing file is a first run, not an error. The mode verdict comes BEFORE
// any read: filemode.EnforceFile refuses a planted symlink and a FIFO that would block boot.
func (s *Store) load() error {
	if _, err := filemode.EnforceFile(s.path, fileMode); err != nil {
		if asidefile.Absent(err) {
			return nil
		}
		// No read on this branch: the name is exactly what could not be verified.
		return fmt.Errorf("verify mode of %s: %w", s.path, err)
	}
	data, err := readBounded(s.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var doc file
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.tabs = sanitize(doc.Tabs)
	s.owed = sanitizeOwed(doc.Owed)
	// The version is adopted as read even when sanitize dropped an entry: no event is emitted at
	// load, and the list endpoint returns the sanitized set with its version.
	s.version = doc.Version
	return nil
}

// readBounded reads with EnforceFile's refusals and the size bound applied to the READ.
// Hand-rolled because filesafe.ReadBounded uses os.Open, which follows symlinks.
func readBounded(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("over the %d byte decode bound", maxBytes)
	}
	return data, nil
}

// A named type rather than a TabSubject with two fields set, because a half-populated record used
// as a key reads like a tab and is not one.
type subjectKey struct {
	kind marotte.TabKind
	ref  string
}

// sanitize drops each entry this store could not have written (intrinsic validity and the
// uniqueness rules) and truncates to MaxTabs. Referential integrity is Prune's.
func sanitize(in []marotte.TabSubject) []marotte.TabSubject {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.TabSubject, 0, min(len(in), MaxTabs))
	seenIDs := make(map[string]struct{}, min(len(in), MaxTabs))
	subjects := make(map[subjectKey]struct{}, min(len(in), MaxTabs))
	for _, t := range in {
		if t.ID == "" || len(t.ID) > maxIDBytes {
			continue
		}
		if checkSubject(t.Kind, t.Ref) != nil {
			continue
		}
		if _, dup := seenIDs[t.ID]; dup {
			continue
		}
		// A duplicate (Kind, Ref) is possible only in a hand-edited file; Open's idempotence needs one.
		key := subjectKey{kind: t.Kind, ref: t.Ref}
		if _, dup := subjects[key]; dup {
			continue
		}
		seenIDs[t.ID] = struct{}{}
		subjects[key] = struct{}{}
		sanitizeLaunch(&t)
		out = append(out, t)
		if len(out) == MaxTabs {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizeLaunch keeps a run tab's valid holder ids and clears the launch fields on every other kind. A dropped holder
// keeps its run-tab fact, since no close can name it.
func sanitizeLaunch(t *marotte.TabSubject) {
	if t.Kind != marotte.TabKindRun {
		t.LaunchChats, t.LaunchResolved = nil, false
		return
	}
	t.LaunchChats = slices.DeleteFunc(slices.Clone(t.LaunchChats), func(h string) bool { return !ids.ValidChatID(h) })
	if len(t.LaunchChats) == 0 {
		t.LaunchChats = nil
	}
}

// persist writes the whole document atomically. filesafe verifies the 0600 on the temp
// descriptor; do not add an EnforceFile by NAME after it.
func (s *Store) persist(ctx context.Context, st *state) error {
	if s.refuseWrites {
		return fmt.Errorf("%w: %s", errWriteRefused, s.unread)
	}
	data, err := json.MarshalIndent(file{Tabs: st.tabs, Owed: st.owed, Version: st.version}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tabs: %w", err)
	}
	if _, err := filesafe.WriteFile(ctx, s.path, data,
		filesafe.WithMode(fileMode), filesafe.WithMkdirMode(dirMode),
		filesafe.WithMaxBytes(maxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

// newID mints a tab id: hex of 16 bytes from crypto/rand (the id is an address a client
// sends back). rand.Read never fails since Go 1.24.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// checkSubject reports whether (kind, ref) names something this store can hold.
// Shared by Open (which refuses at the door) and sanitize (which drops), so the
// rule cannot differ between the wire and the file.
//
// Returns ErrBadKind or ErrBadRef.
func checkSubject(kind marotte.TabKind, ref string) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: %q", ErrBadKind, kind)
	}
	switch {
	case kind.Singleton() && ref != "":
		return fmt.Errorf("%w: kind %q is a singleton and takes no ref, got %q", ErrBadRef, kind, ref)
	case !kind.Singleton() && ref == "":
		return fmt.Errorf("%w: kind %q needs a ref", ErrBadRef, kind)
	case len(ref) > maxRefBytes:
		return fmt.Errorf("%w: ref is %d bytes, max %d", ErrBadRef, len(ref), maxRefBytes)
	}
	return nil
}
