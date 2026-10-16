package filemode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v4"
)

// TestEnforceFile_VerifiesTheModeItStored asserts that the returned mode is an observation of the file, not an
// echo of the argument.
func TestEnforceFile_VerifiesTheModeItStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode().Perm() != 0o666 {
		t.Skipf("filesystem stored %v for a 0o666 chmod; there is no drift here to enforce away", wfi.Mode().Perm())
	}

	stored, err := EnforceFile(path, 0o600)
	if err != nil {
		t.Fatalf("EnforceFile: %v", err)
	}
	if stored != 0o600 {
		t.Errorf("returned mode = %v, want 0600", stored)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("on-disk mode = %v, want 0600", got)
	}
	if got := fi.Mode().Perm(); got != stored.Perm() {
		t.Errorf("returned %v but disk holds %v: the return value is not an observation", stored, got)
	}
}

// TestEnforceFile_RefusesASymlinkAtTheName is the assertion that separates
// this primitive from the os.Chmod it replaced. os.Chmod(path) tightens whatever
// the NAME resolves to, so a symlink planted at the name sends the chmod at a
// file somebody else chose; O_NOFOLLOW makes the kernel refuse the open, so the
// target keeps the mode it had.
func TestEnforceFile_RefusesASymlinkAtTheName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatal(err)
	}
	tfi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if tfi.Mode().Perm() != 0o666 {
		t.Skipf("filesystem stored %v for a 0o666 chmod; a redirected chmod would be indistinguishable here", tfi.Mode().Perm())
	}

	link := filepath.Join(dir, "f.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := EnforceFile(link, 0o600); err == nil {
		t.Fatal("EnforceFile through a symlink = nil, want a refusal")
	}
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o666 {
		t.Errorf("symlink target mode = %v, want 0666: the chmod followed the name", got)
	}
}

// TestEnforceDir_VerifiesTheModeTheKernelStored uses the one widening a test can create for real: a
// setgid parent gives a new directory the setgid bit unasked.
func TestEnforceDir_VerifiesTheModeTheKernelStored(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(parent, "witness")
	if err := os.Mkdir(witness, 0o700); err != nil {
		t.Fatal(err)
	}
	wfi, err := os.Lstat(witness)
	if err != nil {
		t.Fatal(err)
	}
	if wfi.Mode()&os.ModeSetgid == 0 {
		t.Skipf("kernel did not widen a 0o700 mkdir under a setgid parent (got %v); "+
			"this test cannot distinguish a verified mode from an unverified one here", wfi.Mode())
	}

	dir := filepath.Join(parent, "kid")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored, err := EnforceDir(dir, 0o700)
	if err != nil {
		t.Fatalf("EnforceDir: %v", err)
	}
	if stored != 0o700 {
		t.Errorf("returned mode = %v, want 0700 (setgid must be gone)", stored)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode(); got != os.ModeDir|0o700 {
		t.Errorf("on-disk mode = %v, want %v: the mode the kernel stored was not corrected",
			got, os.ModeDir|0o700)
	}
}

// TestEnforceDir_RefusesANonDirectoryAtTheName pins the O_DIRECTORY half:
// a regular file planted where a directory is expected is refused rather than
// chmod'ed.
func TestEnforceDir_RefusesANonDirectoryAtTheName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := EnforceDir(path, 0o700); err == nil {
		t.Fatal("EnforceDir on a regular file = nil, want a refusal")
	}
}

// TestEnforceFile_LeavesAnAlreadyCorrectModeAlone pins the skip that keeps a
// read-only mount holding an already-0600 file quiet: the mode is read off the
// descriptor, and nothing is asked for when the answer is already right. Without
// it every boot on such a volume warns about an exposure that does not exist.
func TestEnforceFile_LeavesAnAlreadyCorrectModeAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := EnforceFile(path, 0o600)
	if err != nil {
		t.Fatalf("EnforceFile: %v", err)
	}
	if stored != 0o600 {
		t.Errorf("returned mode = %v, want 0600", stored)
	}
}

// TestEnforceFile_ReportsAMissingFileAsNotExist pins that a caller can still
// tell "no file here" from "a file I could not secure" — the mcp store's
// warn-only site and the secret store's refusal both read the error.
func TestEnforceFile_ReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := EnforceFile(filepath.Join(t.TempDir(), "absent.json"), 0o600)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want one matching os.ErrNotExist", err)
	}
}

// TestEnforceMode_SentinelIsTheLibrarysOwn asserts that a refused mode arrives as atomicfile.ErrModeNotStored,
// which the call sites match.
func TestEnforceMode_SentinelIsTheLibrarysOwn(t *testing.T) {
	if atomicfile.ErrModeNotStored == nil {
		t.Fatal("atomicfile.ErrModeNotStored is nil")
	}
	if errors.Is(atomicfile.ErrModeNotStored, os.ErrPermission) {
		t.Error("ErrModeNotStored matches os.ErrPermission; the two failures must stay distinguishable")
	}
}

func TestRewriteOptions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		perm        os.FileMode
		wantOpts    int
		wantRestore os.FileMode
	}{
		{name: "owner_only_is_enforced", perm: 0o600, wantOpts: 1},
		{name: "owner_only_exec_is_enforced", perm: 0o700, wantOpts: 1},
		{name: "group_readable_is_restored", perm: 0o640, wantRestore: 0o640},
		{name: "world_readable_script_is_restored", perm: 0o755, wantRestore: 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, restore := RewriteOptions(tc.perm)
			if len(opts) != tc.wantOpts || restore != tc.wantRestore {
				t.Errorf("RewriteOptions(%#o) = %d options, restore %#o; want %d options, restore %#o",
					tc.perm, len(opts), restore, tc.wantOpts, tc.wantRestore)
			}
			if tc.wantOpts == 0 {
				return
			}
			path := filepath.Join(t.TempDir(), "f")
			if _, err := atomicfile.WriteFile(t.Context(), path, []byte("x"), opts...); err != nil {
				t.Fatalf("WriteFile with RewriteOptions(%#o): %v", tc.perm, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := info.Mode().Perm(); got != tc.perm {
				t.Errorf("written mode = %#o, want %#o", got, tc.perm)
			}
		})
	}
}

// fakeDisk is the host disk with the faults a mode-widening filesystem shows, on read handles and
// staged writes alike: a mode reported as widen until a chmod takes, a chmod that fails (chmodErr)
// or does not take (stuck), a staged file that widens once written (widenOnWrite), and a failing
// stat, write, commit or discard of a staged file. A failed discard leaves the staged file in
// place, as atomicfile's failed removal does. notDurable makes a commit rename but report its
// directory unsynced, and syncErr fails SyncDir.
type fakeDisk struct {
	Disk
	chmodErr     error
	removeErr    error
	statErr      error
	writeErr     error
	commitErr    error
	discardErr   error
	syncErr      error
	removed      []string
	widen        os.FileMode
	widenOnWrite os.FileMode
	chmods       int
	stuck        bool
	notDurable   bool
	// exposed records a byte written while the staged file reported a mode isPrivate refuses.
	exposed bool
}

type fakeHandle struct {
	*os.File
	d *fakeDisk
}

type fakeStaged struct {
	Staged
	d *fakeDisk
}

type modeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (m modeInfo) Mode() os.FileMode { return m.mode }

func (d *fakeDisk) OpenNoFollow(path string) (Handle, error) {
	h, err := d.Disk.OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	return fakeHandle{File: h.(*os.File), d: d}, nil
}

func (d *fakeDisk) Stage(ctx context.Context, path string) (Staged, error) {
	st, err := d.Disk.Stage(ctx, path)
	if err != nil {
		return nil, err
	}
	return fakeStaged{Staged: st, d: d}, nil
}

func (d *fakeDisk) Remove(path string) error {
	d.removed = append(d.removed, path)
	if d.removeErr != nil {
		return d.removeErr
	}
	return d.Disk.Remove(path)
}

func (d *fakeDisk) SyncDir(dir string) error {
	if d.syncErr != nil {
		return d.syncErr
	}
	return d.Disk.SyncDir(dir)
}

func (d *fakeDisk) stat(h ModeHandle) (os.FileInfo, error) {
	if d.statErr != nil {
		return nil, d.statErr
	}
	fi, err := h.Stat()
	if err != nil || d.widen == 0 {
		return fi, err
	}
	return modeInfo{FileInfo: fi, mode: d.widen}, nil
}

func (d *fakeDisk) chmod(h ModeHandle, mode os.FileMode) error {
	d.chmods++
	if d.chmodErr != nil {
		return d.chmodErr
	}
	if err := h.Chmod(mode); err != nil {
		return err
	}
	if !d.stuck {
		d.widen = 0
	}
	return nil
}

func (h fakeHandle) Stat() (os.FileInfo, error)   { return h.d.stat(h.File) }
func (h fakeHandle) Chmod(mode os.FileMode) error { return h.d.chmod(h.File, mode) }
func (s fakeStaged) Stat() (os.FileInfo, error)   { return s.d.stat(s.Staged) }
func (s fakeStaged) Chmod(mode os.FileMode) error { return s.d.chmod(s.Staged, mode) }

func (s fakeStaged) Write(p []byte) (int, error) {
	if s.d.writeErr != nil {
		return 0, s.d.writeErr
	}
	if s.d.widen != 0 {
		s.d.exposed = true
	}
	if s.d.widenOnWrite != 0 {
		s.d.widen = s.d.widenOnWrite
	}
	return s.Staged.Write(p)
}

func (s fakeStaged) Commit(ctx context.Context) (bool, error) {
	if s.d.commitErr != nil {
		return false, s.d.commitErr
	}
	durable, err := s.Staged.Commit(ctx)
	return durable && !s.d.notDurable, err
}

func (s fakeStaged) Discard() error {
	if s.d.discardErr != nil {
		return s.d.discardErr
	}
	return s.Staged.Discard()
}

func writeAt(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"k":"v"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPrivate_ModeTable(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{"0600", 0o600, true},
		{"0400", 0o400, true},
		{"0200", 0o200, true},
		{"0640", 0o640, false},
		{"0601", 0o601, false},
		{"0604", 0o604, false},
		{"0404", 0o404, false},
		{"0660", 0o660, false},
		{"0700", 0o700, false},
		{"0600_setuid", 0o600 | os.ModeSetuid, false},
		{"0600_setgid", 0o600 | os.ModeSetgid, false},
		{"0600_sticky", 0o600 | os.ModeSticky, false},
		{"directory_0600", os.ModeDir | 0o600, false},
		{"symlink_0600", os.ModeSymlink | 0o600, false},
		{"fifo_0600", os.ModeNamedPipe | 0o600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := isPrivate(modeInfo{FileInfo: fi, mode: tc.mode}); got != tc.want {
				t.Errorf("isPrivate(%v) = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}

// TestOpenPrivate_RepairsAWidenedBirthAndKeepsTheFile is the boot case on a filesystem that births
// files group-writable: the bits are narrowed on the handle and the content is read, never removed.
func TestOpenPrivate_RepairsAWidenedBirthAndKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	writeAt(t, path, 0o660)
	data, err := ReadPrivate(OS(), path, 1024)
	if err != nil {
		t.Fatalf("ReadPrivate of a 0660 file: %v", err)
	}
	if string(data) != `{"k":"v"}` {
		t.Errorf("content = %q", data)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("the file is gone after the read: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("on-disk mode = %v, want 0600", got)
	}
}

func TestOpenPrivate_RechecksTheHandleAfterTheRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	writeAt(t, path, 0o600)
	d := &fakeDisk{Disk: OS(), widen: 0o644}
	h, err := openPrivate(d, path)
	if err != nil {
		t.Fatalf("openPrivate: %v", err)
	}
	_ = h.Close()
	if d.chmods != 1 {
		t.Errorf("chmods = %d, want 1", d.chmods)
	}
}

func TestOpenPrivate_RefusesAndKeepsAFileItCannotNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    *fakeDisk
	}{
		{"chmod_fails", &fakeDisk{Disk: OS(), widen: 0o660, chmodErr: errors.New("operation not permitted")}},
		{"chmod_does_not_take", &fakeDisk{Disk: OS(), widen: 0o660, stuck: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.json")
			writeAt(t, path, 0o600)
			_, err := openPrivate(tc.d, path)
			npe, ok := errors.AsType[*NotPrivateError](err)
			if !ok {
				t.Fatalf("error = %v, want a *NotPrivateError", err)
			}
			if npe.Mode.Perm() != 0o660 {
				t.Errorf("NotPrivateError = %+v, want mode 0660", npe)
			}
			if len(tc.d.removed) != 0 {
				t.Errorf("removed %v on a read", tc.d.removed)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("the file is gone after a refused read: %v", err)
			}
		})
	}
}

func TestOpenPrivate_RefusesASpecialBitWithoutChmod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	writeAt(t, path, 0o600)
	d := &fakeDisk{Disk: OS(), widen: 0o600 | os.ModeSetgid}
	if _, err := openPrivate(d, path); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("error = %v, want ErrNotPrivate", err)
	}
	if d.chmods != 0 {
		t.Errorf("chmods = %d, want 0: only permission bits are repaired", d.chmods)
	}
}

func TestOpenPrivate_RefusesASymlinkAndLeavesTheTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.json")
	writeAt(t, target, 0o644)
	link := filepath.Join(dir, "f.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := openPrivate(OS(), link)
	npe, ok := errors.AsType[*NotPrivateError](err)
	if !ok || npe.Mode&os.ModeSymlink == 0 {
		t.Fatalf("error = %v, want a *NotPrivateError naming a symlink", err)
	}
	fi, err := os.Lstat(target)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("target = %v, %v; want untouched at 0644", fi, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the link is gone: %v", err)
	}
}

func TestOpenPrivate_RefusesADirectoryAndAFIFO(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "d")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "p")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sub, fifo} {
		if _, err := openPrivate(OS(), path); !errors.Is(err, ErrNotPrivate) {
			t.Errorf("openPrivate(%s) = %v, want ErrNotPrivate", filepath.Base(path), err)
		}
	}
}

func TestOpenPrivate_AMissingFileIsNotExist(t *testing.T) {
	if _, err := openPrivate(OS(), filepath.Join(t.TempDir(), "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want os.ErrNotExist", err)
	}
}

func TestReadPrivate_RefusesAFileOverTheBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	writeAt(t, path, 0o600)
	if _, err := ReadPrivate(OS(), path, 3); !errors.Is(err, ErrTooLarge) {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
	if _, err := ReadPrivate(OS(), path, int64(len(`{"k":"v"}`))); err != nil {
		t.Errorf("a file exactly at the bound: %v", err)
	}
}

func TestWritePrivate_PublishesAt0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := WritePrivate(t.Context(), OS(), path, []byte("x")); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode())
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertPrevious checks that path still holds writeAt's content and that nothing else is in dir.
func assertPrevious(t *testing.T, dir, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"k":"v"}` {
		t.Errorf("previous file = %q, %v; want it intact", data, err)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != filepath.Base(path) {
		t.Errorf("directory holds %v, want only %s: a staged file was left behind", names, filepath.Base(path))
	}
}

func TestWritePrivate_RepairsAWidenedStagedFileBeforeWritingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	d := &fakeDisk{Disk: OS(), widen: 0o660}
	if err := WritePrivate(t.Context(), d, path, []byte("new")); err != nil {
		t.Fatalf("WritePrivate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Errorf("published = %q, %v", data, err)
	}
	if d.exposed {
		t.Error("data was written before the staged file was narrowed")
	}
}

func TestWritePrivate_AFileItCannotProtectIsNeverWrittenOrPublished(t *testing.T) {
	for _, tc := range []struct {
		d    *fakeDisk
		name string
	}{
		{name: "chmod_fails", d: &fakeDisk{Disk: OS(), widen: 0o660, chmodErr: errors.New("operation not permitted")}},
		{name: "chmod_does_not_take", d: &fakeDisk{Disk: OS(), widen: 0o660, stuck: true}},
		{name: "special_bit", d: &fakeDisk{Disk: OS(), widen: 0o600 | os.ModeSetgid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.json")
			writeAt(t, path, 0o600)
			err := WritePrivate(t.Context(), tc.d, path, []byte("new"))
			if _, ok := errors.AsType[*NotPrivateError](err); !ok {
				t.Errorf("error = %v, want a *NotPrivateError", err)
			}
			if tc.d.exposed {
				t.Error("data was written to a staged file not proved private")
			}
			if len(tc.d.removed) != 0 {
				t.Errorf("removed %v", tc.d.removed)
			}
			assertPrevious(t, dir, path)
		})
	}
}

// TestWritePrivate_AFileWidenedByTheWriteIsNeverPublished: the post-write check refuses instead of
// repairing, since the widened file already held the data.
func TestWritePrivate_AFileWidenedByTheWriteIsNeverPublished(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	writeAt(t, path, 0o600)
	d := &fakeDisk{Disk: OS(), widenOnWrite: 0o640}
	err := WritePrivate(t.Context(), d, path, []byte("new"))
	if npe, ok := errors.AsType[*NotPrivateError](err); !ok || npe.Mode.Perm() != 0o640 {
		t.Errorf("error = %v, want a *NotPrivateError naming 0640", err)
	}
	assertPrevious(t, dir, path)
}

func TestWritePrivate_AnyFailureKeepsThePreviousFile(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		d    *fakeDisk
		want error
		name string
	}{
		{name: "stage", ctx: cancelled, d: &fakeDisk{Disk: OS()}, want: context.Canceled},
		{name: "stat", ctx: t.Context(), d: &fakeDisk{Disk: OS(), statErr: syscall.EIO}, want: syscall.EIO},
		{name: "write", ctx: t.Context(), d: &fakeDisk{Disk: OS(), writeErr: syscall.ENOSPC}, want: syscall.ENOSPC},
		{name: "commit", ctx: t.Context(), d: &fakeDisk{Disk: OS(), commitErr: syscall.EIO}, want: syscall.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f.json")
			writeAt(t, path, 0o600)
			err := WritePrivate(tc.ctx, tc.d, path, []byte("new"))
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
			if _, ok := errors.AsType[*LeftoverError](err); ok {
				t.Errorf("error = %v, a leftover after a discard that succeeded", err)
			}
			if len(tc.d.removed) != 0 {
				t.Errorf("removed %v", tc.d.removed)
			}
			assertPrevious(t, dir, path)
		})
	}
}

// TestWritePrivate_AFailedDiscardHandsTheStagedFileToTheCaller: the staged file outlives the
// failed write, and the error is how the caller removes it.
func TestWritePrivate_AFailedDiscardHandsTheStagedFileToTheCaller(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	writeAt(t, path, 0o600)
	discardErr := errors.New("device busy")
	d := &fakeDisk{Disk: OS(), commitErr: syscall.EIO, discardErr: discardErr}
	err := WritePrivate(t.Context(), d, path, []byte("new"))
	lo, ok := errors.AsType[*LeftoverError](err)
	if !ok {
		t.Fatalf("error = %v, want a *LeftoverError", err)
	}
	if !errors.Is(err, syscall.EIO) || !errors.Is(err, discardErr) {
		t.Errorf("error = %v, want the commit failure joined with the discard failure", err)
	}
	if _, err := os.Lstat(lo.Name()); err != nil {
		t.Fatalf("the staged file the error names is not there: %v", err)
	}
	if err := lo.Discard(); !errors.Is(err, discardErr) {
		t.Errorf("Discard while the fault holds = %v, want the fault", err)
	}
	d.discardErr = nil
	if err := lo.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	assertPrevious(t, dir, path)
	if err := lo.Discard(); err != nil {
		t.Errorf("a Discard after the file is gone = %v, want nil", err)
	}
}

func TestWritePrivate_AnUnsyncedRenameIsPublishedButNotDurable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	writeAt(t, path, 0o600)
	d := &fakeDisk{Disk: OS(), notDurable: true}
	err := WritePrivate(t.Context(), d, path, []byte("new"))
	if !errors.Is(err, ErrNotDurable) {
		t.Fatalf("error = %v, want ErrNotDurable", err)
	}
	if _, ok := errors.AsType[*LeftoverError](err); ok {
		t.Errorf("error = %v, a leftover after a rename", err)
	}
	if data, rErr := os.ReadFile(path); rErr != nil || string(data) != "new" {
		t.Errorf("path = %q, %v; want the new content, which the rename published", data, rErr)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("directory holds %v, want only the published file", names)
	}
}

func TestRemoveDurably(t *testing.T) {
	syncErr := errors.New("fault: sync refused")
	for _, tc := range []struct {
		d           *fakeDisk
		wantErr     error
		name        string
		missing     bool
		notDurable  bool
		wantRemoved bool
	}{
		{name: "removed_and_synced", d: &fakeDisk{Disk: OS()}, wantRemoved: true},
		{name: "already_missing", d: &fakeDisk{Disk: OS()}, missing: true, wantRemoved: true},
		{name: "sync_fails", d: &fakeDisk{Disk: OS(), syncErr: syncErr}, wantErr: syncErr, notDurable: true, wantRemoved: true},
		{name: "remove_fails", d: &fakeDisk{Disk: OS(), removeErr: syscall.EBUSY}, wantErr: syscall.EBUSY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.json")
			if !tc.missing {
				writeAt(t, path, 0o600)
			}
			err := RemoveDurably(tc.d, path)
			if !errors.Is(err, tc.wantErr) || errors.Is(err, ErrNotDurable) != tc.notDurable {
				t.Errorf("RemoveDurably = %v, want %v, not durable %v", err, tc.wantErr, tc.notDurable)
			}
			_, sErr := os.Lstat(path)
			if gone := errors.Is(sErr, os.ErrNotExist); gone != tc.wantRemoved {
				t.Errorf("path gone = %v, want %v", gone, tc.wantRemoved)
			}
		})
	}
}

// TestStage_DiscardAfterAFailedCommitRemovesTheStagedFile: atomicfile's Cleanup does nothing once
// Commit was called and its own removal is best effort, so Discard must remove by name.
func TestStage_DiscardAfterAFailedCommitRemovesTheStagedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	st, err := OS().Stage(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := st.Commit(cancelled); err == nil {
		t.Fatal("Commit under a cancelled context = nil")
	}
	if err := os.WriteFile(st.Name(), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("directory holds %v after Discard, want nothing", names)
	}
}

func TestStage_ARefusedModeIsNotPrivate(t *testing.T) {
	for _, tc := range []struct {
		err         error
		name        string
		wantPrivate bool
	}{
		{name: "mode_not_stored", err: &atomicfile.WriteError{Phase: atomicfile.PhaseTempCreate, Err: atomicfile.ErrModeNotStored}, wantPrivate: true},
		{name: "io_error", err: &atomicfile.WriteError{Phase: atomicfile.PhaseTempCreate, Err: syscall.EIO}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := modeRefusal("/x/f.json", tc.err)
			_, isNP := errors.AsType[*NotPrivateError](err)
			if isNP != tc.wantPrivate || !errors.Is(err, tc.err) {
				t.Errorf("modeRefusal = %v: *NotPrivateError %v, want %v, and the cause kept", err, isNP, tc.wantPrivate)
			}
		})
	}
}
