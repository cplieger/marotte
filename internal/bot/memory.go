package bot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// botsDir is KiroHome's directory holding one directory per Bot.
const botsDir = "bots"

const memoryName = "memory.md"

// memoryStartingText is a new memory file's text.
const memoryStartingText = "# Memory\n\nYour notes for future turns. Keep this file under 40,000 characters.\n"

var errLinked = errors.New("not a directory or regular file of its own (a link or another kind of file)")

// Dir is the Bot id's own directory, <KiroHome>/bots/<id>. False when id is not one folder name (ids.ValidChatID) or
// KiroHome is not absolute.
func Dir(id string, paths Paths) (string, bool) {
	if !ids.ValidChatID(id) || !filepath.IsAbs(paths.KiroHome) {
		return "", false
	}
	return filepath.Join(paths.KiroHome, botsDir, id), true
}

// EnsureMemory makes the Bot rec's directory (0700) and memory file (0600, the starting text) where either is missing,
// and keeps what exists. KiroHome must exist; a link at bots/, the Bot's directory or the file is refused and nothing
// is written through it.
func EnsureMemory(rec *marotte.Chat, paths Paths) error {
	root, file, err := openMemoryDir(rec.ID, paths)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return createMemory(root, file)
}

// ClearMemory rewrites the Bot rec's memory file to the starting text, making its directory and file where missing,
// with EnsureMemory's link refusals. The new text replaces the file by a rename, so a reader sees the old notes or the
// new text, never a part of either.
func ClearMemory(rec *marotte.Chat, paths Paths) error {
	root, file, err := openMemoryDir(rec.ID, paths)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return createMemory(root, file)
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s: %w", file, errLinked)
	}
	tmp := file + ".clear"
	_ = root.Remove(tmp)
	if err := createMemory(root, tmp); err != nil {
		return err
	}
	if err := root.Rename(tmp, file); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// RemoveDir removes the Bot id's directory and everything in it, and answers nil where there is none. A link at
// bots/ is refused, since os.Root would follow it to another KiroHome tree.
func RemoveDir(id string, paths Paths) error {
	if _, ok := Dir(id, paths); !ok {
		return fmt.Errorf("bot %q: no directory under an absolute KIRO_HOME", id)
	}
	root, err := os.OpenRoot(paths.KiroHome)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(botsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s: %w", botsDir, errLinked)
	}
	return root.RemoveAll(filepath.Join(botsDir, id))
}

// maxMemoryRead bounds the notes ReadMemory answers: the role asks for under 40,000 characters, and a Bot that
// writes past it must not size an API reply.
const maxMemoryRead = 1 << 20

// MemoryRead is a Bot's memory file as ReadMemory found it.
type MemoryRead struct {
	UpdatedAt time.Time
	Text      string
	// Bytes is the file's size, which is past len(Text) when the file holds more than maxMemoryRead.
	Bytes int64
}

// ReadMemory reads the Bot rec's memory file, found false with no error when there is none. A link at bots/, the
// Bot's directory or the file is refused.
func ReadMemory(rec *marotte.Chat, paths Paths) (MemoryRead, bool, error) {
	if _, ok := Dir(rec.ID, paths); !ok {
		return MemoryRead{}, false, fmt.Errorf("bot %q: no directory under an absolute KIRO_HOME", rec.ID)
	}
	root, err := os.OpenRoot(paths.KiroHome)
	if errors.Is(err, fs.ErrNotExist) {
		return MemoryRead{}, false, nil
	}
	if err != nil {
		return MemoryRead{}, false, err
	}
	defer func() { _ = root.Close() }()
	dir := filepath.Join(botsDir, rec.ID)
	file := filepath.Join(dir, memoryName)
	var linked fs.FileInfo
	for _, name := range []string{botsDir, dir, file} {
		info, statErr := root.Lstat(name)
		linked = info
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			return MemoryRead{}, false, nil
		case statErr != nil:
			return MemoryRead{}, false, statErr
		case name == file && !info.Mode().IsRegular(), name != file && !info.IsDir():
			return MemoryRead{}, false, fmt.Errorf("%s: %w", name, errLinked)
		}
	}
	f, err := root.Open(file)
	if err != nil {
		return MemoryRead{}, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return MemoryRead{}, false, err
	}
	// The file swapped for a link between the Lstat and the open is not the file the Lstat cleared.
	if !os.SameFile(info, linked) {
		return MemoryRead{}, false, fmt.Errorf("%s: %w", file, errLinked)
	}
	text, err := io.ReadAll(io.LimitReader(f, maxMemoryRead))
	if err != nil {
		return MemoryRead{}, false, err
	}
	return MemoryRead{UpdatedAt: info.ModTime(), Text: string(text), Bytes: info.Size()}, true, nil
}

// openMemoryDir opens KiroHome as a root and makes bots/ and the Bot id's directory in it, answering the memory
// file's name in that root.
func openMemoryDir(id string, paths Paths) (*os.Root, string, error) {
	if _, ok := Dir(id, paths); !ok {
		return nil, "", fmt.Errorf("bot %q: no directory under an absolute KIRO_HOME", id)
	}
	root, err := os.OpenRoot(paths.KiroHome)
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(botsDir, id)
	for _, d := range []string{botsDir, dir} {
		if err := ownDir(root, d); err != nil {
			_ = root.Close()
			return nil, "", err
		}
	}
	return root, filepath.Join(dir, memoryName), nil
}

// ownDir makes name in root at 0700 unless it exists, then requires it to be a directory and no link: os.Root follows
// a link that stays in root, which would point the Bot's files at another KiroHome tree.
func ownDir(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", name, errLinked)
	}
	return nil
}

// createMemory writes the starting text to a new file at name. O_EXCL refuses an existing name without following a
// link there, so an existing regular file is kept and anything else is refused.
func createMemory(root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: %w", name, errLinked)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(memoryStartingText)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// A partial file would be kept as the Bot's notes by the next open.
		_ = root.Remove(name)
	}
	return err
}
