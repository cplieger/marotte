// Package filemode makes a file's mode a fact rather than a request. A mode argument goes through
// umask and inheritable ACLs (an nfs4acl dataset stores 0660 for 0o600), so everything here reads
// the mode off a DESCRIPTOR.
package filemode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cplieger/atomicfile/v4"
)

// EnforceFile makes the regular file at path carry want and returns the mode the FILESYSTEM stored.
// atomicfile.EnforceMode fchmods and fstats ONE descriptor and refuses a mismatch with
// atomicfile.ErrModeNotStored.
// O_NOFOLLOW makes the kernel refuse a symlink planted at the name, and one descriptor cannot be
// redirected by a later rename. O_NONBLOCK must stay: these opens run on the boot path, where a
// FIFO at the name would block open(2) forever. A file already carrying want is left alone, so a
// read-only bind mount does not warn every boot.
func EnforceFile(path string, want os.FileMode) (os.FileMode, error) {
	return enforceMode(path, want, 0)
}

// EnforceDir is EnforceFile for a directory: O_DIRECTORY refuses a file or FIFO at the name. The
// comparison includes setgid on purpose: a setgid parent gives the bit unasked, which is a real
// request-versus-disk difference.
func EnforceDir(path string, want os.FileMode) (os.FileMode, error) {
	return enforceMode(path, want, syscall.O_DIRECTORY)
}

// enforceMode holds the shared sequence: open without following, read the mode
// off the descriptor, and ask only when the answer is wrong.
func enforceMode(path string, want os.FileMode, extraFlags int) (os.FileMode, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|extraFlags, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if stored := chmodBits(fi.Mode()); stored == chmodBits(want) {
		return stored, nil
	}
	return atomicfile.EnforceMode(f, want)
}

// chmodBits reduces a mode to the bits chmod(2) can set, mirroring the
// comparison atomicfile.EnforceMode makes internally so the skip above and the
// library's verdict cannot disagree about what "already correct" means.
func chmodBits(m os.FileMode) os.FileMode {
	return m.Perm() | m&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}

// ErrNotPrivate is what a *NotPrivateError matches.
var ErrNotPrivate = errors.New("filemode: not a private regular file")

// NotPrivateError reports a path that is not a private regular file.
type NotPrivateError struct {
	Path string
	// Mode is the mode found, zero when the filesystem refused to store 0600 before the mode could
	// be read (the joined cause then names it). A found mode is never zero: 0000 is private.
	Mode fs.FileMode
}

func (e *NotPrivateError) Error() string {
	if e.Mode == 0 {
		return fmt.Sprintf("filemode: %s could not be made a private regular file", e.Path)
	}
	return fmt.Sprintf("filemode: %s is not a private regular file (mode %v)", e.Path, e.Mode)
}

func (*NotPrivateError) Unwrap() error { return ErrNotPrivate }

// isPrivate reports whether fi is a regular file granting nothing beyond owner read and write. It is
// a bit test, never an ordered comparison against 0600: 0400 passes, 0404 and 0601 fail, and so
// does any setuid, setgid or sticky bit.
func isPrivate(fi fs.FileInfo) bool {
	m := fi.Mode()
	return m.IsRegular() && m.Perm()&^0o600 == 0 && m&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == 0
}

// ModeHandle is an open file whose mode can be read and set on the descriptor.
type ModeHandle interface {
	Stat() (fs.FileInfo, error)
	Chmod(mode fs.FileMode) error
}

// Handle is an open file as a Disk returns it.
type Handle interface {
	io.ReadCloser
	ModeHandle
}

// Staged is a new file beside its path, open for writing, not yet at the path.
type Staged interface {
	ModeHandle
	io.Writer
	// Name is the staged file's own path.
	Name() string
	// Commit atomically replaces the path with the staged file. Any error means the path still
	// holds what it held before. durable false with a nil error means the path holds the staged
	// file, but the rename may not survive a power loss.
	Commit(ctx context.Context) (durable bool, err error)
	// Discard removes the staged file unless Commit succeeded, after a failed Commit too. An error
	// means the file may still be there, and a later call retries.
	Discard() error
}

// Disk is the filesystem openPrivate, ReadPrivate, WritePrivate and RemoveDurably act on. OS is the
// host's; a test substitutes one to report a widened mode or fail a chmod.
type Disk interface {
	Lstat(path string) (fs.FileInfo, error)
	// OpenNoFollow opens path read-only, refusing a symlink at the final component and never
	// blocking on a FIFO.
	OpenNoFollow(path string) (Handle, error)
	Remove(path string) error
	// SyncDir flushes the directory dir's entries to stable storage.
	SyncDir(dir string) error
	// Stage creates an empty file beside path, exclusively, that replaces path on Commit. An error
	// leaves no staged file holding data.
	Stage(ctx context.Context, path string) (Staged, error)
}

type osDisk struct{}

// OS returns the host filesystem.
func OS() Disk { return osDisk{} }

func (osDisk) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

func (osDisk) OpenNoFollow(path string) (Handle, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func (osDisk) Remove(path string) error { return os.Remove(path) }

func (osDisk) SyncDir(dir string) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cErr := f.Close(); err == nil {
		err = cErr
	}
	return err
}

// Stage asks atomicfile for a 0600 birth, so even a filesystem that widens new files grants the
// empty file nothing before WritePrivate judges it. A temp atomicfile refuses is still empty, and
// atomicfile removes it only best effort.
func (osDisk) Stage(ctx context.Context, path string) (Staged, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	p, err := atomicfile.NewPendingFileInRoot(ctx, root, filepath.Base(path), atomicfile.WithMode(0o600))
	if err != nil {
		_ = root.Close()
		return nil, modeRefusal(path, err)
	}
	return &pendingFile{PendingFile: p, root: root, path: path, tmp: filepath.Base(p.Name())}, nil
}

// modeRefusal classes atomicfile's refusal to store 0600 as the *NotPrivateError a caller matches.
func modeRefusal(path string, err error) error {
	if errors.Is(err, atomicfile.ErrModeNotStored) {
		return fmt.Errorf("%w: %w", &NotPrivateError{Path: path}, err)
	}
	return err
}

// pendingFile adapts atomicfile's PendingFile, whose Commit error always means the rename did not
// happen (atomicfile.WriteError); a failed directory fsync after it is Result.Durable false. After
// any Commit call its Cleanup does nothing and its removal of a failed temp is best effort, so
// Discard then removes tmp by name in root, which this pendingFile keeps open until it is gone.
type pendingFile struct {
	*atomicfile.PendingFile
	root      *os.Root
	path      string
	tmp       string
	committed bool
	done      bool
}

func (p *pendingFile) Commit(ctx context.Context) (bool, error) {
	p.committed = true
	res, err := p.PendingFile.Commit(ctx)
	if err != nil {
		return false, modeRefusal(p.path, err)
	}
	p.finish()
	return res.Durable, nil
}

func (p *pendingFile) Discard() error {
	if p.done {
		return nil
	}
	var err error
	if p.committed {
		if err = p.root.Remove(p.tmp); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	} else {
		err = p.Cleanup()
	}
	if err != nil {
		return err
	}
	p.finish()
	return nil
}

func (p *pendingFile) finish() {
	p.done = true
	_ = p.root.Close()
}

// openPrivate opens path for reading only if it is a private regular file, and judges the open
// handle, never the name, so what was checked is what is read. A regular file whose only fault is
// its permission bits is narrowed to 0600 on the handle and judged again. It never removes
// anything: a file a mode-widening filesystem births group-writable must survive the read.
//
// Errors: one matching fs.ErrNotExist when nothing is at path; a *NotPrivateError for a symlink, a
// non-regular entry, a special bit, or bits still too wide after the repair (joined with the chmod
// failure when there was one); the filesystem's own error otherwise.
func openPrivate(d Disk, path string) (Handle, error) {
	fi, err := d.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &NotPrivateError{Path: path, Mode: fi.Mode()}
	}
	h, err := d.OpenNoFollow(path)
	if errors.Is(err, syscall.ELOOP) {
		return nil, &NotPrivateError{Path: path, Mode: fs.ModeSymlink}
	}
	if err != nil {
		return nil, err
	}
	if err := judgeHandle(h, path); err != nil {
		_ = h.Close()
		return nil, err
	}
	return h, nil
}

func judgeHandle(h ModeHandle, path string) error {
	fi, err := h.Stat()
	if err != nil {
		return err
	}
	if isPrivate(fi) {
		return nil
	}
	m := fi.Mode()
	if !m.IsRegular() || m&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return &NotPrivateError{Path: path, Mode: m}
	}
	if chErr := h.Chmod(0o600); chErr != nil {
		return fmt.Errorf("%w: %w", &NotPrivateError{Path: path, Mode: m}, chErr)
	}
	if fi, err = h.Stat(); err != nil {
		return err
	}
	if !isPrivate(fi) {
		return &NotPrivateError{Path: path, Mode: fi.Mode()}
	}
	return nil
}

// ErrTooLarge is what ReadPrivate returns for a file over its bound.
var ErrTooLarge = errors.New("filemode: file is larger than the read bound")

// ReadPrivate is openPrivate followed by a read of at most maxBytes from the same handle.
func ReadPrivate(d Disk, path string, maxBytes int64) ([]byte, error) {
	h, err := openPrivate(d, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = h.Close() }()
	data, err := io.ReadAll(io.LimitReader(h, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, path, maxBytes)
	}
	return data, nil
}

// LeftoverError is a failed WritePrivate whose staged file could not be removed. The path still
// holds what it held; the staged file is the caller's to remove with Discard, retried until nil.
type LeftoverError struct {
	staged Staged
	// Err is the write's failure joined with the removal's.
	Err error
}

func (e *LeftoverError) Error() string { return e.Err.Error() }

func (e *LeftoverError) Unwrap() error { return e.Err }

// Name is the staged file's path.
func (e *LeftoverError) Name() string { return e.staged.Name() }

// Discard retries the removal; nil means the staged file is gone.
func (e *LeftoverError) Discard() error { return e.staged.Discard() }

// ErrNotDurable is a change that reached the path but may not survive a power loss, because its
// directory could not be synced.
var ErrNotDurable = errors.New("filemode: the change may not survive a power loss")

// WritePrivate atomically replaces path with data. The staged file is proved private on its own
// handle (repaired as openPrivate repairs) while empty, written, verified again and renamed with
// its checked inode, so no byte reaches a file not proved private and none unprotected is at path.
//
// nil: path holds data, private and durable. ErrNotDurable: path holds data, private, but the
// rename may not survive a power loss. Otherwise path holds what it held; the staged file is gone
// unless the error is a *LeftoverError, and a *NotPrivateError is a staged file the filesystem
// would not make private.
func WritePrivate(ctx context.Context, d Disk, path string, data []byte) error {
	st, err := d.Stage(ctx, path)
	if err != nil {
		return err
	}
	err = judgeHandle(st, path)
	if err == nil {
		_, err = st.Write(data)
	}
	if err == nil {
		err = verifyHandle(st, path)
	}
	durable := false
	if err == nil {
		durable, err = st.Commit(ctx)
	}
	if err == nil {
		if !durable {
			return fmt.Errorf("%w: %s holds the new content", ErrNotDurable, path)
		}
		return nil
	}
	if dErr := st.Discard(); dErr != nil {
		return &LeftoverError{staged: st, Err: errors.Join(err, dErr)}
	}
	return err
}

// RemoveDurably removes the file at path and syncs its directory. A nil error means nothing is at
// path, durably, a missing file included. An error matching ErrNotDurable means path is gone but
// the removal may not survive a power loss; any other error means path still holds what it held.
func RemoveDurably(d Disk, path string) error {
	if err := d.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := d.SyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("%w: %s was removed: %w", ErrNotDurable, path, err)
	}
	return nil
}

// verifyHandle is the post-write check: it refuses rather than repairs, since a file that widened
// after the write already exposed what it holds.
func verifyHandle(h ModeHandle, path string) error {
	fi, err := h.Stat()
	if err != nil {
		return err
	}
	if !isPrivate(fi) {
		return &NotPrivateError{Path: path, Mode: fi.Mode()}
	}
	return nil
}

// RewriteOptions says how to rewrite an existing regular file of mode perm so
// it keeps its permission bits. An owner-only mode is enforced on the write. Any
// other mode comes back as restore, for the caller to chmod after the write, so
// a directory whose inherited ACL widens new files cannot refuse the save.
func RewriteOptions(perm os.FileMode) (opts []atomicfile.Option, restore os.FileMode) {
	if perm&0o077 == 0 {
		return []atomicfile.Option{atomicfile.WithMode(perm)}, 0
	}
	return nil, perm
}
