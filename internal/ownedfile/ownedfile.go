// Package ownedfile is the one rule for a file marotte owns that is read absent or unreadable:
// what one read found (Classify), when a polling owner may act on it (Gate), and what a reader
// or a merge write builds on meanwhile (Reading, Merging). An absent or unreadable read is held
// until two consecutive polls agree, because an editor's rename-away save or a partial write
// looks like a removal or a broken file for a moment; while held, readers keep the owner's
// applied document.
package ownedfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/cplieger/go/filesafe"
)

// Kind is what one read of an owned file found.
type Kind uint8

// A read is Valid when it found a regular file whose bytes the owner accepts, Absent when it found
// nothing at the path, and Unreadable otherwise: refused or over-cap bytes, a non-regular file, or
// a stat or read error.
const (
	Valid Kind = iota + 1
	Absent
	Unreadable
)

// Read is one read of an owned file: its kind and the key two reads are compared by. Absent has
// no key; equal reads are equal values.
type Read struct {
	Key  string
	Kind Kind
}

// Options says how Classify opens the file. Limit 0 reads the whole file.
type Options struct {
	Limit              int64
	FollowFinalSymlink bool
}

// Classify reads path once and answers its kind, its bytes, the FileInfo of what it found, and,
// for an Unreadable read, why. It never blocks on a FIFO and never reads a non-regular file. A
// file over Limit is Unreadable (ErrFileTooLarge) with the Limit+1 bytes read; accept decides
// Valid, and its error is returned as is. data is non-nil (empty for an empty file) for every
// regular file read, nil for any other read. A missing path is Absent with a nil error.
func Classify(path string, o Options, accept func([]byte) error) (r Read, data []byte, info fs.FileInfo, err error) {
	f, info, err := open(path, o.FollowFinalSymlink)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Read{Kind: Absent}, nil, nil, nil
		}
		return unopened(path, o.FollowFinalSymlink, info, err)
	}
	defer func() { _ = f.Close() }()
	data, err = readAll(f, o.Limit)
	if err != nil {
		return Read{Kind: Unreadable, Key: errorKey(err)}, nil, info, err
	}
	key := sum(data)
	if o.Limit > 0 && int64(len(data)) > o.Limit {
		return Read{Kind: Unreadable, Key: key}, data, info,
			fmt.Errorf("%w: %s is over %d bytes", filesafe.ErrFileTooLarge, path, o.Limit)
	}
	if err := accept(data); err != nil {
		return Read{Kind: Unreadable, Key: key}, data, info, err
	}
	return Read{Kind: Valid, Key: key}, data, info, nil
}

func open(path string, follow bool) (*os.File, fs.FileInfo, error) {
	if !follow {
		return filesafe.OpenRegularNoFollow(path)
	}
	// O_NONBLOCK turns an open of a FIFO with no writer into a refusal below rather than a hang.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, info, &filesafe.NotRegularError{Name: path, Mode: info.Mode()}
	}
	return f, info, nil
}

// unopened keys a refused open: a non-regular file by its mode and identity, so a directory that
// stays put settles, and anything else by the error's text.
func unopened(path string, follow bool, info fs.FileInfo, err error) (Read, []byte, fs.FileInfo, error) {
	if info == nil {
		stat := os.Lstat
		if follow {
			stat = os.Stat
		}
		if fi, serr := stat(path); serr == nil && !fi.Mode().IsRegular() {
			info = fi
		}
	}
	if info == nil {
		return Read{Kind: Unreadable, Key: errorKey(err)}, nil, nil, err
	}
	// A socket's open fails with ENXIO before any descriptor exists to refuse.
	if !errors.Is(err, filesafe.ErrNotRegular) && !errors.Is(err, filesafe.ErrSymlinkTarget) {
		err = &filesafe.NotRegularError{Name: path, Mode: info.Mode()}
	}
	return Read{Kind: Unreadable, Key: identityKey(info)}, nil, info, err
}

func readAll(f *os.File, limit int64) ([]byte, error) {
	var r io.Reader = f
	if limit > 0 {
		r = io.LimitReader(f, limit+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return "sha256 " + hex.EncodeToString(s[:])
}

func identityKey(info fs.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("mode %v dev %d ino %d", info.Mode(), st.Dev, st.Ino)
	}
	return fmt.Sprintf("mode %v", info.Mode())
}

func errorKey(err error) string { return "error " + err.Error() }

// Gate holds an absent or unreadable read until two consecutive observations agree. The zero
// value holds nothing.
type Gate struct {
	last Read
	have bool
}

// Observe answers whether a poll may act on r: a valid read at once, ending any hold; any other
// read only when the previous observation was the same read; otherwise r becomes the held read.
// Once let through, the same read is let through again until another read or a Reset, so the
// owner dedups by the read it last acted on.
func (g *Gate) Observe(r Read) bool {
	if r.Kind == Valid {
		g.have = false
		return true
	}
	if g.have && g.last == r {
		return true
	}
	g.last, g.have = r, true
	return false
}

// Reset ends a hold. Every in-process write of the file calls it.
func (g *Gate) Reset() { g.have = false }

// Source is what a reader or a merge write builds on.
type Source uint8

// The sources Reading and Merging answer.
const (
	Disk    Source = iota + 1 // the bytes just read
	Applied                   // the owner's applied document
	Seed                      // the owner's seed
	Refused                   // the read's error
)

// Reading answers a reader's source for r, given the read the owner last applied (have is false
// before any): Disk for a valid read; Seed or Refused, by r's kind, for a read equal to applied or
// with nothing applied; otherwise (a held read) applied's own answer: Applied when it was valid,
// Seed when an absence, Refused when unreadable.
func Reading(r, applied Read, have bool) Source {
	switch {
	case r.Kind == Valid:
		return Disk
	case !have || r == applied:
		return byKind(r.Kind)
	case applied.Kind == Valid:
		return Applied
	default:
		return byKind(applied.Kind)
	}
}

// Merging is Reading, except that an unreadable r is always Refused: a merge over it would
// destroy the person's bytes.
func Merging(r, applied Read, have bool) Source {
	if r.Kind == Unreadable {
		return Refused
	}
	return Reading(r, applied, have)
}

func byKind(k Kind) Source {
	if k == Absent {
		return Seed
	}
	return Refused
}
