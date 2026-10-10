// Package specapproval records which phase of which spec a human signed off, against exactly
// which document version. It RECORDS and enforces nothing: it preserves the approved version
// so the client can say "changed since you approved it".
//
// One 0600 file beside the chats directory (not beside the docs, which are often a git
// tree). Shaped like internal/tabs: whole-document atomic rewrites, mode verified on a
// descriptor, a malformed file warns and starts empty (invariant 6). Unlike tabs it MERGES a
// phase into the record under the write lock, so a phase approved in between is not dropped.
package specapproval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/marotte"
)

// fileName is the store's file, beside the chats directory in the config dir.
const fileName = "spec-approvals.json"

// fileMode and dirMode are what the file and the config directory must carry.
// The record names workspace paths and, once marotte knows an identity, who
// approved what; neither is anybody else's business on a shared host.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// maxBytes caps a decoded document, derived from the bounds below, and is enforced on both
// the read and the write, so the store never writes a file its load would refuse.
const maxBytes = 256 * 1024

// maxSpecs is the DECODE bound: the most specs this store reads or holds.
const maxSpecs = 500

// maxDirBytes caps one spec's key (a workspace-relative directory).
const maxDirBytes = 512

// maxUserBytes caps a recorded user. Nothing writes one yet, so it bounds a hand-edited file.
const maxUserBytes = 256

// The sentinels this package returns, each wrapped with the offending value.
var (
	// errBadPhase means the phase is not an approvable one. See Phases.
	errBadPhase = errors.New("not an approvable spec phase")
	// errBadHash means the hash is not a sha256 hex digest.
	errBadHash = errors.New("hash must be a sha256 hex digest")
	// errBadDir means the spec dir is empty or over MaxDirBytes.
	errBadDir = errors.New("bad spec dir")
	// ErrTooMany means maxSpecs specs already carry an approval. Approving a
	// phase of a spec already in the record is never refused by it.
	ErrTooMany = errors.New("too many specs carry an approval")
)

// sha256Hex matches a lowercase sha256 hex digest, which is what internal/spec
// hashes a document to.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Phases is the closed set a phase may name, DERIVED from marotte.SpecDocRole: every role but
// other (the residual bucket, which can hold several documents), so a new role cannot be
// silently unapprovable.
func Phases() []marotte.SpecDocRole {
	return []marotte.SpecDocRole{
		marotte.SpecDocRoleRequirements,
		marotte.SpecDocRoleDesign,
		marotte.SpecDocRoleTasks,
	}
}

// ValidHash reports whether hash is a lowercase sha256 hex digest (internal/spec's hash).
// Exported so the command boundary refuses a malformed hash with this store's one grammar.
func ValidHash(hash string) bool {
	return sha256Hex.MatchString(hash)
}

// ValidPhase reports whether phase is one of Phases.
func ValidPhase(phase string) bool {
	for _, p := range Phases() {
		if string(p) == phase {
			return true
		}
	}
	return false
}

// Stale is absent: it is derived per read (Derive).
type record struct {
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
	User string    `json:"user"`
}

// file is the on-disk document: one entry per spec dir, each a phase-keyed map. No version
// field: an unreadable document already warns and starts empty.
type file struct {
	Specs map[string]map[string]record `json:"specs"`
}

// Store is the approval record. Safe for concurrent use by multiple goroutines.
// The ZERO VALUE IS NOT USABLE — it would persist to the empty path; construct
// with NewStore.
type Store struct {
	specs map[string]map[string]record // guarded by stateMu
	// path is immutable after construction, so it is read without a lock.
	path    string
	stateMu sync.Mutex // guards specs; held briefly, NEVER across I/O
	writeMu sync.Mutex // serialises merge-and-persist; the I/O lock
}

// NewStore opens (or starts) the store at <dir>/spec-approvals.json. It ALWAYS returns a
// usable store; the error is diagnostic (warn and continue). The mode verdict comes BEFORE any
// read: filemode.EnforceFile refuses a planted symlink and a FIFO that would block boot.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, fileName), specs: map[string]map[string]record{}}
	if _, err := filemode.EnforceFile(s.path, fileMode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil // first run
		}
		// No read on this branch: the name is exactly what could not be verified.
		return s, fmt.Errorf("verify mode of %s (starting empty): %w", s.path, err)
	}
	data, err := readBounded(s.path)
	if err != nil {
		return s, fmt.Errorf("read %s (starting empty): %w", s.path, err)
	}
	var doc file
	if err := json.Unmarshal(data, &doc); err != nil {
		return s, fmt.Errorf("parse %s (starting empty): %w", s.path, err)
	}
	s.specs = sanitize(doc.Specs)
	return s, nil
}

// readBounded reads with EnforceFile's refusals and the size bound applied to the READ.
// Hand-rolled because atomicfile.ReadBounded uses os.Open, which follows symlinks.
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

// sanitize drops each entry this store could not have written (intrinsic validity only) and
// truncates to maxSpecs, so a bad entry costs one badge, not the whole record.
func sanitize(in map[string]map[string]record) map[string]map[string]record {
	out := make(map[string]map[string]record, min(len(in), maxSpecs))
	// Map order is random, so which specs survive a truncation is unspecified, deliberately.
	for dir, phases := range in {
		if len(out) == maxSpecs {
			break
		}
		if !validDir(dir) {
			continue
		}
		kept := make(map[string]record, len(phases))
		for phase, r := range phases {
			if !ValidPhase(phase) || !sha256Hex.MatchString(r.Hash) || len(r.User) > maxUserBytes {
				continue
			}
			kept[phase] = r
		}
		if len(kept) == 0 {
			continue
		}
		out[dir] = kept
	}
	return out
}

func validDir(dir string) bool {
	return dir != "" && len(dir) <= maxDirBytes
}

// Approve records that a human approved phase of the spec at dir, against hash, MERGED into
// the record under the write lock. Re-approving overwrites. Whether hash still matches the
// document is the caller's precondition. Returns ErrBadDir, ErrBadPhase, errBadHash or
// ErrTooMany; on any error nothing is applied.
func (s *Store) Approve(ctx context.Context, dir, phase, hash, user string) error {
	switch {
	case !validDir(dir):
		return fmt.Errorf("%w: %q", errBadDir, dir)
	case !ValidPhase(phase):
		return fmt.Errorf("%w: %q", errBadPhase, phase)
	case !sha256Hex.MatchString(hash):
		return fmt.Errorf("%w: %q", errBadHash, hash)
	case len(user) > maxUserBytes:
		return fmt.Errorf("%w: %d bytes", errBadDir, len(user))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	next := s.snapshot()
	if _, known := next[dir]; !known {
		if len(next) >= maxSpecs {
			return fmt.Errorf("%w: %d specs, limit %d", ErrTooMany, len(next), maxSpecs)
		}
		next[dir] = map[string]record{}
	}
	next[dir][phase] = record{Hash: hash, At: time.Now().UTC(), User: user}

	if err := s.persist(ctx, next); err != nil {
		return err
	}
	s.publish(next)
	return nil
}

// For returns the stored approvals for one spec, keyed by phase, with Stale UNSET (pass them
// to Derive). Nil when the spec carries none.
func (s *Store) For(dir string) map[string]marotte.SpecApproval {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	phases := s.specs[dir]
	if len(phases) == 0 {
		return nil
	}
	out := make(map[string]marotte.SpecApproval, len(phases))
	for phase, r := range phases {
		out[phase] = marotte.SpecApproval{Hash: r.Hash, At: r.At, User: r.User}
	}
	return out
}

// Derive fills Stale by comparing each approved hash with the phase's document NOW. A gone
// document is stale, not omitted; an over-cap one (empty hash) is stale too. For a shared
// role the FIRST document in display order decides, the one the reader approved.
func Derive(approvals map[string]marotte.SpecApproval, docs []marotte.SpecDoc) map[string]marotte.SpecApproval {
	if len(approvals) == 0 {
		return nil
	}
	live := make(map[string]string, len(docs))
	for _, d := range docs {
		role := string(d.Role)
		if _, seen := live[role]; !seen {
			live[role] = d.Hash
		}
	}
	out := make(map[string]marotte.SpecApproval, len(approvals))
	for phase, a := range approvals {
		a.Stale = live[phase] == "" || live[phase] != a.Hash
		out[phase] = a
	}
	return out
}

// snapshot deep-clones the record under stateMu, so a failed write cannot change what
// readers see through shared inner maps.
func (s *Store) snapshot() map[string]map[string]record {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	out := make(map[string]map[string]record, len(s.specs))
	for dir, phases := range s.specs {
		out[dir] = maps.Clone(phases)
	}
	return out
}

// Called only after the clone is durable, so what a reader sees is always what is on disk.
func (s *Store) publish(next map[string]map[string]record) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.specs = next
}

// persist writes the whole document atomically. atomicfile verifies the 0600 on the temp
// descriptor; do not add an EnforceFile by NAME after it.
func (s *Store) persist(ctx context.Context, next map[string]map[string]record) error {
	data, err := json.MarshalIndent(file{Specs: next}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode spec approvals: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
