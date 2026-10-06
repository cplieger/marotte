// Package filemode makes a file's mode a fact rather than a request. A mode argument goes through
// umask and inheritable ACLs (an nfs4acl dataset stores 0660 for 0o600), so everything here reads
// the mode off a DESCRIPTOR.
package filemode

import (
	"os"
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
