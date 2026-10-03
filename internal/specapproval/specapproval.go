// Package specapproval records which phase of which spec a human signed off,
// and against exactly which version of the document.
//
// # It records, it does not enforce
//
// Nothing here gates anything. There is no phase-order rule, no refusal to run a
// task because design is unapproved, and no control the record disables. The
// agent writes a spec's documents through its own file tools, so this server
// cannot enforce an order without owning that access; what it CAN do is preserve
// the version a person approved, which is what makes the client's "changed since
// you approved it" badge sayable at all.
//
// # Why it is a file beside the chats directory
//
// This is the sixth 0600 file in the config dir, beside mcp.json,
// mcp-secrets.json, schedules.json, runs.json and tabs.json, and it is NOT a
// third root for the chat log (marotte invariant 5 forbids that, and this store
// holds no entries and no turns). Two other homes were considered and refused:
//
//   - .kiro/specs/<name>/.approvals.json, beside the docs, which is where the
//     record conceptually belongs. Refused because a spec directory is very often
//     a git working tree — this workspace's own .kiro repo is one — so marotte
//     would drop an untracked file into somebody's `git status` on every
//     approval.
//   - .config.kiro. Refused: that file is KAS's, and marotte does not write it.
//
// # Shape
//
// internal/tabs' shape, for internal/tabs' reasons: one server-owned collection,
// a whole-document atomic rewrite per mutation, the mode VERIFIED on a descriptor
// rather than requested, and a malformed file WARNS AND STARTS EMPTY rather than
// failing boot (invariant 6 — an approval is re-creatable by approving again, and
// refusing to boot over it would leave no way in to repair it).
//
// The one thing this store does that internal/tabs does not is MERGE: a phase is
// written into the record that is already there, under the write lock, rather
// than by reading the map out, editing it and stamping it back. This is the field
// where losing a record silently defeats the point of having it, so a second
// phase approved in between must not be dropped.
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

// FileName is the store's file, beside the chats directory in the config dir.
const FileName = "spec-approvals.json"

// fileMode and dirMode are what the file and the config directory must carry.
// The record names workspace paths and, once marotte knows an identity, who
// approved what; neither is anybody else's business on a shared host.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// MaxBytes caps a decoded document, derived from the bounds below rather than
// picked: MaxSpecs specs each carrying every phase at MaxDirBytes is well under
// 256 KiB of indented JSON. Enforced on the way in (the read) and on the way out
// (atomicfile's WithMaxBytes), so the store refuses to write a file its own load
// path would refuse to read.
const MaxBytes = 256 * 1024

// MaxSpecs is the DECODE bound: the most specs this store will read out of a
// file, and the most it will hold. A workspace has tens of specs; the bound is an
// outer wall against a hostile or broken writer.
const MaxSpecs = 500

// MaxDirBytes caps one spec's key. A key is a workspace-relative directory, so
// the reasoning is internal/tabs' MaxRefBytes': PATH_MAX is 4096 on Linux while
// every path this store can be handed comes from marotte's own spec roots.
const MaxDirBytes = 512

// MaxUserBytes caps a recorded user. Nothing writes one today (SpecApproval.User
// is documented empty), so this bounds a hand-edited file rather than a producer.
const MaxUserBytes = 256

// The sentinels this package returns, compared with errors.Is. Each is wrapped
// with the value that offended it, so a caller can report WHAT was wrong without
// parsing a message.
var (
	// ErrBadPhase means the phase is not an approvable one. See Phases.
	ErrBadPhase = errors.New("not an approvable spec phase")
	// ErrBadHash means the hash is not a sha256 hex digest.
	ErrBadHash = errors.New("hash must be a sha256 hex digest")
	// ErrBadDir means the spec dir is empty or over MaxDirBytes.
	ErrBadDir = errors.New("bad spec dir")
	// ErrTooMany means MaxSpecs specs already carry an approval. Approving a
	// phase of a spec already in the record is never refused by it.
	ErrTooMany = errors.New("too many specs carry an approval")
)

// sha256Hex matches a lowercase sha256 hex digest, which is what internal/spec
// hashes a document to.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Phases is the closed set a phase may name, and it is DERIVED from
// marotte.SpecDocRole rather than listed independently: it is every role except
// other. One statement rather than a second list that can disagree with the role
// vocabulary the loader already assigns, so a role added there cannot be silently
// unapprovable.
//
// other is excluded because it is not a phase — it is the residual bucket, and
// one spec directory can hold several other documents, so a per-phase key could
// not name which of them was approved.
//
// Kiro Crew approves two phases (requirements and design) and leaves tasks out,
// because its own workflow's third gate is the run question rather than a review.
// marotte's third gate is the run question too (the phase checkpoint carries it),
// and tasks is still a document a person reads and signs off, so it stays in.
func Phases() []marotte.SpecDocRole {
	return []marotte.SpecDocRole{
		marotte.SpecDocRoleRequirements,
		marotte.SpecDocRoleDesign,
		marotte.SpecDocRoleTasks,
	}
}

// ValidHash reports whether hash is a lowercase sha256 hex digest, which is what
// internal/spec hashes a document to.
//
// Exported so the command boundary refuses a malformed hash BEFORE it reads a
// document, and so the one grammar has one owner: a caller checking the shape
// itself could disagree with what this store will accept.
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

// record is one phase's stored approval. Stale is deliberately absent: it is
// derived per read against the document's live hash (see Derive), so storing it
// would be a second copy of a fact that goes wrong the moment the file moves.
type record struct {
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
	User string    `json:"user"`
}

// file is the on-disk document: one entry per spec dir, each a phase-keyed map.
//
// There is no format version field, for internal/tabs' reason: the answer to a
// document this store cannot read is already to warn and start empty, and an
// approval is re-creatable by approving again.
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

// NewStore opens (or starts) the store at <dir>/spec-approvals.json.
//
// It ALWAYS returns a usable store; the error is diagnostic and the caller's job
// is to WARN AND CONTINUE. internal/tabs' choice and for its reason: an approval
// is re-creatable by approving again, so refusing to boot would take the app down
// over a record with no way in to repair it.
//
// THE MODE VERDICT COMES FIRST, before anything reads the bytes, and the ordering
// is load-bearing rather than tidy. filemode.EnforceFile opens with
// O_NOFOLLOW|O_NONBLOCK and reads the mode off the DESCRIPTOR, so a symlink
// planted at the name is refused rather than having its target parsed as the
// record, and a FIFO at the name is refused rather than blocking os.ReadFile
// inside open(2) forever — on the boot path, which is how a widened mode becomes
// a container that never finishes starting.
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName), specs: map[string]map[string]record{}}
	if _, err := filemode.EnforceFile(s.path, fileMode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil // first run
		}
		// Deliberately no read on this branch: the name is exactly what could
		// not be verified.
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

// readBounded reads the document with the same refusals filemode.EnforceFile
// made, and with the size bound applied to the READ rather than after it.
//
// Hand-rolled over atomicfile.ReadBounded for the reason its own doc states: that
// helper uses os.Open, which FOLLOWS symlinks, so using it right after
// EnforceFile refused one would give the refusal away again. io.LimitReader is
// what makes the bound real — a length check after os.ReadFile has already
// allocated the hostile file.
func readBounded(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("over the %d byte decode bound", MaxBytes)
	}
	return data, nil
}

// sanitize drops every entry that could not have come from this store, and
// truncates to MaxSpecs.
//
// Individually rather than by failing the document, internal/tabs' reasoning: the
// failure mode should be one missing badge rather than a record that will not
// load at all. It checks INTRINSIC validity only — the key's shape, the phase's
// membership, the hash's shape. Whether the document still exists is not
// knowable here and is Derive's question.
func sanitize(in map[string]map[string]record) map[string]map[string]record {
	out := make(map[string]map[string]record, min(len(in), MaxSpecs))
	// Iterated in map order, which is random, so WHICH specs survive a truncation
	// is unspecified. Deliberate: a file over the bound was not written by this
	// store, and picking a winner would imply an order the document does not
	// carry.
	for dir, phases := range in {
		if len(out) == MaxSpecs {
			break
		}
		if !validDir(dir) {
			continue
		}
		kept := make(map[string]record, len(phases))
		for phase, r := range phases {
			if !ValidPhase(phase) || !sha256Hex.MatchString(r.Hash) || len(r.User) > MaxUserBytes {
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

// validDir reports whether dir is a usable key.
func validDir(dir string) bool {
	return dir != "" && len(dir) <= MaxDirBytes
}

// Approve records that a human approved phase of the spec at dir, against hash.
//
// The MERGE is inside the write lock, and that is the point of this method
// existing rather than a Set that takes the whole map: a phase is written into
// whatever the record already holds, so a second phase approved in between is not
// dropped. Re-approving a phase overwrites its record, which is what a re-review
// of a moved document means.
//
// Validation is the CALLER's compare-and-swap precondition, not this store's
// question: whether hash still matches the document is decided by whoever can
// read that document through the confined loader. This method validates only what
// it stores.
//
// Returns ErrBadDir, ErrBadPhase, ErrBadHash or ErrTooMany. On any error nothing
// is applied.
func (s *Store) Approve(ctx context.Context, dir, phase, hash, user string) error {
	switch {
	case !validDir(dir):
		return fmt.Errorf("%w: %q", ErrBadDir, dir)
	case !ValidPhase(phase):
		return fmt.Errorf("%w: %q", ErrBadPhase, phase)
	case !sha256Hex.MatchString(hash):
		return fmt.Errorf("%w: %q", ErrBadHash, hash)
	case len(user) > MaxUserBytes:
		return fmt.Errorf("%w: %d bytes", ErrBadDir, len(user))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	next := s.snapshot()
	if _, known := next[dir]; !known {
		if len(next) >= MaxSpecs {
			return fmt.Errorf("%w: %d specs, limit %d", ErrTooMany, len(next), MaxSpecs)
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

// For returns the stored approvals for one spec, keyed by phase, with Stale
// UNSET — deriving it needs the documents, which this store does not read. Pass
// the result to Derive.
//
// Nil when the spec carries none, which is the common case.
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

// Derive fills Stale on each approval by comparing the hash that was approved
// against the phase's document as it is NOW.
//
// A phase whose document is GONE is stale, not omitted: the approval still
// records that somebody signed something off, and dropping it would report the
// spec as never reviewed. A document over the read cap carries an empty hash
// (internal/spec lists it with TooLarge and no content), so it reads as stale
// too — which is honest, since nothing can say whether those bytes moved.
//
// Where two documents share a role (requirements.md and bugfix.md both map to
// requirements), the FIRST in display order decides, which is the document the
// client's phase segment shows and therefore the one a reader approved.
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

// snapshot deep-clones the record under stateMu. DEEP because the value is
// itself a map: a shallow clone would hand a mutation the published inner maps,
// so a failed write would already have changed what readers see.
func (s *Store) snapshot() map[string]map[string]record {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	out := make(map[string]map[string]record, len(s.specs))
	for dir, phases := range s.specs {
		out[dir] = maps.Clone(phases)
	}
	return out
}

// publish installs a mutated clone. Called only after the clone is durable, so
// what a reader sees is always what is on disk.
func (s *Store) publish(next map[string]map[string]record) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.specs = next
}

// persist writes the whole document atomically.
//
// A full rewrite per mutation rather than an incremental edit, internal/tabs' and
// internal/schedule's reasoning: the document is a few kilobytes and a rewrite is
// one publication either way.
//
// THE 0600 IS VERIFIED BY THIS WRITE, not by a pass afterwards.
// atomicfile.WriteFile enforces the mode on the OPEN TEMP DESCRIPTOR (fchmod then
// fstat, one handle) and fails rather than publishing a wider file, and the rename
// publishes that same verified inode. Do not add a filemode.EnforceFile call
// after this: it would re-verify by NAME, which is the weaker check.
func (s *Store) persist(ctx context.Context, next map[string]map[string]record) error {
	data, err := json.MarshalIndent(file{Specs: next}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode spec approvals: %w", err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(MaxBytes)); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
