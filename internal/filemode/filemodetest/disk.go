// Package filemodetest provides an in-memory filemode.Disk, for tests whose subject writes private files
// often and whose assertions are not about the host filesystem.
package filemodetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
)

// Disk is a filemode.Disk holding regular files in memory, keyed by path. It models no directories: SyncDir always
// succeeds and every Commit is durable. Safe for concurrent use.
type Disk struct {
	files map[string]*file
	mu    sync.Mutex
	seq   int
}

var _ filemode.Disk = (*Disk)(nil)

// file is an inode: a handle keeps it after its path is replaced or removed, as an open descriptor does.
type file struct {
	data []byte
	mode fs.FileMode
}

// NewDisk returns an empty Disk.
func NewDisk() *Disk { return &Disk{files: map[string]*file{}} }

// WriteFile puts a regular file holding data at path, with the permission bits perm.
func (d *Disk) WriteFile(path string, data []byte, perm fs.FileMode) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[path] = &file{data: bytes.Clone(data), mode: perm.Perm()}
}

// Lstat describes the file at path; fs.ErrNotExist when nothing is there.
func (d *Disk) Lstat(path string) (fs.FileInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
	}
	return fileInfo(path, f), nil
}

// OpenNoFollow opens path for reading what it holds now.
func (d *Disk) OpenNoFollow(path string) (filemode.Handle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return &handle{d: d, f: f, name: path, r: bytes.NewReader(bytes.Clone(f.data))}, nil
}

// Remove removes path; fs.ErrNotExist when nothing is there.
func (d *Disk) Remove(path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.files[path]; !ok {
		return &fs.PathError{Op: "remove", Path: path, Err: fs.ErrNotExist}
	}
	delete(d.files, path)
	return nil
}

// SyncDir succeeds: the disk models no directories.
func (*Disk) SyncDir(string) error { return nil }

// Stage puts an empty 0600 file beside path, under a name of its own, as the host's Stage does.
func (d *Disk) Stage(ctx context.Context, path string) (filemode.Staged, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("filemodetest: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	name := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.stage-%d", filepath.Base(path), d.seq))
	f := &file{mode: 0o600}
	d.files[name] = f
	return &staged{d: d, f: f, name: name, path: path}, nil
}

func fileInfo(name string, f *file) fs.FileInfo {
	return info{name: filepath.Base(name), size: int64(len(f.data)), mode: f.mode}
}

type handle struct {
	d    *Disk
	f    *file
	r    *bytes.Reader
	name string
}

func (h *handle) Read(p []byte) (int, error) {
	if h.r == nil {
		return 0, io.EOF
	}
	return h.r.Read(p)
}

func (*handle) Close() error { return nil }

func (h *handle) Stat() (fs.FileInfo, error) {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return fileInfo(h.name, h.f), nil
}

func (h *handle) Chmod(mode fs.FileMode) error {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	h.f.mode = mode.Perm()
	return nil
}

type staged struct {
	handle
	path      string
	committed bool
}

func (s *staged) Name() string { return s.name }

func (s *staged) Write(p []byte) (int, error) {
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.f.data = append(s.f.data, p...)
	return len(p), nil
}

func (s *staged) Commit(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("filemodetest: %w", err)
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.committed = true
	delete(s.d.files, s.name)
	s.d.files[s.path] = s.f
	return true, nil
}

func (s *staged) Discard() error {
	if s.committed {
		return nil
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	if s.d.files[s.name] == s.f {
		delete(s.d.files, s.name)
	}
	return nil
}

type info struct {
	name string
	size int64
	mode fs.FileMode
}

func (i info) Name() string      { return i.name }
func (i info) Size() int64       { return i.size }
func (i info) Mode() fs.FileMode { return i.mode }
func (info) ModTime() time.Time  { return time.Time{} }
func (info) IsDir() bool         { return false }
func (info) Sys() any            { return nil }
